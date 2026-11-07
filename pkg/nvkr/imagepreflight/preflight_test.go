// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package imagepreflight

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const testPlan = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const secretMarker = "SYNTHETIC-SECRET-MUST-NOT-LEAK"

type rewriteTransport struct {
	target *url.URL
	inner  http.RoundTripper
}

func (r rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	copy.URL.Scheme, copy.URL.Host = r.target.Scheme, r.target.Host
	return r.inner.RoundTrip(copy)
}

type registryFixture struct {
	t                       *testing.T
	mode                    string
	private                 bool
	manifest, child, config []byte
	media                   types.MediaType
	mu                      sync.Mutex
	requests                map[string]int
}

func fixture(t *testing.T, mode string, private bool) (*registryFixture, http.RoundTripper) {
	t.Helper()
	f := &registryFixture{t: t, mode: mode, private: private, requests: map[string]int{}, media: types.OCIManifestSchema1}
	architecture := "amd64"
	if mode == "config-platform" {
		architecture = "arm64"
	}
	f.config = []byte(fmt.Sprintf(`{"architecture":%q,"os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`, architecture))
	configHash, _ := v1.NewHash(blobDigest(f.config))
	layerHash, _ := v1.NewHash(testPlan)
	f.child, _ = json.Marshal(v1.Manifest{SchemaVersion: 2, MediaType: types.OCIManifestSchema1, Config: v1.Descriptor{MediaType: types.OCIConfigJSON, Digest: configHash, Size: int64(len(f.config))}, Layers: []v1.Descriptor{{MediaType: types.OCILayer, Digest: layerHash, Size: 99999}}})
	f.manifest = f.child
	if strings.HasPrefix(mode, "index") {
		platform := &v1.Platform{OS: "linux", Architecture: "amd64"}
		if mode == "index-missing-platform" {
			platform.Architecture = "arm64"
		}
		hash, _ := v1.NewHash(blobDigest(f.child))
		children := []v1.Descriptor{{MediaType: types.OCIManifestSchema1, Digest: hash, Size: int64(len(f.child)), Platform: platform}}
		if mode == "index-ambiguous" {
			children = append(children, children[0])
		}
		index := v1.IndexManifest{SchemaVersion: 2, MediaType: types.OCIImageIndex, Manifests: children}
		switch mode {
		case "index-omitted-media":
			index.MediaType = ""
		case "index-mismatched-media":
			index.MediaType = types.OCIManifestSchema1
		case "index-invalid-schema":
			index.SchemaVersion = 1
		}
		f.manifest, _ = json.Marshal(index)
		f.media = types.OCIImageIndex
	}
	if mode == "malformed" {
		f.manifest = []byte(`{"schemaVersion":2`)
	}
	if mode == "oversize" {
		f.manifest = []byte(strings.Repeat("x", maxMetadataBytes+1))
	}
	server := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	return f, rewriteTransport{target: target, inner: server.Client().Transport}
}

func (f *registryFixture) subject() Subject {
	route := Anonymous
	if f.private {
		route = "github:TEST_READER|github:TEST_USER"
	}
	return Subject{Image: "ghcr.io/test/tool@" + blobDigest(f.manifest), Platform: "linux/amd64", Route: route, Placement: "ubuntu-latest"}
}

