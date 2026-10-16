// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
)

func checkLocalPublicationAdmission(t *testing.T, source *localVerifiedQualification) {
	t.Helper()
	for _, mode := range []string{"unissued", "closed", "tagged", "scratch", "credentials", "canceled"} {
		t.Run("publication admission/"+mode, func(t *testing.T) {
			candidate := *source
			candidate.closed = new(atomic.Bool)
			owner := &candidate
			repo, scratch := "localhost:5000/fixture", t.TempDir()
			options := delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			code := e.CodeUsage
			switch mode {
			case "unissued":
				owner = nil
				code = e.CodeGateUnmet
			case "closed":
				_ = owner.Close()
				code = e.CodeGateUnmet
			case "tagged":
				repo += ":latest"
			case "scratch":
				scratch = ""
			case "credentials":
				options.Username = "invalid"
			case "canceled":
				cancel()
				code = e.CodeTimeout
			}
			report, consumer, err := publishLocalQualification(ctx, owner, repo, scratch, "unused", options)
			assertCode(t, err, code)
			if consumer != nil || report.Evidence != "" || report.Ordinary != "" || len(report.Disks) != 0 {
				t.Fatal("admission failure started publication")
			}
		})
	}
}

func checkRealLocalPublication(t *testing.T, source *localVerifiedQualification, binary string) {
	t.Helper()
	for _, mode := range []string{"exact", "readback-failure"} {
		t.Run("publication orchestration/"+mode, func(t *testing.T) {
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "readback-failure" && r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/evidence/manifests/") {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			scratch := t.TempDir()
			report, consumer, err := publishLocalQualification(t.Context(), source, strings.TrimPrefix(server.URL, "http://")+"/qualification", scratch, binary, delivery.RegistryOptions{PlainHTTP: true, Anonymous: true})
			if mode == "readback-failure" {
				assertCode(t, err, e.CodeUnavailable)
				if consumer != nil {
					_ = consumer.Close()
					t.Fatal("failed readback returned acceptance")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err := consumer.qualified.requireLive(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err := consumer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if report.Ordinary == "" || report.Evidence == "" || len(report.Disks) != len(source.build.capture.disks.members) {
				t.Fatal("publication lost attempted destinations")
			}
			entries, err := os.ReadDir(scratch)
			if err != nil || len(entries) != 0 {
				t.Fatal("publication scratch leaked", err)
			}
			if err := source.requireLive(t.Context()); err != nil {
				t.Fatal("publication closed source", err)
			}
		})
	}
}
