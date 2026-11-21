// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

type stagedResetBody struct{ *releasePullResetBody }

func (body stagedResetBody) Read(buffer []byte) (int, error) {
	n, err := body.releasePullResetBody.Read(buffer)
	// Deliver a persistent reset with the final prefix bytes, before the
	// Content-Length-limited registry reader can synthesize EOF itself.
	if n > 0 && body.remaining == 0 {
		_, err = body.releasePullResetBody.Read(nil)
	}
	return n, err
}

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
			case "interrupted payload":
				w.Header().Set("Content-Length", strconv.FormatInt(request.Objects[0].Output.Disk.Size, 10))
				_, _ = io.WriteString(w, "partial")
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
	for _, scenario := range []string{"changed root", "missing verifier", "rejected signer", "rejected second bundle", "canceled", "changed output", "missing payload", "corrupt payload", "interrupted payload", "peer reset", "peer reset after size", "peer reset and close failure"} {
		t.Run(scenario, func(t *testing.T) {
			writes.Store(0)
			fault.Store(scenario)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			root, input := signed.RootDigest, attested
			verifications := 0
			var resets atomic.Int32
			var resetTransport *releasePullBlobTransport
			verify := func(context.Context, string, []byte) error {
				verifications++
				if scenario == "rejected second bundle" && verifications == 1 {
					return nil
				}
				return e.New(e.CodeSignatureInvalid, "synthetic signer rejection")
			}
			code := e.CodeVerifyFailed
			switch scenario {
			case "missing payload", "interrupted payload", "peer reset":
				code = e.CodeUnavailable
			case "changed root":
				root = digestBytes([]byte("other"))
			case "missing verifier":
				verify, code = nil, e.CodeEngineCapability
			case "rejected signer", "rejected second bundle":
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
			if strings.HasPrefix(scenario, "peer reset") {
				original := remote.DefaultTransport
				resetTransport = &releasePullBlobTransport{RoundTripper: original, digest: payload, body: func(_ int, body io.ReadCloser) io.ReadCloser {
					remaining := int(request.Objects[0].Output.Disk.Size) / 2
					if scenario == "peer reset after size" {
						remaining = int(request.Objects[0].Output.Disk.Size)
					}
					reset := &releasePullResetBody{ReadCloser: body, remaining: remaining, onReset: func() { resets.Add(1) }}
					if scenario == "peer reset and close failure" {
						return releasePullCloseFailureBody{reset}
					}
					return stagedResetBody{reset}
				}}
				remote.DefaultTransport = resetTransport
				t.Cleanup(func() { remote.DefaultTransport = original })
			}
			_, err := CompleteStagedDisk(ctx, ref, filepath.Join(t.TempDir(), "readback"), input, root, options, verify)
			assertRegistryCode(t, err, code)
			if resetTransport != nil && (resets.Load() == 0 || resetTransport.reads.Load() != 1 || verifications != 0) {
				t.Fatalf("reset was not observed before attestation: resets=%d reads=%d verifications=%d", resets.Load(), resetTransport.reads.Load(), verifications)
			}
			if scenario == "rejected second bundle" && verifications != 2 {
				t.Fatalf("second-bundle refusal verified %d bundles", verifications)
			}
			if writes.Load() != 0 {
				t.Fatalf("rejected completion wrote %d requests", writes.Load())
			}
		})
	}
}

func TestCompleteStagedDiskMetadataFailureKeepsUncertainty(t *testing.T) {
	request, manifest := diskPublicationRequest(t, "amd64")
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
	var completing atomic.Bool
	var writes, failures atomic.Int32
	backend := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if completing.Load() {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				writes.Add(1)
			}
			if writes.Load() > 0 && r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/manifests/"+manifest) {
				failures.Add(1)
				// Persistently truncate metadata readback after evidence uploads.
				w.Header().Set("Content-Length", "1024")
				_, _ = io.WriteString(w, "{}")
				return
			}
		}
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	repository := strings.TrimPrefix(server.URL, "http://") + "/fixture/releases"
	ref, err := StagePreparedDisk(t.Context(), source, repository, prepared.RootDigest, options)
	if err != nil {
		t.Fatal(err)
	}
	completing.Store(true)
	published, err := CompleteStagedDisk(t.Context(), ref, filepath.Join(parent, "readback"), attested, signed.RootDigest, options,
		func(_ context.Context, subject string, bundle []byte) error {
			_, err := sbom.AttestedDocument(bundle, subject)
			return err
		})
	assertRegistryCode(t, err, e.CodeUnavailable)
	var uncertain registryPublicationUncertain
	if published != "" || writes.Load() == 0 || failures.Load() == 0 || !errors.As(err, &uncertain) ||
		strings.Contains(err.Error(), "no upload was attempted") || !strings.Contains(SafeRegistryPublishError(t.Context(), err).Error(), "partial uploads") {
		t.Fatalf("post-write failure lost uncertainty: ref=%q writes=%d failed reads=%d err=%v", published, writes.Load(), failures.Load(), err)
	}
}