func (f *registryFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	key := r.Method + " " + r.URL.Path
	f.requests[key]++
	count := f.requests[key]
	f.mu.Unlock()
	if r.URL.Path == "/config" {
		if r.Header.Get("Authorization") != "" {
			f.t.Error("config CDN received reader credentials")
		}
		_, _ = w.Write(f.config)
		return
	}
	if r.URL.Path == "/v2/" {
		realm := "https://ghcr.io/token"
		if f.mode == "foreign-reader-realm" {
			realm = "https://foreign.example/token"
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="`+realm+`",service="ghcr.io"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/token" {
		if r.URL.Query().Get("scope") != "repository:test/tool:pull" {
			f.t.Errorf("unexpected token scope: %q", r.URL.Query().Get("scope"))
		}
		user, password, _ := r.BasicAuth()
		if f.private && (user != "reader" || password != secretMarker) {
			f.t.Error("reader credentials missing")
		}
		if !f.private && r.Header.Get("Authorization") != "" {
			f.t.Error("anonymous route received credentials")
		}
		if f.mode == "token-forbidden" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(secretMarker))
			return
		}
		if f.mode == "malformed-token" {
			_, _ = w.Write([]byte(`{"unexpected":"` + secretMarker + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"fixture-bearer"}`))
		return
	}
	if r.Header.Get("Authorization") != "Bearer fixture-bearer" {
		f.t.Error("request omitted negotiated bearer token")
	}
	if strings.Contains(r.URL.Path, "/manifests/") {
		switch f.mode {
		case "unauthorized":
			w.Header().Set("WWW-Authenticate", `Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:test/tool:push,pull"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(secretMarker))
			return
		case "forbidden":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(secretMarker))
			return
		case "missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(secretMarker))
			return
		case "head-fallback":
			if r.Method == http.MethodHead {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
		case "transient":
			if count == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		case "transient-exhausted":
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		case "cancel":
			<-r.Context().Done()
			return
		}
		raw, media := f.manifest, f.media
		if strings.HasSuffix(r.URL.Path, blobDigest(f.child)) {
			raw, media = f.child, types.OCIManifestSchema1
		}
		w.Header().Set("Content-Type", string(media))
		switch f.mode {
		case "parameterized-media", "index-parameterized-media":
			w.Header().Set("Content-Type", string(media)+"; charset=utf-8")
		case "malformed-media":
			w.Header().Set("Content-Type", string(media)+"; charset")
		case "unsupported-media":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		}
		w.Header().Set("Docker-Content-Digest", blobDigest(raw))
		if f.mode == "head-digest" && r.Method == http.MethodHead {
			w.Header().Set("Docker-Content-Digest", testPlan)
		}
		if r.Method == http.MethodGet {
			if f.mode == "digest" {
				raw = []byte("wrong bytes " + secretMarker)
			}
			_, _ = w.Write(raw)
		}
		return
	}
	if r.URL.Path == "/v2/test/tool/blobs/"+blobDigest(f.config) {
		if f.mode == "config-redirect" {
			w.Header().Set("Location", "https://cdn.example/config?signature=SYNTHETIC")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		_, _ = w.Write(f.config)
		return
	}
	f.t.Errorf("unexpected request (possible layer download): %s", r.URL.Path)
	w.WriteHeader(http.StatusNotFound)
}

func TestExactManifestReaderPreflight(t *testing.T) {
	for _, test := range []struct {
		mode string
		code e.Code
	}{
		{"image", ""}, {"index", ""}, {"head-fallback", ""}, {"transient", ""}, {"config-redirect", ""},
		{"index-omitted-media", ""}, {"parameterized-media", ""}, {"index-parameterized-media", ""},
		{"index-mismatched-media", e.CodeFetchFailed}, {"index-invalid-schema", e.CodeFetchFailed},
		{"malformed-media", e.CodeFetchFailed}, {"unsupported-media", e.CodeFetchFailed},
		{"foreign-reader-realm", e.CodeFetchFailed},
		{"malformed-token", e.CodeFetchFailed},
		{"unauthorized", e.CodeFetchFailed}, {"forbidden", e.CodeFetchFailed}, {"token-forbidden", e.CodeFetchFailed}, {"missing", e.CodeFetchFailed},
		{"digest", e.CodeFetchFailed}, {"head-digest", e.CodeFetchFailed}, {"malformed", e.CodeFetchFailed}, {"oversize", e.CodeFetchFailed}, {"transient-exhausted", e.CodeFetchFailed},
		{"config-platform", e.CodeEngineCapability}, {"index-missing-platform", e.CodeEngineCapability}, {"index-ambiguous", e.CodeEngineCapability},
	} {
		t.Run(test.mode, func(t *testing.T) {
			f, rt := fixture(t, test.mode, true)
			now := time.Date(2026, 11, 6, 1, 2, 3, 0, time.UTC)
			report, err := observe(t.Context(), testPlan, []Subject{f.subject(), f.subject()}, func(context.Context, string) (Credential, error) {
				return Credential{Username: "reader", Token: secretMarker}, nil
			}, rt, func() time.Time { return now })
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				if report.PlanDigest != testPlan || len(report.Observations) != 1 || report.Observations[0].Manifest != blobDigest(f.child) || report.Observations[0].ObservedAt != now {
					t.Fatalf("unexpected report: %+v", report)
				}
			} else {
				if errorCode(err) != test.code || report != nil {
					t.Fatalf("report=%+v error=%v, want %s", report, err, test.code)
				}
				if strings.Contains(err.Error(), secretMarker) {
					t.Fatal("error leaked secret/body")
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for key, count := range f.requests {
				bound := 1
				if test.mode == "transient" || test.mode == "transient-exhausted" {
					bound = 2
				}
				if count > bound {
					t.Errorf("request %s count=%d exceeds %d", key, count, bound)
				}
			}
		})
	}
}

func TestAnonymousRouteAndMissingReader(t *testing.T) {
	for _, mode := range []string{"anonymous", "missing-reader", "unknown-route"} {
		t.Run(mode, func(t *testing.T) {
			f, rt := fixture(t, "image", false)
			subject := f.subject()
			if mode == "missing-reader" {
				subject.Route = "reader"
			}
			if mode == "unknown-route" {
				subject.Route = ""
			}
			report, err := observe(t.Context(), testPlan, []Subject{subject}, nil, rt, time.Now)
			if mode == "anonymous" {
				if err != nil || report == nil {
					t.Fatal(err)
				}
				return
			}
			if errorCode(err) != e.CodeEngineCapability {
				t.Fatalf("got %v", err)
			}
			if len(f.requests) != 0 {
				t.Fatal("network before route admission")
			}
		})
	}
}

func TestPreflightObservationDeduplicationAndOrdering(t *testing.T) {
	f, rt := fixture(t, "image", false)
	a, b, c := f.subject(), f.subject(), f.subject()
	a.Placement = "z-runner"
	b.Placement = "a-runner"
	c.Placement = "m-runner"
	called := false
	report, err := observe(t.Context(), testPlan, []Subject{a, c, b, a, b}, func(context.Context, string) (Credential, error) {
		called = true
		return Credential{Token: secretMarker}, nil
	}, rt, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if called || len(report.Observations) != 3 {
		t.Fatal("anonymous route resolved credentials or deduplication lost placement")
	}
	for index, want := range []string{"a-runner", "m-runner", "z-runner"} {
		if report.Observations[index].Placement != want {
			t.Fatal("observations are not sorted")
		}
	}
}

func TestPreflightRejectsUnadmittedPlatformsBeforeIO(t *testing.T) {
	for _, platform := range []string{"", "linux", "linux/amd64:unverified-os-version", "linux/arm64/v8", " linux/amd64 ", "darwin/arm64"} {
		t.Run(platform, func(t *testing.T) {
			subject := Subject{Image: "ghcr.io/test/tool@" + testPlan, Platform: platform, Route: Anonymous, Placement: "runner"}
			_, err := observe(t.Context(), testPlan, []Subject{subject}, nil, roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("network before platform admission")
				return nil, nil
			}), time.Now)
			if errorCode(err) != e.CodeEngineCapability {
				t.Fatal(err)
			}
		})
	}
}

func TestPreflightAdmitsAllSubjectsBeforeCredentialResolution(t *testing.T) {
	for _, malformed := range []string{"platform", "subject", "route"} {
		t.Run(malformed, func(t *testing.T) {
			good := Subject{Image: "ghcr.io/test/tool@" + testPlan, Platform: "linux/amd64", Route: "reader", Placement: "runner"}
			bad := good
			switch malformed {
			case "platform":
				bad.Platform = "linux/unknown"
			case "subject":
				bad.Image = "ghcr.io/test/tool:mutable"
			case "route":
				bad.Route = ""
			}
			_, err := observe(t.Context(), testPlan, []Subject{good, bad}, func(context.Context, string) (Credential, error) {
				t.Fatal("credential I/O before complete admission")
				return Credential{}, nil
			}, nil, time.Now)
			if errorCode(err) != e.CodeEngineCapability {
				t.Fatal(err)
			}
		})
	}
}

func TestMetadataTransportBoundsRequestsAndTokenBodies(t *testing.T) {
	for _, large := range []bool{false, true} {
		t.Run(fmt.Sprint(large), func(t *testing.T) {
			calls := 0
			bounded := &metadataTransport{registry: "ghcr.io", repository: "test/tool", paths: map[string]bool{"/v2/": true}, base: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				deadline, ok := r.Context().Deadline()
				if !ok || time.Until(deadline) > requestTimeout {
					t.Fatal("request timeout missing")
				}
				raw := "{}"
				if large {
					raw = strings.Repeat("x", (64<<10)+1)
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(raw)), ContentLength: int64(len(raw))}, nil
			})}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://ghcr.io/token?scope=repository:test/tool:pull", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := bounded.RoundTrip(request)
			if response != nil {
				_ = response.Body.Close()
			}
			if (err != nil) != large || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestCancellationJoinsWorkersAndBoundsConcurrency(t *testing.T) {
	var active, maximum atomic.Int32
	started := make(chan struct{}, workers)
	rt := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for previous := maximum.Load(); n > previous; previous = maximum.Load() {
			if maximum.CompareAndSwap(previous, n) {
				break
			}
		}
		started <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	subjects := make([]Subject, workers*3)
	for i := range subjects {
		subjects[i] = Subject{Image: "ghcr.io/test/tool@" + testPlan, Platform: "linux/amd64", Route: Anonymous, Placement: fmt.Sprintf("runner-%02d", i)}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := observe(ctx, testPlan, subjects, nil, rt, time.Now); done <- err }()
	for range workers {
		select {
		case <-started:
		case <-time.After(time.Second * 5):
			t.Fatal("workers did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if errorCode(err) != e.CodeTimeout {
			t.Fatal(err)
		}
	case <-time.After(time.Second * 5):
		t.Fatal("cancellation did not join workers")
	}
	if active.Load() != 0 || maximum.Load() > workers {
		t.Fatalf("active=%d maximum=%d", active.Load(), maximum.Load())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func errorCode(err error) e.Code {
	var coded *e.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
