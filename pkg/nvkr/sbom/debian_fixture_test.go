// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func debianMaterialOperationFixture(t *testing.T, withPayload bool) (*heldfile.File, Scanner, string) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("held package-reader execution requires Linux")
	}
	toolBytes, err := os.ReadFile("/usr/bin/dpkg-deb")
	if os.IsNotExist(err) {
		t.Skip("existing package reader is unavailable")
	}
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	root := filepath.Join(workspace, "package")
	control := filepath.Join(root, "DEBIAN")
	if err := os.MkdirAll(control, 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := "Package: nvkr-test-metapackage\nVersion: 1:2.3-4\nArchitecture: all\nMaintainer: NVKR Test <test@example.invalid>\nDescription: controlled empty payload fixture\n"
	if err := os.WriteFile(filepath.Join(control, "control"), []byte(metadata), 0o644); err != nil {
		t.Fatal(err)
	}
	if withPayload {
		if err := os.MkdirAll(filepath.Join(root, "usr", "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "usr", "bin", "tool"), []byte("contained file"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	filename := filepath.Join(workspace, "package.deb")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/dpkg-deb", "--build", root, filename)
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C", "SOURCE_DATE_EPOCH=0"}
	if diagnostic, err := command.CombinedOutput(); err != nil {
		t.Fatalf("existing dpkg builder failed: %v: %s", err, diagnostic)
	}
	subject, err := heldfile.OpenContext(ctx, filename, heldfile.Options{Label: "control-only-debian-test", MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = subject.Close() })
	scannerPath := filepath.Join(workspace, "scanner")
	script := []byte("#!/bin/sh\nprintf '%s' '{\"schema\":{\"version\":\"16.1.10\"},\"artifacts\":[]}'\n")
	if err := os.WriteFile(scannerPath, script, 0o755); err != nil {
		t.Fatal(err)
	}
	return subject, Scanner{Path: scannerPath, Digest: rawDigest(script)}, rawDigest(toolBytes)
}
