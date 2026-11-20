// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestStagedDiskMatchesCurrentReleaseRoot(t *testing.T) {
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			request, _ := diskPublicationRequest(t, arch)
			parent := t.TempDir()
			source := filepath.Join(parent, "prepared")
			prepared, err := BuildPublicationRoot(t.Context(), source, request)
			if err != nil {
				t.Fatal(err)
			}
			controls, attested := filepath.Join(parent, "controls"), filepath.Join(parent, "attested")
			if _, err := ExportMetadataWithExpectedRoot(t.Context(), source, controls, prepared.RootDigest); err != nil {
				t.Fatal(err)
			}
			signed, err := AttestPreparedMetadata(t.Context(), controls, attested, prepared.RootDigest, fixtureMetadataAttester)
			if err != nil {
				t.Fatal(err)
			}
			payload := request.Objects[0].Output.Disk.PayloadDigest
			var completing atomic.Bool
			var payloadRequests atomic.Int64
			var payloadWrites atomic.Int64
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if completing.Load() && (strings.Contains(r.URL.Path, payload) || r.URL.Query().Get("mount") == payload || r.URL.Query().Get("digest") == payload) {
					if r.Method == http.MethodGet {
						payloadRequests.Add(1)
					} else if r.Method != http.MethodHead {
						payloadWrites.Add(1)
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			repository := strings.TrimPrefix(server.URL, "http://") + "/fixture/releases"
			options := RegistryOptions{PlainHTTP: true, Anonymous: true}
			ref, err := StagePreparedDisk(t.Context(), source, repository, prepared.RootDigest, options)
			if err != nil {
				t.Fatal(err)
			}
			verify := func(_ context.Context, subject string, bundle []byte) error {
				_, err := sbom.AttestedDocument(bundle, subject)
				return err
			}
			completing.Store(true)
			readback := filepath.Join(parent, "readback")
			stagedRef, err := CompleteStagedDisk(t.Context(), ref, readback, attested, signed.RootDigest, options, verify)
			if err != nil {
				t.Fatal(err)
			}
			completing.Store(false)
			if payloadRequests.Load() != 1 || payloadWrites.Load() != 0 {
				t.Fatalf("completion must read payload once without writes: reads=%d writes=%d", payloadRequests.Load(), payloadWrites.Load())
			}
			if _, err := os.Stat(fixtureBlobPath(readback, payload)); !os.IsNotExist(err) {
				t.Fatalf("readback retained payload: %v", err)
			}
			legacy := filepath.Join(parent, "legacy")
			completed, err := CompleteAttestedPreparation(t.Context(), source, attested, legacy, prepared.RootDigest, signed.RootDigest, verify)
			if err != nil {
				t.Fatal(err)
			}
			legacyRef, err := Push(t.Context(), legacy, repository, options)
			if err != nil {
				t.Fatal(err)
			}
			if stagedRef != legacyRef || completed.RootDigest != signed.RootDigest {
				t.Fatalf("release roots differ: staged=%s current=%s", stagedRef, legacyRef)
			}
			oldBytes, err := os.ReadFile(filepath.Join(legacy, "index.json"))
			if err != nil {
				t.Fatal(err)
			}
			newBytes, err := os.ReadFile(filepath.Join(attested, "index.json"))
			if err != nil || !bytes.Equal(oldBytes, newBytes) {
				t.Fatalf("release root bytes differ: %v", err)
			}
			t.Logf("identity platform=linux/%s prepared=%s payload=%s staged-release=%s legacy-release=%s root-bytes=%d", arch, prepared.RootDigest, payload, signed.RootDigest, completed.RootDigest, len(newBytes))
			// Independent full readback still owns acceptance after completion.
			if _, err := Pull(t.Context(), stagedRef, filepath.Join(parent, "verified"), options); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStageDiskRejectsBeforeNetwork(t *testing.T) {
	for _, scenario := range []string{"tag", "digest", "changed root", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			source, _, _ := registryDiskFixture(t, "amd64")
			inspection, err := Inspect(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			repository, root := "invalid.example.test/fixture/releases", inspection.RootDigest
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			code := e.CodeUsage
			switch scenario {
			case "tag":
				repository += ":latest"
			case "digest":
				repository += "@" + root
			case "changed root":
				root, code = digestBytes([]byte("other")), e.CodeVerifyFailed
			case "canceled":
				cancel()
				code = e.CodeTimeout
			}
			_, err = StagePreparedDisk(ctx, source, repository, root, RegistryOptions{Anonymous: true})
			assertRegistryCode(t, err, code)
		})
	}
}

func TestCompleteStagedDiskRejectsBeforeWrites(t *testing.T) {
	request, _ := diskPublicationRequest(t, "amd64")
	parent := t.TempDir()
	source, controls, attested := filepath.Join(parent, "prepared"), filepath.Join(parent, "controls"), filepath.Join(parent, "attested")
	prepared, err := BuildPublicationRoot(t.Context(), source, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExportMetadataWithExpectedRoot(t.Context(), source, controls, prepared.RootDigest); err != nil {
		t.Fatal(err)
	}
	signed, err := AttestPreparedMetadata(t.Context(), controls, attested, prepared.RootDigest, fixtureMetadataAttester)
	if err != nil {
		t.Fatal(err)
	}
	var writes atomic.Int64
	var fault atomic.Value
	fault.Store("")
	payload := request.Objects[0].Output.Disk.PayloadDigest
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.Contains(r.URL.Path, payload) {
			switch fault.Load().(string) {
			case "missing payload":
				http.NotFound(w, r)
				return
			case "corrupt payload":
				_, _ = io.WriteString(w, "corrupted bytes")
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writes.Add(1)
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	repository := strings.TrimPrefix(server.URL, "http://") + "/fixture/releases"
	ref, err := StagePreparedDisk(t.Context(), source, repository, prepared.RootDigest, options)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"changed root", "missing verifier", "rejected signer", "canceled", "changed output", "missing payload", "corrupt payload"} {
		t.Run(scenario, func(t *testing.T) {
			writes.Store(0)
			fault.Store(scenario)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			root, input := signed.RootDigest, attested
			verify := func(context.Context, string, []byte) error {
				return e.New(e.CodeSignatureInvalid, "synthetic signer rejection")
			}
			code := e.CodeVerifyFailed
			switch scenario {
			case "missing payload":
				code = e.CodeUnavailable
			case "changed root":
				root = digestBytes([]byte("other"))
			case "missing verifier":
				verify, code = nil, e.CodeEngineCapability
			case "rejected signer":
				code = e.CodeSignatureInvalid
			case "canceled":
				cancel()
				code = e.CodeTimeout
			case "changed output":
				inspection, err := InspectMetadata(ctx, attested)
				if err != nil {
					t.Fatal(err)
				}
				inspection.Root.Release.InvocationID = "https://github.com/example/other/actions/runs/1/attempts/1"
				sources := map[string]string{}
				for _, object := range inspection.Root.Objects {
					sources[object.Name] = attested
				}
				input = filepath.Join(t.TempDir(), "changed")
				changed, err := BuildMetadataRoot(ctx, input, inspection.Root, sources)
				if err != nil {
					t.Fatal(err)
				}
				root = changed.RootDigest
			}
			_, err := CompleteStagedDisk(ctx, ref, filepath.Join(t.TempDir(), "readback"), input, root, options, verify)
			assertRegistryCode(t, err, code)
			if writes.Load() != 0 {
				t.Fatalf("rejected completion wrote %d requests", writes.Load())
			}
		})
	}
}
