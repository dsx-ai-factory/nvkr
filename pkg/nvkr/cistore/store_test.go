// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

var testRequest = Request{Repository: "Example/Source", RunID: "123", Attempt: "2", Name: "runtime-inputs"}
var testOptions = Options{Token: "test-secret"}

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
	calls  atomic.Int64
}

func (r *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.calls.Add(1)
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = r.target.Scheme, r.target.Host
	clone.URL = &u
	clone.Host = r.target.Host
	return r.base.RoundTrip(clone)
}

type storageFixture struct {
	b         *backend
	transport *rewriteTransport
	policy    func(int) (int, any)
	gets      int
}

func newStorageFixture(t *testing.T, policy func(int) (int, any)) *storageFixture {
	t.Helper()
	server := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	route := &rewriteTransport{target: target, base: server.Client().Transport}
	f := &storageFixture{transport: route, policy: policy}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-secret" {
			t.Errorf("missing explicit API credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var value any
		status := http.StatusOK
		switch r.URL.EscapedPath() {
		case "/repos/Example/Source":
			value = map[string]any{"id": 1, "full_name": "Example/Source", "owner": map[string]any{"login": "Example", "type": "Organization"}}
		case "/orgs/Example/packages/container/source%2Fci-artifacts":
			f.gets++
			status, value = f.policy(f.gets)
		default:
			t.Errorf("unexpected API path %s", r.URL.EscapedPath())
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
	f.b = &backend{api: api.URL, client: api.Client(), transport: route}
	return f
}

func privatePackage() map[string]any {
	// GitHub's documented unlinked package response omits repository.
	return map[string]any{"name": "source/ci-artifacts", "package_type": "container", "visibility": "private", "owner": map[string]any{"login": "Example"}}
}

func admittedPolicy(int) (int, any) { return http.StatusOK, privatePackage() }

func assertCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("got %v, want coded %s", err, code)
	}
}

func writePayload(t *testing.T, root, name, value string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, name), []byte(value), mode); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateCarrierRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name   string
		paths  []string
		prefix string
		files  []string
	}{
		{"single file", []string{"nested/bin/tool"}, "nested/bin", []string{"tool"}},
		{"directory", []string{"nested/bin"}, "nested/bin", []string{"tool", "data"}},
		{"multiple roots", []string{"nested/bin/tool", "nested/meta/info"}, "nested", []string{"bin/tool", "meta/info"}},
		{"workspace roots", []string{"nested/bin/tool", "other/info"}, "", []string{"nested/bin/tool", "other/info"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStorageFixture(t, admittedPolicy)
			root := t.TempDir()
			for _, p := range []string{"nested/bin/tool", "nested/bin/data", "nested/meta/info", "other/info"} {
				writePayload(t, root, p, p, 0o751)
			}
			locator, err := f.b.upload(t.Context(), testRequest, root, tc.paths, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			if locator.StripPrefix != tc.prefix || !strings.HasPrefix(locator.Reference, "ghcr.io/example/source/ci-artifacts@sha256:") {
				t.Fatalf("incorrect LCA or package: %+v", locator)
			}
			raw, err := canonical.Marshal(locator)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := DecodeLocator(raw)
			if err != nil {
				t.Fatal(err)
			}
			destination := t.TempDir()
			if err := f.b.download(t.Context(), testRequest, decoded, destination, testOptions); err != nil {
				t.Fatal(err)
			}
			for _, p := range tc.files {
				info, err := os.Stat(filepath.Join(destination, p))
				if err != nil || info.Mode().Perm() != 0o751 {
					t.Fatalf("missing file/mode %s: %v", p, err)
				}
			}
			if entries, err := os.ReadDir(destination); err != nil || len(entries) == 0 {
				t.Fatalf("empty restore: %v", err)
			}
		})
	}
}

