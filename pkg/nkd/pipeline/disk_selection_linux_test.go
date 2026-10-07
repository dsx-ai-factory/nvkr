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

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Exercise the real accessor through its held executable, runtime identity,
// image revalidation and lifetime. The controlled ELF is not a Docker daemon.
func TestDiskInspectionSelectionProjectsAuthenticatedLogicalRuntime(t *testing.T) {
	for _, name := range []string{"docker", "podman"} {
		for _, mode := range []string{"unchanged", "closed", "runtime identity", "daemon", "image"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
				defer cancel()
				root := t.TempDir()
				path, _ := copyTestExecutable(t, name)
				held, err := snapshotRuntimeExecutableContext(ctx, path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = held.close() })
				held.environment = append(held.environment, "NKD_DISK_INSPECTION_FIXTURE_ROOT="+root,
					"NKD_DISK_INSPECTION_FIXTURE_MODE="+mode, "NKD_DISK_INSPECTION_FIXTURE_RUNTIME="+name)
				held.identity, err = observeHeldRuntimeIdentity(ctx, held)
				if err != nil {
					t.Fatal(err)
				}
				selection := testDiskInspectionSelection(t)
				selection.Platform = "linux/" + runtime.GOARCH
				selection.Image = "example.test/inspection@sha256:" + strings.Repeat("1", 64)
				selection.RuntimeExecutable = "/caller/private/path/" + name
				selection.compositionKind, selection.consumerPlatform, selection.scratchVolume = "disk-image", "linux/amd64", "private-volume-name"
				container := &DiskInspectionContainer{runtime: held, selection: selection}
				t.Cleanup(func() { _ = container.Close() })
				if err := container.observeBinding(ctx); err != nil {
					t.Fatal(err)
				}
				want := e.Code("")
				switch mode {
				case "closed":
					if err := container.Close(); err != nil {
						t.Fatal(err)
					}
					want = e.CodeEngineCapability
				case "runtime identity":
					held.identity = "sha256:" + strings.Repeat("f", 64)
					want = e.CodeEngineCapability
				case "daemon", "image":
					if err := os.WriteFile(filepath.Join(root, "start-complete"), nil, 0o600); err != nil {
						t.Fatal(err)
					}
					want = e.CodeEngineCapability
					if mode == "image" {
						want = e.CodeVerifyFailed
					}
				}
				got, err := container.SelectionContext(ctx)
				if code(err) != want {
					t.Fatalf("selection=%+v error=%v want=%s", got, err, want)
				}
				if want != "" {
					if got != (DiskInspectionSelection{}) {
						t.Fatal("failed owner disclosed a selection")
					}
					return
				}
				expected := selection
				expected.Workspace, expected.RuntimeExecutable = nil, name
				expected.compositionKind, expected.consumerPlatform, expected.scratchVolume = "", "", ""
				if got != expected {
					t.Fatalf("logical runtime/profile roundtrip leaked or substituted owner state: got=%+v want=%+v", got, expected)
				}
				got.RuntimeExecutable = "caller-modification"
				again, err := container.SelectionContext(ctx)
				if err != nil || again != expected {
					t.Fatalf("description mutated owner: %+v %v", again, err)
				}
			})
		}
	}
}
