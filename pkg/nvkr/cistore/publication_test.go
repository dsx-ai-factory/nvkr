// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

var publisherOptions = Options{Token: "publisher-secret", Username: "publisher"}

func publicationBackend(t *testing.T, policy func(string, int) (int, any)) (*storageFixture, *atomic.Int64) {
	t.Helper()
	f := newStorageFixture(t, admittedPolicy)
	calls := new(atomic.Int64)
	counts := map[string]int{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer publisher-secret" {
			t.Errorf("publication API did not use the explicit publisher token")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		status := http.StatusOK
		var value any
		if r.URL.Path == "/repos/Example/Source" {
			value = map[string]any{"id": 1, "full_name": "Example/Source", "owner": map[string]any{"login": "Example", "type": "Organization"}}
		} else if packageName, ok := strings.CutPrefix(r.URL.Path, "/orgs/Example/packages/container/"); ok {
			counts[packageName]++
			status, value = policy(packageName, counts[packageName])
		} else {
			t.Errorf("unexpected publication API path: %s", r.URL.Path)
			status = http.StatusNotFound
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if value != nil {
			if err := json.NewEncoder(w).Encode(value); err != nil {
				t.Error(err)
			}
		}
	}))
	t.Cleanup(api.Close)
	f.b.api, f.b.client = api.URL, api.Client()
	return f, calls
}

func publicationPackage(packageName string) map[string]any {
	p := privatePackage()
	p["name"] = packageName
	return p
}

func TestPrivatePublicationAdmitsExactOutputAndReleasePackages(t *testing.T) {
	f, _ := publicationBackend(t, func(packageName string, _ int) (int, any) {
		p := publicationPackage(packageName)
		if packageName == "source/releases" {
			p["visibility"] = "internal"
		}
		return http.StatusOK, p
	})
	if err := f.b.admitPrivatePublication(t.Context(), "Example/Source", []string{"ghcr.io/example/source/releases", "ghcr.io/example/source/nightly/nvkectl"}, publisherOptions); err != nil {
		t.Fatal(err)
	}
	if f.transport.calls.Load() != 0 {
		t.Fatal("existing private package admission wrote registry data")
	}
}

func TestPrivatePublicationValidatesEntireRepositorySetBeforeIO(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target string
	}{
		{"public registry", "docker.io/example/source/output"},
		{"foreign owner", "ghcr.io/other/source/output"},
		{"tagged", "ghcr.io/example/source/output:latest"},
		{"digested", "ghcr.io/example/source/output@sha256:" + strings.Repeat("a", 64)},
		{"dot segment", "ghcr.io/example/source/../output"},
		{"double slash", "ghcr.io/example//output"},
		{"uppercase", "ghcr.io/Example/source/output"},
		{"duplicate", "ghcr.io/example/a-missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, calls := publicationBackend(t, func(string, int) (int, any) { return http.StatusNotFound, nil })
			err := f.b.admitPrivatePublication(t.Context(), "Example/Source", []string{"ghcr.io/example/a-missing", tc.target}, publisherOptions)
			assertCode(t, err, e.CodeUsage)
			if calls.Load() != 0 || f.transport.calls.Load() != 0 {
				t.Fatal("invalid target set performed API or registry IO")
			}
		})
	}
}

func TestPrivatePublicationRefusesPublicUnknownAndLinkedPackagesBeforeSeed(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
	}{
		{"public", func(p map[string]any) { p["visibility"] = "public" }},
		{"unknown visibility", func(p map[string]any) { delete(p, "visibility") }},
		{"public linked", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 3, "full_name": "Example/Source", "private": false, "visibility": "public"}
		}},
		{"private linked", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 3, "full_name": "Example/Private", "private": true}
		}},
		{"unknown linkage", func(p map[string]any) { p["repository"] = map[string]any{} }},
		{"substituted package", func(p map[string]any) { p["name"] = "other/releases" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := publicationBackend(t, func(packageName string, _ int) (int, any) {
				if packageName == "a-missing" {
					return http.StatusNotFound, nil
				}
				p := publicationPackage(packageName)
				tc.edit(p)
				return http.StatusOK, p
			})
			err := f.b.admitPrivatePublication(t.Context(), "Example/Source", []string{"ghcr.io/example/a-missing", "ghcr.io/example/source/releases"}, publisherOptions)
			assertCode(t, err, e.CodeTrustPolicyMissing)
			if f.transport.calls.Load() != 0 {
				t.Fatal("one inadmissible package did not prevent all metadata seeds")
			}
		})
	}
}