func TestPrivateCarrierPreservesContainedSymlinksAndRejectsOverwrite(t *testing.T) {
	f := newStorageFixture(t, admittedPolicy)
	root := t.TempDir()
	writePayload(t, root, "payload/bin/tool", "executable", 0o755)
	if err := os.Symlink("bin/tool", filepath.Join(root, "payload/link")); err != nil {
		t.Fatal(err)
	}
	l, err := f.b.upload(t.Context(), testRequest, root, []string{"payload"}, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := f.b.download(t.Context(), testRequest, l, destination, testOptions); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(filepath.Join(destination, "link")); err != nil || got != "bin/tool" {
		t.Fatalf("safe symlink not preserved: %q %v", got, err)
	}
	assertCode(t, f.b.download(t.Context(), testRequest, l, destination, testOptions), e.CodeImmutableViolation)
	if value, err := os.ReadFile(filepath.Join(destination, "bin/tool")); err != nil || string(value) != "executable" {
		t.Fatalf("existing output changed: %q %v", value, err)
	}
}

func TestPrivateCarrierDeniesPrivacyBeforePayloadAccess(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		status int
	}{
		{"public package", func(p map[string]any) { p["visibility"] = "public" }, 200},
		{"unknown visibility", func(p map[string]any) { delete(p, "visibility") }, 200},
		{"malformed association", func(p map[string]any) { p["repository"] = []any{} }, 200},
		{"public linked repository", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 2, "full_name": "Example/Source", "private": false, "visibility": "public"}
		}, 200},
		{"private linked repository", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 2, "full_name": "Example/Source", "private": true, "visibility": "private"}
		}, 200},
		{"unknown linked privacy", func(p map[string]any) {
			p["repository"] = map[string]any{"id": 2, "full_name": "Example/Source", "private": true}
		}, 200},
		{"wrong package", func(p map[string]any) { p["name"] = "other/ci-artifacts" }, 200},
		{"wrong owner", func(p map[string]any) { p["owner"] = map[string]any{"login": "Other"} }, 200},
		{"auth refusal", func(map[string]any) {}, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newStorageFixture(t, func(int) (int, any) { p := privatePackage(); tc.change(p); return tc.status, p })
			_, err := f.b.upload(t.Context(), testRequest, filepath.Join(t.TempDir(), "nonexistent-workspace"), []string{"missing"}, testOptions)
			assertCode(t, err, e.CodeTrustPolicyMissing)
			if f.transport.calls.Load() != 0 {
				t.Fatal("registry contacted before privacy admission")
			}
		})
	}
}

func TestPrivateCarrierSeedHasNoPayload(t *testing.T) {
	f := newStorageFixture(t, func(n int) (int, any) {
		if n == 1 {
			return http.StatusNotFound, nil
		}
		p := privatePackage()
		p["repository"] = map[string]any{"id": 2, "full_name": "Example/Source", "private": false, "visibility": "public"}
		return http.StatusOK, p
	})
	_, err := f.b.upload(t.Context(), testRequest, "/not-a-workspace", []string{"secret"}, testOptions)
	assertCode(t, err, e.CodeTrustPolicyMissing)
	ref, err := name.NewTag(testRequest.packageName()+":privacy-probe-v1", name.StrictValidation)
	if err != nil {
		t.Fatal(err)
	}
	image, err := remote.Image(ref, f.b.registryOptions(t.Context(), testOptions)...)
	if err != nil {
		t.Fatal(err)
	}
	layers, err := image.Layers()
	if err != nil || len(layers) != 0 {
		t.Fatalf("seed contains payload layers: %v", err)
	}
	config, err := image.ConfigFile()
	if err != nil || config.Config.Labels[BindingLabel] != "" {
		t.Fatalf("seed carries source metadata: %v", err)
	}
}

