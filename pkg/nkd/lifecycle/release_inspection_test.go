// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func TestInspectReleaseLocalAndRegistryHaveSameClosure(t *testing.T) {
	for _, private := range []bool{false, true} {
		t.Run(map[bool]string{false: "anonymous", true: "authenticated"}[private], func(t *testing.T) {
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if private {
					username, password, ok := r.BasicAuth()
					if !ok || username != "fixture" || password != "password" {
						w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			transport := delivery.RegistryOptions{PlainHTTP: true, Anonymous: !private}
			if private {
				transport.Username, transport.Password = "fixture", "password"
			}
			layout, _ := testPublicationLayout(t)
			reference, err := delivery.Push(t.Context(), layout, strings.TrimPrefix(server.URL, "http://")+"/product/releases:test", transport)
			if err != nil {
				t.Fatal(err)
			}
			local, err := InspectRelease(t.Context(), reference, InspectionOptions{LayoutPath: layout})
			if err != nil {
				t.Fatal(err)
			}
			workspace := filepath.Join(t.TempDir(), "inspection")
			remote, err := InspectRelease(t.Context(), reference, InspectionOptions{Registry: transport, WorkspaceRoot: workspace})
			if err != nil || !reflect.DeepEqual(local, remote) {
				t.Fatalf("registry view differs from exact local closure: %v\nlocal=%+v\nremote=%+v", err, local, remote)
			}
			if len(remote.Outputs) != 1 || len(remote.Evidence) == 0 || remote.Metadata.ContentSubject == "" {
				t.Fatalf("incomplete projection: %+v", remote)
			}
			entries, err := os.ReadDir(workspace)
			if err != nil || len(entries) != 0 {
				t.Fatalf("inspection leaked registry staging: %v, %v", entries, err)
			}
		})
	}
}

func TestInspectReleaseAdmissionPrecedesFilesystemAndNetwork(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	ref := strings.TrimPrefix(server.URL, "http://") + "/product/releases@sha256:" + strings.Repeat("a", 64)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		ref     string
		options InspectionOptions
		code    e.Code
	}{
		{"nil context", nil, ref, InspectionOptions{}, e.CodeUsage},
		{"canceled", canceled, ref, InspectionOptions{}, e.CodeTimeout},
		{"blank", t.Context(), "", InspectionOptions{}, e.CodeUnverifiedRef},
		{"whitespace", t.Context(), " " + ref, InspectionOptions{}, e.CodeUnverifiedRef},
		{"missing digest", t.Context(), strings.Split(ref, "@")[0], InspectionOptions{}, e.CodeUnverifiedRef},
		{"malformed digest", t.Context(), strings.Split(ref, "@")[0] + "@sha256:bad", InspectionOptions{}, e.CodeUnverifiedRef},
		{"multiple digests", t.Context(), ref + "@sha256:" + strings.Repeat("b", 64), InspectionOptions{}, e.CodeUnverifiedRef},
		{"tag", t.Context(), strings.Split(ref, "@")[0] + ":latest", InspectionOptions{}, e.CodeUnverifiedRef},
		{"URL scheme", t.Context(), "https://" + ref, InspectionOptions{}, e.CodeUnverifiedRef},
		{"implicit registry", t.Context(), "releases@sha256:" + strings.Repeat("a", 64), InspectionOptions{}, e.CodeUnverifiedRef},
		{"negative timeout", t.Context(), ref, InspectionOptions{Timeout: -time.Second}, e.CodeUsage},
		{"partial credential", t.Context(), ref, InspectionOptions{Registry: delivery.RegistryOptions{Username: "fixture"}}, e.CodeUsage},
		{"anonymous credentials", t.Context(), ref, InspectionOptions{Registry: delivery.RegistryOptions{Anonymous: true, Username: "fixture", Password: "password"}}, e.CodeUsage},
		{"mixed local registry", t.Context(), ref, InspectionOptions{LayoutPath: "missing", Registry: delivery.RegistryOptions{Anonymous: true}}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.options.WorkspaceRoot = filepath.Join(t.TempDir(), "must-not-exist")
			view, err := InspectRelease(tc.ctx, tc.ref, tc.options)
			assertCode(t, err, tc.code)
			if !reflect.DeepEqual(view, delivery.PublicationDescription{}) {
				t.Fatal("failed inspection returned a view")
			}
			if _, err := os.Stat(tc.options.WorkspaceRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed admission touched workspace: %v", err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatal("failed admission reached the registry")
	}
}

func TestInspectReleaseTimeoutCleansRegistryWorkspace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	reference := strings.TrimPrefix(server.URL, "http://") + "/product/releases@sha256:" + strings.Repeat("a", 64)
	workspace := filepath.Join(t.TempDir(), "inspection")
	view, err := InspectRelease(t.Context(), reference, InspectionOptions{Timeout: 50 * time.Millisecond, WorkspaceRoot: workspace, Registry: delivery.RegistryOptions{Anonymous: true, PlainHTTP: true}})
	assertCode(t, err, e.CodeTimeout)
	if !reflect.DeepEqual(view, delivery.PublicationDescription{}) {
		t.Fatal("timeout returned partial view")
	}
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("timeout leaked staging: %v, %v", entries, err)
	}
}

func TestInspectReleaseDefaultWorkspaceIsOwnedAndTemporary(t *testing.T) {
	layout, _ := testPublicationLayout(t)
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 1:
			w.WriteHeader(http.StatusForbidden)
		case 2:
			<-r.Context().Done()
		default:
			handler.ServeHTTP(w, r)
		}
	}))
	defer server.Close()
	transport := delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}
	reference, err := delivery.Push(t.Context(), layout, strings.TrimPrefix(server.URL, "http://")+"/product/releases:test", transport)
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	t.Chdir(cwd)
	if err := os.Chmod(cwd, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cwd, 0o700) })
	for _, tc := range []struct {
		name string
		mode int32
		code e.Code
	}{
		{"success", 0, ""},
		{"registry rejection", 1, e.CodeFetchFailed},
		{"timeout", 2, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode.Store(tc.mode)
			temporary := t.TempDir()
			t.Setenv("TMPDIR", temporary)
			options := InspectionOptions{Registry: transport, Timeout: 5 * time.Second}
			if tc.mode == 2 {
				options.Timeout = 50 * time.Millisecond
			}
			view, err := InspectRelease(t.Context(), reference, options)
			if tc.code == "" {
				if err != nil || len(view.Outputs) != 1 {
					t.Fatalf("default inspection failed in a read-only working directory: %+v, %v", view, err)
				}
			} else {
				assertCode(t, err, tc.code)
				if !reflect.DeepEqual(view, delivery.PublicationDescription{}) {
					t.Fatal("failed inspection returned a view")
				}
			}
			for _, directory := range []string{cwd, temporary} {
				entries, err := os.ReadDir(directory)
				if err != nil || len(entries) != 0 {
					t.Fatalf("inspection retained files in %s: %v, %v", directory, entries, err)
				}
			}
		})
	}
}
