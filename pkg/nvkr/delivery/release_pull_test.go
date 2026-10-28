// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestPullExecutableAdmitsBeforeWork(t *testing.T) {
	ref := "registry.example.test/product/releases@sha256:" + strings.Repeat("a", 64)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name, reference, output, out string
		ctx                          context.Context
		options                      RegistryOptions
		code                         e.Code
	}{
		{"nil context", ref, "tools", "out", nil, RegistryOptions{}, e.CodeUsage},
		{"canceled", ref, "tools", "out", canceled, RegistryOptions{}, e.CodeTimeout},
		{"tag", "registry.example.test/releases:latest", "tools", "out", t.Context(), RegistryOptions{}, e.CodeUnverifiedRef},
		{"implicit reference", "releases@sha256:" + strings.Repeat("a", 64), "tools", "out", t.Context(), RegistryOptions{}, e.CodeUnverifiedRef},
		{"missing output", ref, "", "out", t.Context(), RegistryOptions{}, e.CodeUsage},
		{"unsafe output", ref, "../tools", "out", t.Context(), RegistryOptions{}, e.CodeUsage},
		{"missing destination", ref, "tools", "", t.Context(), RegistryOptions{}, e.CodeUsage},
		{"current directory", ref, "tools", ".", t.Context(), RegistryOptions{}, e.CodeUsage},
		{"partial credentials", ref, "tools", "out", t.Context(), RegistryOptions{Username: "user"}, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := filepath.Join(t.TempDir(), "scratch")
			bundle, err := PullExecutable(test.ctx, test.reference, test.output, test.out, workspace, test.options)
			assertRegistryCode(t, err, test.code)
			if !reflect.DeepEqual(bundle, BinaryBundle{}) {
				t.Fatalf("failed admission returned a bundle: %+v", bundle)
			}
			if _, err := os.Lstat(workspace); !os.IsNotExist(err) {
				t.Fatalf("admission touched workspace: %v", err)
			}
		})
	}
}

func TestPullExecutableRejectsWorkspaceInsideDestination(t *testing.T) {
	for _, test := range []struct {
		name, child string
		alias       bool
	}{
		{"same path", ".", false},
		{"nested path", "scratch", false},
		{"symlink alias", "scratch", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			out := filepath.Join(parent, "tools")
			workspace := filepath.Join(out, test.child)
			if test.alias {
				alias := filepath.Join(t.TempDir(), "parent")
				if err := os.Symlink(parent, alias); err != nil {
					t.Fatal(err)
				}
				workspace = filepath.Join(alias, "tools", test.child)
			}
			_, err := PullExecutable(t.Context(), "registry.example.test/releases@sha256:"+strings.Repeat("a", 64),
				"tools", out, workspace, RegistryOptions{})
			assertRegistryCode(t, err, e.CodeUsage)
			assertEmptyPullDirectory(t, parent)
		})
	}
}

