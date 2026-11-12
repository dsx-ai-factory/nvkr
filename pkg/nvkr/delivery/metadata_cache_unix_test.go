// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package delivery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestMetadataCacheLockCancellationPrecedesWork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "unexpected registry work", http.StatusInternalServerError)
	}))
	defer server.Close()
	cache := t.TempDir()
	digest := strings.Repeat("a", 64)
	path := filepath.Join(cache, digest+".lock")
	unlock, err := lockMetadataCache(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	visited := false
	ref := strings.TrimPrefix(server.URL, "http://") + "/product/releases@sha256:" + digest
	err = VisitCachedMetadata(ctx, ref, cache, MetadataPullOptions{Registry: RegistryOptions{PlainHTTP: true, Anonymous: true}}, func(context.Context, string, Inspection) error {
		visited = true
		return nil
	})
	assertRegistryCode(t, err, e.CodeTimeout)
	if visited || requests.Load() != 0 {
		t.Fatalf("canceled waiter did work: visitor=%t requests=%d", visited, requests.Load())
	}
	unlock()
	reuseCtx, reuseCancel := context.WithTimeout(t.Context(), time.Second)
	defer reuseCancel()
	release, err := lockMetadataCache(reuseCtx, path)
	if err != nil {
		t.Fatal("released lock could not be reused", err)
	}
	release()
}

func TestMetadataCacheLockRefusesUnsafeFiles(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(map[bool]string{false: "nonempty", true: "symlink"}[symlink], func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			contents := []byte("preserve this file")
			if err := os.WriteFile(target, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			path := target
			if symlink {
				path = filepath.Join(root, "lock")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			unlock, err := lockMetadataCache(t.Context(), path)
			if unlock != nil {
				unlock()
				t.Fatal("unsafe lock was acquired")
			}
			assertRegistryCode(t, err, e.CodeWriteConflict)
			raw, err := os.ReadFile(target)
			if err != nil || string(raw) != string(contents) {
				t.Fatal("lock admission changed existing bytes", err)
			}
		})
	}
}
