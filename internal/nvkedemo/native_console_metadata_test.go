// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTenantConsoleMetadataUsesOnlySelectedBackendLayout(t *testing.T) {
	for _, backend := range []string{"libvirt", "darwin-qemu"} {
		for _, unsafe := range []bool{false, true} {
			name := backend
			if unsafe {
				name += "-symlink"
			}
			t.Run(name, func(t *testing.T) {
				root := privateTestDir(t)
				dir := filepath.Join(root, "instances", "instance")
				if backend == "libvirt" {
					dir = filepath.Join(root, "var", "instances", "instance")
				}
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				member := "console.log"
				if backend == "libvirt" {
					member = "disk.qcow2.ttyS0.log"
				}
				console := filepath.Join(dir, member)
				if unsafe {
					if err := os.Symlink("/etc/passwd", console); err != nil {
						t.Fatal(err)
					}
				} else {
					writeTenantFixture(t, console, []byte("private console bytes"), 0600)
				}
				files, err := tenantConsoleMetadataForBackend(t.Context(), root, "instance", backend)
				if (err == nil) == unsafe {
					t.Fatal("backend layout weakened safe metadata admission", err)
				}
				if !unsafe && (files[0] == nil || files[0].Size() != 21) {
					t.Fatal("selected backend's actual console metadata missing", files)
				}
				if _, err := tenantConsoleMetadataForBackend(t.Context(), root, "instance", "foreign-backend"); err == nil {
					t.Fatal("unselected console layout admitted")
				}
			})
		}
	}
}
