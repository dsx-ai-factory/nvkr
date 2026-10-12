// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
)

func TestDiskHTTPUsesExactHeldBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "held.qcow2")
	raw := []byte(strings.Repeat("actual-disk-bytes", 256))
	if err := os.WriteFile(path, raw, 0o444); err != nil {
		t.Fatal(err)
	}
	service, err := startDiskService(t.Context(), path, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := service.Stop(ctx); err != nil {
			t.Error(err)
		}
	}()
	image := lifecycle.Artifact{Digest: "sha256:" + plainSHA(raw), Disk: &delivery.DiskOutput{Size: int64(len(raw))}}
	if err = verifyHTTPDisk(t.Context(), service.url, image); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		size   int64
		digest string
	}{{"truncated", int64(len(raw) - 1), image.Digest}, {"oversize", int64(len(raw) + 1), image.Digest}, {"substituted", int64(len(raw)), "sha256:" + strings.Repeat("f", 64)}} {
		t.Run(test.name, func(t *testing.T) {
			bad := image
			disk := *image.Disk
			disk.Size = test.size
			bad.Disk = &disk
			bad.Digest = test.digest
			if err := verifyHTTPDisk(t.Context(), service.url, bad); err == nil {
				t.Fatal("incorrect disk accepted")
			}
		})
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, suffix := range []string{"?extra=1", "/foreign"} {
		response, err := client.Get(service.url + suffix)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatal("unselected disk path served")
		}
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, service.url, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Range", "bytes=0-7")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusPartialContent || string(data) != string(raw[:8]) {
		t.Fatal("standard HTTP range failed")
	}
}
func TestDiskHTTPDoesNotFollowSubstitution(t *testing.T) {
	parent := t.TempDir()
	original := filepath.Join(parent, "original")
	if err := os.WriteFile(original, []byte("held"), 0o444); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(original, link); err != nil {
		t.Fatal(err)
	}
	if service, err := startDiskService(t.Context(), link, "127.0.0.1:0"); err == nil {
		_ = service.Stop(t.Context())
		t.Fatal("symlink admitted")
	}
}

func TestRetainedDiskHTTPOutlivesWorkCancellationAndJoinsExplicitStop(t *testing.T) {
	owner, work, cancelWork := retainedTestOwner(t)
	path := filepath.Join(privateTestDir(t), "held.qcow2")
	raw := []byte(strings.Repeat("retained-exact-disk", 256))
	if err := os.WriteFile(path, raw, 0o444); err != nil {
		t.Fatal(err)
	}
	service, err := startDiskService(work, path, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
		defer stop()
		if err := service.Stop(cleanup); err != nil {
			t.Error(err)
		}
	}()
	cancelWork()
	image := lifecycle.Artifact{Digest: "sha256:" + plainSHA(raw), Disk: &delivery.DiskOutput{Size: int64(len(raw))}}
	if err := verifyHTTPDisk(t.Context(), service.url, image); err != nil {
		t.Fatal("canceled viewer lost the actual retained disk service", err)
	}
	if err := owner.requestCleanup(t.Context()); err != nil {
		t.Fatal(err)
	}
	// Stop is deliberately called once: it owns the Serve/handler/file join.
	// The deferred Stop above supplies the single cleanup obligation.
}