func TestPulledExecutableRequiresExactReleaseMembership(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*BinaryBundle, *PublishedObject)
		code e.Code
	}{
		{"exact", func(*BinaryBundle, *PublishedObject) {}, ""},
		{"different component", func(_ *BinaryBundle, object *PublishedObject) { object.Output.Components = []string{"other"} }, e.CodeVerifyFailed},
		{"transport path", func(_ *BinaryBundle, object *PublishedObject) { object.Output.TransportPath = "files" }, e.CodeVerifyFailed},
		{"platform", func(_ *BinaryBundle, object *PublishedObject) { object.Arch = "arm64" }, e.CodeVerifyFailed},
		{"semantic identity", func(_ *BinaryBundle, object *PublishedObject) { object.SemanticDigest = digestBytes([]byte("other")) }, e.CodeVerifyFailed},
		{"expanded size", func(bundle *BinaryBundle, object *PublishedObject) {
			bundle.Entries[0].Size = maxDeliveryFileBytes + 1
			object.SemanticDigest, _ = bundle.Digest()
		}, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			bundle := BinaryBundle{SchemaVersion: BinaryBundleSchemaVersion, OS: "linux", Arch: "amd64", Purpose: "tools",
				Entries: []BinaryEntry{{Name: "cli", InstallPath: "/usr/bin/cli", Mode: 0o755, Size: 1, SHA256: digestBytes([]byte("x"))}}}
			semantic, err := bundle.Digest()
			if err != nil {
				t.Fatal(err)
			}
			object := PublishedObject{OS: "linux", Arch: "amd64", SemanticDigest: semantic, Output: &OutputBinding{Components: []string{"cli"}}}
			test.edit(&bundle, &object)
			err = validatePulledBundle(bundle, object)
			if test.code != "" {
				assertRegistryCode(t, err, test.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPullExecutableSelectsOnlyExactOutputPayload(t *testing.T) {
	fixture := pullExecutableFixture(t)
	for _, test := range []struct {
		name, output, failure string
		code                  e.Code
		payload               bool
	}{
		{name: "selected executable", output: "tools", payload: true},
		{name: "authenticated executable", output: "tools", failure: "authenticated", payload: true},
		{name: "unknown exact output", output: "cli", code: e.CodeUsage},
		{name: "unsupported disk output", output: "node-image", code: e.CodeEngineCapability},
		{name: "corrupt unselected metadata", output: "tools", failure: "metadata", code: e.CodeFetchFailed},
		{name: "corrupt selected payload", output: "tools", failure: "payload", code: e.CodeVerifyFailed, payload: true},
		{name: "existing directory", output: "tools", failure: "existing", code: e.CodeImmutableViolation, payload: true},
		{name: "destination symlink", output: "tools", failure: "symlink", code: e.CodeImmutableViolation, payload: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var active atomic.Bool
			var selectedGets, otherGets atomic.Int64
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.failure == "authenticated" {
					username, password, ok := r.BasicAuth()
					if !ok || username != "pull-user" || password != "pull-password" {
						w.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
				}
				if active.Load() && r.Method == http.MethodGet {
					if strings.HasSuffix(r.URL.Path, "/blobs/"+fixture.diskLayer) {
						otherGets.Add(1)
						http.Error(w, "unselected payload is inaccessible", http.StatusForbidden)
						return
					}
					if strings.HasSuffix(r.URL.Path, "/blobs/"+fixture.layer) {
						selectedGets.Add(1)
						if test.failure == "payload" {
							raw := append([]byte(nil), fixture.layerBytes...)
							raw[0] ^= 1
							_, _ = w.Write(raw)
							return
						}
					}
					if test.failure == "metadata" && strings.HasSuffix(r.URL.Path, "/blobs/"+fixture.diskConfig) {
						_, _ = w.Write([]byte("substituted metadata"))
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			options := RegistryOptions{Anonymous: true, PlainHTTP: true}
			if test.failure == "authenticated" {
				options = RegistryOptions{PlainHTTP: true, Username: "pull-user", Password: "pull-password"}
			}
			reference, err := Push(t.Context(), fixture.root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
			if err != nil {
				t.Fatal(err)
			}
			active.Store(true)
			parent, workspace := t.TempDir(), t.TempDir()
			out := filepath.Join(parent, "tools")
			if test.failure == "existing" {
				if err := os.Mkdir(out, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if test.failure == "symlink" {
				if err := os.Symlink(t.TempDir(), out); err != nil {
					t.Fatal(err)
				}
			}
			bundle, err := PullExecutable(t.Context(), reference, test.output, out, workspace, options)
			if (selectedGets.Load() > 0) != test.payload || otherGets.Load() != 0 {
				t.Fatalf("wrong selected payload traffic: selected=%d other=%d", selectedGets.Load(), otherGets.Load())
			}
			assertEmptyPullDirectory(t, workspace)
			if test.code != "" {
				assertRegistryCode(t, err, test.code)
				if !reflect.DeepEqual(bundle, BinaryBundle{}) {
					t.Fatalf("failed pull returned bundle: %+v", bundle)
				}
				switch test.failure {
				case "existing":
					assertEmptyPullDirectory(t, out)
				case "symlink":
					info, err := os.Lstat(out)
					if err != nil || info.Mode()&os.ModeSymlink == 0 {
						t.Fatalf("destination symlink replaced: %v", err)
					}
				default:
					assertEmptyPullDirectory(t, parent)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(bundle.Entries) != 1 || bundle.Entries[0].Name != "cli" {
				t.Fatalf("wrong bundle: %+v", bundle)
			}
			raw, err := os.ReadFile(filepath.Join(out, "cli"))
			if err != nil || string(raw) != "exact executable" {
				t.Fatalf("wrong executable: %q, %v", raw, err)
			}
			info, err := os.Stat(filepath.Join(out, "cli"))
			if err != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("executable mode not retained: %v, %v", info, err)
			}
		})
	}
}

func TestPullExecutableCancellationRemovesPrivateWork(t *testing.T) {
	fixture := pullExecutableFixture(t)
	var active atomic.Bool
	started := make(chan struct{})
	var once sync.Once
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if active.Load() && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/blobs/"+fixture.layer) {
			once.Do(func() { close(started) })
			<-r.Context().Done()
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := RegistryOptions{Anonymous: true, PlainHTTP: true}
	reference, err := Push(t.Context(), fixture.root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", options)
	if err != nil {
		t.Fatal(err)
	}
	active.Store(true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	parent, workspace := t.TempDir(), t.TempDir()
	finished := make(chan error, 1)
	go func() {
		_, err := PullExecutable(ctx, reference, "tools", filepath.Join(parent, "tools"), workspace, options)
		finished <- err
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(10 * time.Second):
		t.Fatal("pull did not start selected payload request")
	}
	select {
	case err := <-finished:
		assertRegistryCode(t, err, e.CodeTimeout)
	case <-time.After(10 * time.Second):
		t.Fatal("canceled pull did not join")
	}
	assertEmptyPullDirectory(t, parent)
	assertEmptyPullDirectory(t, workspace)
}

func TestPulledExecutableRejectsMalformedArchivesWithoutOutput(t *testing.T) {
	const payload = "executable bytes"
	var archive bytes.Buffer
	tarWriter := tar.NewWriter(&archive)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "files/cli", Size: int64(len(payload)), Mode: 0o751, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tarWriter, payload); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	gzipBytes := func(raw []byte) []byte {
		var compressed bytes.Buffer
		writer := gzip.NewWriter(&compressed)
		if _, err := writer.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return compressed.Bytes()
	}
	valid := gzipBytes(archive.Bytes())
	badTrailer := append([]byte(nil), valid...)
	badTrailer[len(badTrailer)-1] ^= 1
	// GNU long-name records are consumed internally by tar.Next and never appear
	// as output entries. Repetition must still hit the total expanded byte bound.
	var extension bytes.Buffer
	extensionWriter := tar.NewWriter(&extension)
	if err := extensionWriter.WriteHeader(&tar.Header{Name: "longname", Size: 10, Mode: 0o644, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}); err != nil {
		t.Fatal(err)
	}
	if _, err := extensionWriter.Write([]byte("files/cli\x00")); err != nil {
		t.Fatal(err)
	}
	if err := extensionWriter.Close(); err != nil {
		t.Fatal(err)
	}
	extensionBytes := append([]byte(nil), extension.Bytes()[:1024]...)
	extensionBytes[156] = tar.TypeGNULongName
	copy(extensionBytes[148:156], "        ")
	var checksum int
	for _, value := range extensionBytes[:512] {
		checksum += int(value)
	}
	copy(extensionBytes[148:156], fmt.Sprintf("%06o\x00 ", checksum))
	hiddenHeaders := append(bytes.Repeat(extensionBytes, 8193), archive.Bytes()...)
	for _, test := range []struct {
		name string
		raw  []byte
		code e.Code
	}{
		{"valid modes", valid, ""},
		{"bad gzip checksum", badTrailer, e.CodeVerifyFailed},
		{"truncated gzip trailer", valid[:len(valid)-4], e.CodeVerifyFailed},
		{"trailing tar payload", gzipBytes(append(append([]byte(nil), archive.Bytes()...), []byte("hidden")...)), e.CodeVerifyFailed},
		{"additional gzip member", append(append([]byte(nil), valid...), gzipBytes(nil)...), e.CodeVerifyFailed},
		{"hidden extension expansion", gzipBytes(hiddenHeaders), e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			held, err := heldfile.FromBytesContext(t.Context(), test.raw, heldfile.Options{Label: "test executable layer", MaxBytes: 16 << 20})
			if err != nil {
				t.Fatal(err)
			}
			defer held.Close()
			parent := t.TempDir()
			out := filepath.Join(parent, "tools")
			bundle := BinaryBundle{Entries: []BinaryEntry{{Name: "cli", Size: int64(len(payload)), Mode: 0o751, SHA256: digestBytes([]byte(payload))}}}
			err = extractPulledExecutable(t.Context(), held, out, bundle)
			if test.code != "" {
				assertRegistryCode(t, err, test.code)
				assertEmptyPullDirectory(t, parent)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(filepath.Join(out, "cli"))
			if err != nil || info.Mode().Perm() != 0o751 {
				t.Fatalf("mode not preserved: %v, %v", info, err)
			}
		})
	}
}

type executablePullFixture struct {
	root, layer, diskLayer, diskConfig string
	layerBytes                         []byte
}

func pullExecutableFixture(t *testing.T) executablePullFixture {
	t.Helper()
	request := releasePublicationRequestFixture(t, "linux", "amd64")
	disk, _ := diskPublicationRequest(t, "amd64")
	request.Release.PlanDigest = disk.Release.PlanDigest
	request.Objects = append(request.Objects, disk.Objects...)
	var outputs []Output
	for _, object := range request.Objects {
		if object.Output == nil {
			continue
		}
		descriptor, err := PayloadDescriptor(t.Context(), object.Path)
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, Output{Name: object.Name, Kind: object.Output.Format, Digest: descriptor.Digest,
			BuildDigest: object.Output.BuildDigest, Components: object.Output.Components, Disk: object.Output.Disk})
	}
	content, err := ContentDigest(outputs)
	if err != nil {
		t.Fatal(err)
	}
	request.Release.ContentSubject = content
	fixture := executablePullFixture{root: filepath.Join(t.TempDir(), "release")}
	if _, err := BuildPublicationRoot(t.Context(), fixture.root, request); err != nil {
		t.Fatal(err)
	}
	inspection, err := Inspect(t.Context(), fixture.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range inspection.Root.Objects {
		if object.Output == nil {
			continue
		}
		manifest, err := flatManifest(t.Context(), fixture.root, object)
		if err != nil {
			t.Fatal(err)
		}
		if object.Name == "tools" {
			fixture.layer = manifest.Layers[0].Digest
			fixture.layerBytes, err = readBlob(t.Context(), fixture.root, manifest.Layers[0], maxDeliveryFileBytes)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			fixture.diskLayer, fixture.diskConfig = manifest.Layers[0].Digest, manifest.Config.Digest
		}
	}
	return fixture
}

func assertEmptyPullDirectory(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil || len(entries) != 0 {
		t.Fatalf("private or partial output remains at %s: %v, %v", path, entries, err)
	}
}
