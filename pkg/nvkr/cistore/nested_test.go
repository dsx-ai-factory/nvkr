// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func nestedLocatorFixture(t *testing.T) (Request, Locator, map[string]NestedArchive) {
	t.Helper()
	request := testRequest
	locator := githubAdapterLocator(request)
	member := ".nvkr/carriers/payload.tar"
	locator.Paths, locator.StripPrefix = []string{member}, ".nvkr/carriers"
	return request, locator, map[string]NestedArchive{request.Name: {Member: member, Allowed: []string{"payload"}}}
}

func writeNestedLocator(t *testing.T, directory, name string, locator Locator) {
	t.Helper()
	raw, err := canonical.Marshal(locator)
	if err != nil {
		t.Fatal(err)
	}
	writePayload(t, directory, name+locatorSuffix, string(raw), 0o600)
}

func TestNestedGitHubIndexAdmissionPrecedesEveryNetworkRequest(t *testing.T) {
	for _, test := range []struct {
		name string
		code e.Code
	}{
		{"missing", e.CodeReleaseIncomplete},
		{"extra", e.CodeVerifyFailed},
		{"duplicate", e.CodeVerifyFailed},
		{"wrong request", e.CodeVerifyFailed},
		{"wrong carrier", e.CodeVerifyFailed},
		{"wrong strip", e.CodeVerifyFailed},
		{"non-locator", e.CodeVerifyFailed},
		{"nested index directory", e.CodeVerifyFailed},
		{"symlink locator", e.CodeVerifyFailed},
		{"overlapping roots", e.CodeUsage},
		{"existing destination", e.CodeImmutableViolation},
		{"symlink destination parent", e.CodeImmutableViolation},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, locator, selections := nestedLocatorFixture(t)
			index, destination := t.TempDir(), t.TempDir()
			switch test.name {
			case "wrong request":
				locator.Request.Attempt = "999"
			case "wrong carrier":
				locator.Paths = []string{".nvkr/carriers/other.tar"}
			case "wrong strip":
				locator.StripPrefix = ".nvkr"
			case "overlapping roots":
				selections["second"] = NestedArchive{Member: ".nvkr/carriers/second.tar", Allowed: []string{"payload/child"}}
				request.Name = ""
			case "existing destination":
				writePayload(t, destination, "payload", "retained", 0o600)
			case "symlink destination parent":
				selections[request.Name] = NestedArchive{Member: locator.Paths[0], Allowed: []string{"link/child"}}
				if err := os.Symlink(t.TempDir(), filepath.Join(destination, "link")); err != nil {
					t.Fatal(err)
				}
			}
			if test.name != "missing" {
				writeNestedLocator(t, index, locator.Request.Name, locator)
			}
			switch test.name {
			case "extra":
				writePayload(t, index, "extra", "unselected", 0o600)
			case "duplicate":
				writeNestedLocator(t, filepath.Join(index, locator.Request.Name), locator.Request.Name, locator)
			case "non-locator":
				if err := os.Rename(filepath.Join(index, locator.Request.Name+locatorSuffix), filepath.Join(index, "other")); err != nil {
					t.Fatal(err)
				}
			case "nested index directory":
				if err := os.MkdirAll(filepath.Join(index, locator.Request.Name, "nested"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "symlink locator":
				name := filepath.Join(index, locator.Request.Name+locatorSuffix)
				if err := os.Rename(name, name+"-held"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(name+"-held", name); err != nil {
					t.Fatal(err)
				}
			}
			backend := newBackend()
			backend.client.Transport = nestedForbiddenNetwork{t}
			backend.transport = nestedForbiddenNetwork{t}
			err := backend.downloadGitHubNested(t.Context(), request, index, destination, selections, testOptions)
			assertCode(t, err, test.code)
			if test.name == "existing destination" {
				raw, err := os.ReadFile(filepath.Join(destination, "payload"))
				if err != nil || string(raw) != "retained" {
					t.Fatal("index admission replaced an existing destination")
				}
			}
		})
	}
}

type nestedForbiddenNetwork struct{ t *testing.T }

func (n nestedForbiddenNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	n.t.Fatal("invalid nested index reached network admission or payload transfer")
	return nil, io.ErrUnexpectedEOF
}

func TestNestedIndexSupportsBothProviderLayouts(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		t.Run(map[bool]string{false: "merged", true: "artifact directory"}[grouped], func(t *testing.T) {
			request, locator, selections := nestedLocatorFixture(t)
			index := t.TempDir()
			directory := index
			if grouped {
				directory = filepath.Join(index, request.Name)
			}
			writeNestedLocator(t, directory, request.Name, locator)
			got, err := readNestedIndex(t.Context(), request, index, selections, false)
			if err != nil || len(got) != 1 || got[request.Name].Reference != locator.Reference {
				t.Fatalf("exact index rejected: %v", err)
			}
		})
	}
}