func TestPrivateCarrierRejectsLocatorSubstitutionBeforeFetch(t *testing.T) {
	f := newStorageFixture(t, admittedPolicy)
	root := t.TempDir()
	writePayload(t, root, "data/payload", "exact payload", 0o600)
	l, err := f.b.upload(t.Context(), testRequest, root, []string{"data/payload"}, testOptions)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*Locator)
	}{
		{"wrong attempt", func(l *Locator) { l.Request.Attempt = "3" }},
		{"wrong run", func(l *Locator) { l.Request.RunID = "124" }},
		{"wrong name", func(l *Locator) { l.Request.Name = "other" }},
		{"wrong package", func(l *Locator) { l.Reference = strings.Replace(l.Reference, "/source/", "/other/", 1) }},
		{"mutable reference", func(l *Locator) { l.Reference = testRequest.packageName() + ":latest" }},
		{"unsafe path", func(l *Locator) { l.Paths = []string{"../payload"} }},
		{"unsafe strip", func(l *Locator) { l.StripPrefix = "../" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := l
			tc.change(&changed)
			before, apiBefore := f.transport.calls.Load(), f.gets
			assertCode(t, f.b.download(t.Context(), testRequest, changed, t.TempDir(), testOptions), e.CodeVerifyFailed)
			if before != f.transport.calls.Load() || apiBefore != f.gets {
				t.Fatal("substituted locator performed network access")
			}
		})
	}
	for _, tc := range []struct {
		name   string
		change func(*Locator)
	}{
		{"archive size", func(l *Locator) { l.ArchiveSize++ }},
		{"archive digest", func(l *Locator) { l.ArchiveDigest = "sha256:" + strings.Repeat("0", 64) }},
		{"strip binding", func(l *Locator) { l.StripPrefix = "" }},
		{"path binding", func(l *Locator) { l.Paths = []string{"data/substituted"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := l
			tc.change(&changed)
			destination := t.TempDir()
			assertCode(t, f.b.download(t.Context(), testRequest, changed, destination, testOptions), e.CodeVerifyFailed)
			entries, err := os.ReadDir(destination)
			if err != nil || len(entries) != 0 {
				t.Fatalf("substituted metadata exposed payload: %v", err)
			}
		})
	}
}

func TestPrivateCarrierCancellationAndUnsafeSource(t *testing.T) {
	f := newStorageFixture(t, admittedPolicy)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := f.b.upload(ctx, testRequest, t.TempDir(), []string{"payload"}, testOptions)
	assertCode(t, err, e.CodeTimeout)
	root := t.TempDir()
	writePayload(t, root, "payload/file", "data", 0o600)
	if err := os.Symlink("../../outside", filepath.Join(root, "payload/escape")); err != nil {
		t.Fatal(err)
	}
	_, err = f.b.upload(t.Context(), testRequest, root, []string{"payload"}, testOptions)
	if err == nil {
		t.Fatal("escaping payload symlink admitted")
	}
	if f.transport.calls.Load() != 0 {
		t.Fatal("unsafe payload reached registry")
	}
}

func TestPrivateCarrierPrivacyRecheckAndDownloadAdmission(t *testing.T) {
	for _, tc := range []struct {
		name       string
		visibility string
		linked     any
	}{
		{"private unlinked", "private", nil},
		{"internal unlinked", "internal", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deny := false
			f := newStorageFixture(t, func(int) (int, any) {
				p := privatePackage()
				p["visibility"], p["repository"] = tc.visibility, tc.linked
				if deny {
					p["visibility"] = "public"
				}
				return http.StatusOK, p
			})
			root := t.TempDir()
			writePayload(t, root, "payload", "exact bytes", 0o600)
			l, err := f.b.upload(t.Context(), testRequest, root, []string{"payload"}, testOptions)
			if err != nil {
				t.Fatal(err)
			}
			deny = true
			before := f.transport.calls.Load()
			assertCode(t, f.b.download(t.Context(), testRequest, l, filepath.Join(t.TempDir(), "absent"), testOptions), e.CodeTrustPolicyMissing)
			if f.transport.calls.Load() != before {
				t.Fatal("payload fetched after package became public")
			}
		})
	}
	f := newStorageFixture(t, func(n int) (int, any) {
		p := privatePackage()
		if n > 1 {
			p["visibility"] = "public"
		}
		return http.StatusOK, p
	})
	root := t.TempDir()
	writePayload(t, root, "payload", "exact bytes", 0o600)
	_, err := f.b.upload(t.Context(), testRequest, root, []string{"payload"}, testOptions)
	assertCode(t, err, e.CodeTrustPolicyMissing)
	if f.transport.calls.Load() != 0 {
		t.Fatal("payload uploaded after pre-upload visibility changed")
	}
}

func TestPrivateCarrierStopsBlockedAdmission(t *testing.T) {
	started, finished := make(chan struct{}), make(chan struct{})
	api := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(finished)
	}))
	defer api.Close()
	b := &backend{api: api.URL, client: api.Client(), transport: http.DefaultTransport}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := b.upload(ctx, testRequest, "/not-a-workspace", []string{"payload"}, testOptions)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("admission request did not start")
	}
	cancel()
	select {
	case err := <-done:
		assertCode(t, err, e.CodeTimeout)
	case <-time.After(5 * time.Second):
		t.Fatal("canceled admission did not join")
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("admission HTTP handler remained active")
	}
}
