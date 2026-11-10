// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestFactoryRegistryCheckReadOnlyReport(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v2/" {
					t.Error("diagnostic attempted a registry mutation")
				}
				w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
				w.WriteHeader(status)
			}))
			defer server.Close()
			output, err := invokeLifecycleCLI(t, "factory", "registry-check", "--repository", strings.TrimPrefix(server.URL, "http://")+"/project", "--anonymous", "--plain-http")
			if status == http.StatusOK && err != nil {
				t.Fatal(err)
			}
			if status == http.StatusForbidden {
				assertLifecycleCommandCode(t, err, e.CodeRegistryAuthFailed)
			}
			var report delivery.RegistryReport
			if err := json.Unmarshal([]byte(output), &report); err != nil || report.BaseStatus != status || report.OCIDistributionVersion != "unknown" || report.ManifestUpload != "unknown" {
				t.Fatalf("missing or overstated API report: %s %v", output, err)
			}
		})
	}
	for _, args := range [][]string{nil, {"--repository", "registry.invalid/project", "unexpected"}, {"--repository", "https://registry.invalid/project"}} {
		output, err := invokeLifecycleCLI(t, append([]string{"factory", "registry-check"}, args...)...)
		assertLifecycleCommandCode(t, err, e.CodeUsage)
		if output != "" {
			t.Fatalf("usage rejection exposed a partial report: %s", output)
		}
	}
}

func TestFactoryRegistryCheckCredentialSource(t *testing.T) {
	for _, test := range []struct {
		name, username, password, source string
		anonymous                        bool
		want                             e.Code
	}{
		{"explicit", "synthetic-user", "synthetic-password", "explicit", false, ""},
		{"ambient", "", "", "ambient", false, ""},
		{"anonymous overrides env", "synthetic-user", "synthetic-password", "anonymous", true, ""},
		{"username only", "synthetic-user", "", "", false, e.CodeUsage},
		{"credential only", "", "synthetic-password", "", false, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DOCKER_CONFIG", t.TempDir())
			t.Setenv("NVKR_REGISTRY_USER", test.username)
			t.Setenv("NVKR_CREDENTIAL", test.password)
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if test.want != "" {
					t.Error("invalid credential pair reached network I/O")
				}
				if test.source == "explicit" {
					user, password, ok := r.BasicAuth()
					if !ok || user != test.username || password != test.password {
						w.Header().Set("WWW-Authenticate", `Basic realm="synthetic"`)
						w.WriteHeader(http.StatusUnauthorized)
						return
					}
				} else if r.Header.Get("Authorization") != "" {
					t.Error("ambient/anonymous fixture unexpectedly received credentials")
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			args := []string{"factory", "registry-check", "--repository", strings.TrimPrefix(server.URL, "http://") + "/project", "--plain-http"}
			if test.anonymous {
				args = append(args, "--anonymous")
			}
			output, err := invokeLifecycleCLI(t, args...)
			if test.want != "" {
				assertLifecycleCommandCode(t, err, test.want)
				if output != "" || requests.Load() != 0 {
					t.Fatal("invalid credentials produced output or I/O")
				}
				return
			}
			var report delivery.RegistryReport
			if err != nil || json.Unmarshal([]byte(output), &report) != nil || report.CredentialSource != test.source {
				t.Fatalf("credential source was not reported safely: %s %v", output, err)
			}
			if strings.Contains(output, "synthetic-user") || strings.Contains(output, "synthetic-password") {
				t.Fatal("report disclosed a credential value")
			}
		})
	}
}

func TestFactoryRegistryCheckPreservesOwnerOnOutputFailure(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
			defer server.Close()
			outputErr := errors.New("synthetic diagnostic writer failure")
			app := newApp()
			app.Writer = failingRunWriter{err: outputErr}
			err := app.RunContext(t.Context(), []string{"nvkr", "factory", "registry-check", "--repository", strings.TrimPrefix(server.URL, "http://") + "/project", "--anonymous", "--plain-http"})
			want := e.CodeInternal
			if status == http.StatusForbidden {
				want = e.CodeRegistryAuthFailed
			}
			assertLifecycleCommandCode(t, err, want)
			if !errors.Is(err, outputErr) {
				t.Fatal("reporting cause was lost")
			}
		})
	}
}
