// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func privateLocatorZIP(t *testing.T, name string, request cistore.Request, edit func(map[string]any)) []byte {
	t.Helper()
	value := map[string]any{"schemaVersion": "nvkr.dev/ci-carrier/v1", "request": request,
		"reference":     "ghcr.io/example/nvkr/ci-artifacts@sha256:" + strings.Repeat("a", 64),
		"archiveDigest": "sha256:" + strings.Repeat("b", 64), "archiveSize": 10240,
		"paths": []string{"handoff.json"}, "stripPrefix": ""}
	if edit != nil {
		edit(value)
	}
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return handoffZIP(t, []zip.FileHeader{{Name: name + ".ci-locator.json", Method: zip.Deflate}}, raw)
}

func setPrivateHandoffArchive(fixture *handoffDownloadFixture, archive []byte) {
	fixture.archive = archive
	fixture.list.Artifacts[0].Digest = handoffArchiveDigest(archive)
	fixture.list.Artifacts[0].Size = json.Number(strconv.Itoa(len(archive)))
}

func TestPrivateStorageCredentialsAreExplicitWithLegacyFallback(t *testing.T) {
	for _, tc := range []struct {
		name, api, storage, want string
	}{
		{"separate", "api-read", "storage-read", "storage-read"},
		{"fallback", "api-read", "", "api-read"},
		{"empty", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := NewGitHubWithCIStorage(tc.api, tc.storage)
			if g.token != tc.api || g.storageToken != tc.want || g.downloadPrivate == nil {
				t.Fatal("provider and private storage credentials were conflated or implicit")
			}
		})
	}
	if g := NewGitHub("legacy-read"); g.token != "legacy-read" || g.storageToken != "legacy-read" {
		t.Fatal("legacy constructor lost its explicit read-token fallback")
	}
}

func TestPrivateQualificationHandoffKeepsProviderAndStorageBindings(t *testing.T) {
	fixture := newHandoffDownloadFixture(t)
	request := cistore.Request{Repository: "example/nvkr", RunID: "1234", Attempt: "2", Name: fixture.list.Artifacts[0].Name}
	setPrivateHandoffArchive(fixture, privateLocatorZIP(t, request.Name, request, nil))
	g := fixture.github(t)
	g.storageToken = "private-read-secret"
	called, staged := false, ""
	g.downloadPrivate = func(ctx context.Context, got cistore.Request, locator cistore.Locator, destination string, options cistore.Options) error {
		called, staged = true, destination
		if ctx.Err() != nil || got != request || locator.Request != request || options.Token != "private-read-secret" {
			t.Fatal("private download lost its exact origin or separate explicit credential")
		}
		if _, err := os.Stat(destination); !os.IsNotExist(err) {
			t.Fatal("private payload destination must be fresh")
		}
		if err := os.Mkdir(destination, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(destination, "handoff.json"), []byte(`{"private":"handoff"}`), 0o600)
	}
	raw, err := g.DownloadQualificationHandoff(t.Context(), fixture.source)
	if err != nil || !called || !bytes.Equal(raw, []byte(`{"private":"handoff"}`)) {
		t.Fatalf("private handoff = %q, %v (called=%t)", raw, err, called)
	}
	if _, err := os.Stat(filepath.Dir(staged)); !os.IsNotExist(err) {
		t.Fatal("private download staging survived completion")
	}
	if err := fixture.source.Require(); err != nil {
		t.Fatal("private download closed its borrowed source")
	}
}

