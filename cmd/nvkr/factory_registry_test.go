// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		_, err := invokeLifecycleCLI(t, append([]string{"factory", "registry-check"}, args...)...)
		assertLifecycleCommandCode(t, err, e.CodeUsage)
	}
}
