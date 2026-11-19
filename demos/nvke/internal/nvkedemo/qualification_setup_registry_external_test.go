// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// This opt-in test uses the actual pinned registry and private, disk-backed
// storage. An HTTP readiness check alone cannot detect storage permission errors.
func TestExternalQualificationRegistryStorage(t *testing.T) {
	if os.Getenv("NVKR_TEST_LOCAL_REGISTRY") != "1" {
		t.Skip("set NVKR_TEST_LOCAL_REGISTRY=1 on native Linux with Docker")
	}
	if runtime.GOOS != "linux" || os.Getuid() == 0 {
		t.Fatal("requires a non-root Linux caller to exercise private storage ownership")
	}
	out := t.TempDir()
	if err := os.Chmod(out, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(out, "registry-data"), 0700); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Env: NativeEnvironment()}
	registry, err := startQualificationRegistry(t.Context(), runner, out)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Minute)
		defer cancel()
		if err := stopQualificationRegistry(ctx, runner, out, registry); err != nil {
			t.Error(err)
		}
	})
	ref, err := name.NewTag("localhost:"+registry.Port+"/storage-probe:check", name.StrictValidation, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, empty.Image, remote.WithContext(t.Context())); err != nil {
		t.Fatal(err)
	}
	actual, err := remote.Image(ref, remote.WithContext(t.Context()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := actual.Digest()
	if err != nil {
		t.Fatal(err)
	}
	want, err := empty.Image.Digest()
	if err != nil || got != want {
		t.Fatalf("registry read-back: got %s want %s: %v", got, want, err)
	}
}