func TestNestedChunkRetryNeverMaterializesCarrierOrSkipsOuterVerification(t *testing.T) {
	for _, test := range []struct {
		name           string
		corrupt        bool
		cleanupFailure bool
	}{
		{name: "recovered"},
		{name: "outer digest rejected", corrupt: true},
		{name: "failed attempt cleanup", cleanupFailure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writePayload(t, root, "payload/data", strings.Repeat("nested bytes ", 300), 0o640)
			member := ".nvkr/carriers/payload.tar"
			if err := transportarchive.Pack(t.Context(), filepath.Join(root, filepath.FromSlash(member)), root, []string{"payload"}); err != nil {
				t.Fatal(err)
			}
			inner, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(member)))
			if err != nil {
				t.Fatal(err)
			}
			var outer bytes.Buffer
			writer := tar.NewWriter(&outer)
			if err := writer.WriteHeader(&tar.Header{Name: member, Mode: 0o644, Size: int64(len(inner)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(inner); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			layers, digest, err := chunkArchive(t.Context(), bytes.NewReader(outer.Bytes()), int64(outer.Len()), 1024)
			if err != nil {
				t.Fatal(err)
			}
			locator := Locator{SchemaVersion: SchemaVersion, Request: testRequest, ArchiveSize: int64(outer.Len()), ArchiveDigest: digest, Paths: []string{member}, StripPrefix: ".nvkr/carriers"}
			image, err := carrierImage(layers, locator)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := image.Manifest()
			if err != nil {
				t.Fatal(err)
			}
			last := len(layers) - 1
			original, opens := layers[last], 0
			layers[last] = editedChunk{Layer: original, open: func() (io.ReadCloser, error) {
				opens++
				if opens == 1 {
					return io.NopCloser(failingChunkBody{}), nil
				}
				return original.Compressed()
			}}
			if test.corrupt {
				locator.ArchiveDigest = "sha256:" + strings.Repeat("0", 64)
			}
			restored := filepath.Join(t.TempDir(), "restored")
			waits := 0
			err = unpackChunkAttempts(t.Context(), layers, manifest, restored, func(stream *chunkReader) error {
				return transportarchive.UnpackNestedStream(t.Context(), stream, locator.ArchiveSize, locator.ArchiveDigest, member, restored, []string{"payload"})
			}, func(context.Context, time.Duration) error {
				waits++
				if _, err := os.Lstat(restored); !os.IsNotExist(err) {
					t.Fatal("failed nested attempt survived into retry")
				}
				return nil
			}, func(path string) error {
				if err := os.RemoveAll(path); err != nil {
					return err
				}
				if test.cleanupFailure {
					return io.ErrClosedPipe
				}
				return nil
			})
			if test.cleanupFailure {
				assertCode(t, err, e.CodeImmutableViolation)
				if opens != 1 || waits != 0 || e.Retryable(err) {
					t.Fatal("failed nested cleanup permitted a new attempt")
				}
				return
			}
			if opens != 2 || waits != 1 {
				t.Fatalf("nested retry did not restart exactly once: %d/%d", opens, waits)
			}
			if test.corrupt {
				assertCode(t, err, e.CodeVerifyFailed)
				if _, statErr := os.Lstat(restored); !os.IsNotExist(statErr) {
					t.Fatal("outer failure published inner roots")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(restored, filepath.FromSlash(member))); !os.IsNotExist(err) {
				t.Fatal("nested restore wrote a factory carrier")
			}
			if raw, err := os.ReadFile(filepath.Join(restored, "payload/data")); err != nil || string(raw) != strings.Repeat("nested bytes ", 300) {
				t.Fatal("recovered nested payload differs")
			}
		})
	}
}