func TestPrivateQualificationHandoffRejectsBeforeStorageAndNeverFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*handoffDownloadFixture, *GitHub, *cistore.Request, *map[string]any)
		code e.Code
	}{
		{"wrong repository", func(_ *handoffDownloadFixture, _ *GitHub, r *cistore.Request, _ *map[string]any) {
			r.Repository = "other/nvkr"
		}, e.CodeVerifyFailed},
		{"wrong run", func(_ *handoffDownloadFixture, _ *GitHub, r *cistore.Request, _ *map[string]any) { r.RunID = "1235" }, e.CodeVerifyFailed},
		{"wrong attempt", func(_ *handoffDownloadFixture, _ *GitHub, r *cistore.Request, _ *map[string]any) { r.Attempt = "1" }, e.CodeVerifyFailed},
		{"wrong name", func(_ *handoffDownloadFixture, _ *GitHub, r *cistore.Request, _ *map[string]any) {
			r.Name = "other-artifact"
		}, e.CodeVerifyFailed},
		{"payload bound", func(_ *handoffDownloadFixture, _ *GitHub, _ *cistore.Request, v *map[string]any) {
			(*v)["archiveSize"] = maxQualificationArchiveBytes + 1
		}, e.CodeVerifyFailed},
		{"no storage token", func(_ *handoffDownloadFixture, g *GitHub, _ *cistore.Request, _ *map[string]any) { g.storageToken = "" }, e.CodeEngineCapability},
		{"provider size", func(f *handoffDownloadFixture, _ *GitHub, _ *cistore.Request, _ *map[string]any) {
			f.list.Artifacts[0].Size = "1"
		}, e.CodeVerifyFailed},
		{"provider digest", func(f *handoffDownloadFixture, _ *GitHub, _ *cistore.Request, _ *map[string]any) {
			f.list.Artifacts[0].Digest = "sha256:" + strings.Repeat("c", 64)
		}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			g := fixture.github(t)
			request := cistore.Request{Repository: "example/nvkr", RunID: "1234", Attempt: "2", Name: fixture.list.Artifacts[0].Name}
			values := map[string]any{}
			tc.edit(fixture, g, &request, &values)
			archive := privateLocatorZIP(t, fixture.list.Artifacts[0].Name, request, func(v map[string]any) {
				for key, value := range values {
					v[key] = value
				}
			})
			oldSize, oldDigest := fixture.list.Artifacts[0].Size, fixture.list.Artifacts[0].Digest
			setPrivateHandoffArchive(fixture, archive)
			if tc.name == "provider size" {
				fixture.list.Artifacts[0].Size = oldSize
			}
			if tc.name == "provider digest" {
				fixture.list.Artifacts[0].Digest = oldDigest
			}
			g.downloadPrivate = func(context.Context, cistore.Request, cistore.Locator, string, cistore.Options) error {
				t.Fatal("inadmissible provider/locator reached private storage")
				return nil
			}
			raw, err := g.DownloadQualificationHandoff(t.Context(), fixture.source)
			assertCode(t, err, tc.code)
			if raw != nil {
				t.Fatal("rejected private locator returned fallback payload")
			}
		})
	}
}

func TestPrivateQualificationHandoffOwnsFailureAndCancellationCleanup(t *testing.T) {
	for _, failure := range []string{"storage", "extra member", "symlink", "revoked", "canceled"} {
		t.Run(failure, func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			request := cistore.Request{Repository: "example/nvkr", RunID: "1234", Attempt: "2", Name: fixture.list.Artifacts[0].Name}
			setPrivateHandoffArchive(fixture, privateLocatorZIP(t, request.Name, request, nil))
			g := fixture.github(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			staged := ""
			g.downloadPrivate = func(_ context.Context, _ cistore.Request, _ cistore.Locator, destination string, _ cistore.Options) error {
				staged = destination
				if err := os.Mkdir(destination, 0o700); err != nil {
					return err
				}
				if failure == "storage" {
					return e.New(e.CodeFetchFailed, "private storage rejected")
				}
				if failure == "symlink" {
					return os.Symlink("outside", filepath.Join(destination, "handoff.json"))
				}
				if err := os.WriteFile(filepath.Join(destination, "handoff.json"), []byte("private"), 0o600); err != nil {
					return err
				}
				switch failure {
				case "extra member":
					return os.WriteFile(filepath.Join(destination, "extra"), []byte("unrelated"), 0o600)
				case "revoked":
					return fixture.source.Close()
				case "canceled":
					cancel()
				}
				return nil
			}
			raw, err := g.DownloadQualificationHandoff(ctx, fixture.source)
			code := e.CodeVerifyFailed
			switch failure {
			case "storage":
				code = e.CodeFetchFailed
			case "revoked":
				code = e.CodeProvenanceMissing
			case "canceled":
				code = e.CodeTimeout
			}
			assertCode(t, err, code)
			if raw != nil || staged == "" {
				t.Fatal("private failure returned payload or did not exercise restoration")
			}
			if _, err := os.Stat(filepath.Dir(staged)); !os.IsNotExist(err) {
				t.Fatal("private staging survived a failed or canceled download")
			}
		})
	}
}

