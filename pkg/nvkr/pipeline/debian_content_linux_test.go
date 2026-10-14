// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
)

// A controlled runtime ELF exercises held snapshots, capacity and cleanup. Its
// report is a fixture, not a real dpkg-deb/Syft inventory or daemon acceptance.
func TestDebianContentOwnsSnapshotCapacityAndCleanup(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		want       e.Code
	}{
		{"unchanged", "unchanged", ""},
		{"changed runtime", "daemon", e.CodeEngineCapability},
		{"snapshot substitution", "unchanged", e.CodeVerifyFailed},
		{"caller path replacement", "subject", ""},
		{"capacity before path", "unchanged", e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			container, root := newReservedDiskInspectionFixture(t, ctx, tc.mode)
			container.selection.GuestfishDigest = "sha256:" + strings.Repeat("d", 64)
			container.selection.ScannerDigest = "sha256:" + strings.Repeat("e", 64)
			container.selection.PackageReaderDigest = "sha256:" + strings.Repeat("f", 64)
			container.selection.MaxFilesystemBytes = 1 << 20
			container.selection.ScratchStorage = "memory"
			container.selection.Resources.MemoryMiB = 8192
			container.selection.Resources.EphemeralStorageMiB = 8192
			// Prescribed host facts isolate the reservation contract; these are
			// not observations of production capacity or a supported tool profile.
			container.observeHost = func(context.Context, *os.File) (execution.ObservedHostFacts, error) {
				facts := execution.ObservedHostFacts{Platform: execution.HostPlatform(container.selection.Platform), Resources: execution.ResourceCapacity{
					CPUMillis: 1000, MemoryMiB: 9216, EphemeralStorageMiB: 8192,
				}}
				if tc.name == "capacity before path" {
					facts.Resources.MemoryMiB = 0
				}
				return facts, nil
			}
			raw := []byte("controlled captured package bytes")
			digest := FileDigest(raw)
			path := filepath.Join(t.TempDir(), "package-without-extension")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			expected := digest
			if tc.name == "snapshot substitution" {
				expected = "sha256:" + strings.Repeat("b", 64)
			}
			if tc.name == "capacity before path" {
				path += "-absent"
			}
			consumer := "linux/arm64"
			if runtime.GOARCH == "arm64" {
				consumer = "linux/amd64"
			}
			var result *DiskInspectionObservation
			var inspectErr error
			done := make(chan struct{})
			go func() {
				result, inspectErr = container.InspectDebianPackageContent(ctx, path, expected, int64(len(raw)), consumer)
				close(done)
			}()
			defer func() { cancel(); <-done }()
			if tc.name == "caller path replacement" {
				waitDiskInspectionFixtureMarker(t, ctx, done, root, "consumed")
				if err := os.WriteFile(path, []byte("replacement bytes"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "release"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("owned Debian inspection did not join before its deadline")
			}
			if code(inspectErr) != tc.want || (result != nil) != (tc.want == "") {
				t.Fatalf("inspection = %v, %v; want %s", result, inspectErr, tc.want)
			}
			if tc.name == "capacity before path" && !strings.Contains(inspectErr.Error(), "host memory capacity") {
				t.Fatalf("capacity admission did not precede path work: %v", inspectErr)
			}
			if tc.name == "snapshot substitution" || tc.name == "capacity before path" {
				if _, err := os.Stat(filepath.Join(root, "consumed")); !os.IsNotExist(err) {
					t.Fatalf("failed admission/capture reached the child: %v", err)
				}
			} else if _, err := os.Stat(filepath.Join(root, "removed")); err != nil {
				t.Fatalf("inspection returned before container cleanup: %v", err)
			}
			if !diskInspectionCapacity.TryLock() {
				t.Fatal("owned Debian inspection leaked its capacity permit")
			}
			diskInspectionCapacity.Unlock()
			if result != nil {
				actual, size, _, _, _ := result.Binding()
				kind, _, _, _, platform, _ := result.ContentBinding()
				if actual != digest || size != int64(len(raw)) || kind != "host-package" || platform != consumer {
					t.Fatal("observation substituted package bytes or inspection-host architecture")
				}
			}
		})
	}
}
