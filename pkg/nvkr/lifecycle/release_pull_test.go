// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPullReleaseOutputAdmitsBeforeWork(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	reference := strings.TrimPrefix(server.URL, "http://") + "/product/releases@sha256:" + strings.Repeat("a", 64)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name    string
		ctx     context.Context
		ref     string
		output  string
		options PullOptions
		code    e.Code
	}{
		{"nil context", nil, reference, "tools", PullOptions{}, e.CodeUsage},
		{"canceled", canceled, reference, "tools", PullOptions{}, e.CodeTimeout},
		{"timeout", t.Context(), reference, "tools", PullOptions{Timeout: -time.Second}, e.CodeUsage},
		{"tag", t.Context(), strings.Split(reference, "@")[0] + ":latest", "tools", PullOptions{}, e.CodeUnverifiedRef},
		{"blank output", t.Context(), reference, "", PullOptions{}, e.CodeUsage},
		{"credentials", t.Context(), reference, "tools", PullOptions{Registry: delivery.RegistryOptions{Username: "fixture"}}, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.options.WorkspaceRoot = filepath.Join(root, "workspace")
			out := filepath.Join(root, "output")
			bundle, err := PullReleaseOutput(test.ctx, test.ref, test.output, out, test.options)
			assertCode(t, err, test.code)
			if !reflect.DeepEqual(bundle, delivery.PulledOutput{}) {
				t.Fatal("failed pull returned a bundle")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed admission wrote files: %v, %v", entries, err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatal("failed admission contacted registry")
	}
}

func TestPullReleaseOutputDeadlineCleansWorkspace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	reference := strings.TrimPrefix(server.URL, "http://") + "/product/releases@sha256:" + strings.Repeat("a", 64)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	out := filepath.Join(root, "output")
	bundle, err := PullReleaseOutput(t.Context(), reference, "tools", out, PullOptions{
		Timeout: 50 * time.Millisecond, WorkspaceRoot: workspace,
		Registry: delivery.RegistryOptions{Anonymous: true, PlainHTTP: true},
	})
	assertCode(t, err, e.CodeTimeout)
	if !reflect.DeepEqual(bundle, delivery.PulledOutput{}) {
		t.Fatal("timed out pull returned a bundle")
	}
	if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("timed out pull left output: %v", err)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("timed out pull leaked workspace: %v, %v", entries, err)
	}
}