func TestPrivateNativeSmokeRetainsExactPlatformAndCompleteEvidence(t *testing.T) {
	for _, scenario := range []string{"complete", "missing", "empty", "storage failure"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newNativeSmokeFixture(t)
			for _, arch := range []string{"amd64", "arm64"} {
				name := "nvkr-smoke-2345-3-" + arch
				request := cistore.Request{Repository: "example/nvkr", RunID: "2345", Attempt: "3", Name: name}
				archive := privateLocatorZIP(t, name, request, func(value map[string]any) {
					value["paths"] = []string{"worker.bundle.json", "worker.json", "worker.pem", "worker.sig"}
				})
				listing := fixture.artifacts[name]
				listing.Artifacts[0].Size = json.Number(strconv.Itoa(len(archive)))
				listing.Artifacts[0].Digest = handoffArchiveDigest(archive)
				fixture.artifacts[name] = listing
				fixture.archives[string(listing.Artifacts[0].ID)] = archive
			}
			g := fixture.github(t)
			var staged []string
			g.downloadPrivate = func(_ context.Context, request cistore.Request, locator cistore.Locator, destination string, options cistore.Options) error {
				staged = append(staged, destination)
				if request.Repository != "example/nvkr" || request.RunID != "2345" || request.Attempt != "3" || request != locator.Request || options.Token != "read-secret" {
					t.Fatal("worker locator escaped its exact qualifier attempt or explicit credential")
				}
				if scenario == "storage failure" {
					return e.New(e.CodeFetchFailed, "private worker transport failed")
				}
				if err := os.Mkdir(destination, 0o700); err != nil {
					return err
				}
				for _, header := range nativeSmokeHeaders() {
					if scenario == "missing" && header.Name == "worker.pem" {
						continue
					}
					raw := []byte(request.Name)
					if scenario == "empty" && header.Name == "worker.json" {
						raw = nil
					}
					if err := os.WriteFile(filepath.Join(destination, header.Name), raw, 0o600); err != nil {
						return err
					}
				}
				return nil
			}
			artifacts, err := g.DownloadNativeSmoke(t.Context(), fixture.invocation, []string{"linux/arm64", "linux/amd64"})
			if scenario == "complete" {
				if err != nil || len(artifacts) != 2 || artifacts[0].Platform != "linux/amd64" || artifacts[1].Platform != "linux/arm64" {
					t.Fatalf("private smoke platform closure = %v, %v", artifacts, err)
				}
				for _, artifact := range artifacts {
					want := []byte("nvkr-smoke-2345-3-" + strings.TrimPrefix(artifact.Platform, "linux/"))
					for _, raw := range [][]byte{artifact.Document, artifact.Signature, artifact.Certificate, artifact.Bundle} {
						if !bytes.Equal(raw, want) {
							t.Fatal("private worker evidence selected another native platform")
						}
					}
				}
			} else {
				code := e.CodeVerifyFailed
				if scenario == "storage failure" {
					code = e.CodeFetchFailed
				}
				assertCode(t, err, code)
				if artifacts != nil {
					t.Fatal("failed private worker download returned partial evidence")
				}
			}
			for _, destination := range staged {
				if _, err := os.Stat(filepath.Dir(destination)); !os.IsNotExist(err) {
					t.Fatal("private worker staging leaked after completion")
				}
			}
		})
	}
}
