// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"encoding/json"
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

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestPullMetadataReusesRepositoryNegotiationWithinOneOperation(t *testing.T) {
	full, digest := releasePublicationFixture(t)
	expected, err := Inspect(t.Context(), full)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &metadataNegotiationRegistry{handler: registry.New(registry.Logger(log.New(io.Discard, "", 0)))}
	server := httptest.NewServer(fixture)
	defer server.Close()
	reference, err := Push(t.Context(), full, strings.TrimPrefix(server.URL, "http://")+"/product/releases", RegistryOptions{PlainHTTP: true, Anonymous: true})
	if err != nil {
		t.Fatal(err)
	}
	fixture.enabled.Store(true)
	for _, test := range []struct {
		name, username, password string
		refused                  bool
	}{
		{"first reader", "fixture-first", "fixture-first-password", false},
		{"independent second reader", "fixture-second", "fixture-second-password", false},
		{"previous credential cannot borrow second reader", "fixture-first", "fixture-first-password", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture.mu.Lock()
			if !test.refused {
				fixture.username, fixture.password = test.username, test.password
			}
			fixture.pings = 0
			fixture.tokens, fixture.manifests, fixture.blobs = map[string]int{}, map[string]int{}, map[string]int{}
			fixture.mu.Unlock()
			parent := t.TempDir()
			got, err := PullMetadata(t.Context(), reference, filepath.Join(parent, "metadata"), RegistryOptions{
				PlainHTTP: true, Username: test.username, Password: test.password,
			})
			if test.refused {
				assertRegistryCode(t, err, e.CodeFetchFailed)
				if !reflect.DeepEqual(got, Inspection{}) {
					t.Fatal("refused credential returned partial inspection")
				}
				entries, readErr := os.ReadDir(parent)
				if readErr != nil || len(entries) != 0 {
					t.Fatalf("refused credential left output or scratch: %v / %v", entries, readErr)
				}
				return
			}
			if err != nil || got.RootDigest != digest || !reflect.DeepEqual(got.Root, expected.Root) {
				t.Fatalf("metadata pull did not preserve complete exact inspection: %v", err)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.pings != 2 || len(fixture.tokens) != 2 {
				t.Errorf("expected one fresh negotiation per repository: pings=%d, scopes=%v", fixture.pings, fixture.tokens)
			}
			for _, repository := range []string{"product/releases", "product/tools"} {
				if fixture.tokens[repository] != 1 || fixture.manifests[repository] < 2 || fixture.blobs[repository] < 1 {
					t.Errorf("repository %s: tokens=%d manifests=%d blobs=%d", repository, fixture.tokens[repository], fixture.manifests[repository], fixture.blobs[repository])
				}
			}
			if fixture.blobs["product/tools"] < 3 {
				t.Fatal("fixture did not exercise repeated configs and evidence in one repository")
			}
		})
	}
}

type metadataNegotiationRegistry struct {
	handler   http.Handler
	enabled   atomic.Bool
	mu        sync.Mutex
	username  string
	password  string
	pings     int
	tokens    map[string]int
	manifests map[string]int
	blobs     map[string]int
}

func (fixture *metadataNegotiationRegistry) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !fixture.enabled.Load() {
		fixture.handler.ServeHTTP(w, r)
		return
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if r.URL.Path == "/v2/" {
		fixture.pings++
		w.Header().Set("WWW-Authenticate", `Bearer realm="http://`+r.Host+`/token",service="metadata-fixture"`)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Path == "/token" {
		username, password, ok := r.BasicAuth()
		if !ok || username != fixture.username || password != fixture.password {
			http.Error(w, "fixture credential refused", http.StatusUnauthorized)
			return
		}
		scope := r.URL.Query().Get("scope")
		repository := strings.TrimSuffix(strings.TrimPrefix(scope, "repository:"), ":pull")
		if scope != "repository:"+repository+":pull" || (repository != "product/releases" && repository != "product/tools") {
			http.Error(w, "fixture scope refused", http.StatusForbidden)
			return
		}
		fixture.tokens[repository]++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "fixture-token-" + username + ":" + repository})
		return
	}
	repository, _, manifest := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v2/"), "/manifests/")
	if !manifest {
		repository, _, _ = strings.Cut(strings.TrimPrefix(r.URL.Path, "/v2/"), "/blobs/")
	}
	if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer fixture-token-"+fixture.username+":"+repository {
		http.Error(w, "fixture repository authorization refused", http.StatusForbidden)
		return
	}
	if manifest {
		fixture.manifests[repository]++
	} else {
		fixture.blobs[repository]++
	}
	fixture.handler.ServeHTTP(w, r)
}