func TestPrivatePublicationSeedsOnlyMetadataAndRechecksPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		visibility string
		linked     bool
		wantError  bool
	}{
		{"private seed", "private", false, false},
		{"public seed", "public", false, true},
		{"private linked seed", "private", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := publicationBackend(t, func(packageName string, count int) (int, any) {
				if count <= 2 {
					return http.StatusNotFound, nil
				}
				p := publicationPackage(packageName)
				p["visibility"] = tc.visibility
				if tc.linked {
					p["repository"] = map[string]any{"id": 3, "full_name": "Example/Private", "private": true}
				}
				return http.StatusOK, p
			})
			err := f.b.admitPrivatePublication(t.Context(), "Example/Source", []string{"ghcr.io/example/source/releases"}, publisherOptions)
			if tc.wantError {
				assertCode(t, err, e.CodeTrustPolicyMissing)
			} else if err != nil {
				t.Fatal(err)
			}
			ref, err := name.NewTag("ghcr.io/example/source/releases:privacy-probe-v1", name.StrictValidation)
			if err != nil {
				t.Fatal(err)
			}
			image, err := remote.Image(ref, f.b.registryOptions(t.Context(), publisherOptions)...)
			if err != nil {
				t.Fatal(err)
			}
			layers, err := image.Layers()
			if err != nil || len(layers) != 0 {
				t.Fatalf("package seed contains payload: %v", err)
			}
			config, err := image.ConfigFile()
			if err != nil || config.Config.Labels[BindingLabel] != "" {
				t.Fatalf("package seed contains source metadata: %v", err)
			}
		})
	}
}

func TestPrivatePublicationRechecksWholeSetAndContext(t *testing.T) {
	f, _ := publicationBackend(t, func(packageName string, count int) (int, any) {
		p := publicationPackage(packageName)
		if count > 1 {
			p["visibility"] = "public"
		}
		return http.StatusOK, p
	})
	err := f.b.admitPrivatePublication(t.Context(), "Example/Source", []string{"ghcr.io/example/source/releases"}, publisherOptions)
	assertCode(t, err, e.CodeTrustPolicyMissing)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertCode(t, f.b.admitPrivatePublication(ctx, "Example/Source", []string{"ghcr.io/example/source/releases"}, publisherOptions), e.CodeTimeout)
	assertCode(t, f.b.admitPrivatePublication(t.Context(), "../invalid", []string{"ghcr.io/example/source/releases"}, publisherOptions), e.CodeUsage)
	assertCode(t, f.b.admitPrivatePublication(t.Context(), "Example/Source", []string{"ghcr.io/example/source/releases"}, Options{}), e.CodeTrustPolicyMissing)
}

func TestPrivatePublicationRefusesPrivateLinkageAtFinalWholeSetCheck(t *testing.T) {
	f, calls := publicationBackend(t, func(packageName string, count int) (int, any) {
		p := publicationPackage(packageName)
		if packageName == "source/z-output" && count > 1 {
			p["repository"] = map[string]any{"id": 3, "full_name": "Example/Private", "private": true}
		}
		return http.StatusOK, p
	})
	err := f.b.admitPrivatePublication(t.Context(), "Example/Source", []string{"ghcr.io/example/source/releases", "ghcr.io/example/source/z-output"}, publisherOptions)
	assertCode(t, err, e.CodeTrustPolicyMissing)
	if calls.Load() != 5 || f.transport.calls.Load() != 0 {
		t.Fatal("final whole-set linkage refusal must perform both rechecks without registry writes")
	}
}
