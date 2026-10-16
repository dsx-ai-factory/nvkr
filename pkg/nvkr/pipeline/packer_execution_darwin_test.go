// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package pipeline

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func TestDarwinPackerAcceleratorAdmission(t *testing.T) {
	other := "amd64"
	if runtime.GOARCH == "amd64" {
		other = "arm64"
	}
	for _, test := range []struct {
		name, architecture, accelerator string
		want                            e.Code
	}{
		{"native HVF", runtime.GOARCH, "hvf", ""},
		{"native TCG", runtime.GOARCH, "tcg", ""},
		{"foreign TCG", other, "tcg", ""},
		{"foreign HVF", other, "hvf", e.CodeEngineCapability},
		{"KVM rejected", runtime.GOARCH, "kvm", e.CodeEngineCapability},
		{"unknown rejected", runtime.GOARCH, "automatic", e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := (darwinPackerExecution{}).accelerator(test.architecture, test.accelerator); code(err) != test.want {
				t.Fatalf("accelerator admission = %v, want %s", err, test.want)
			}
		})
	}
}

func TestDarwinPackerProjectDirectoryRejectsReplacement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	handle, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	class := darwinPackerExecution{}
	if _, err := class.projectDirectory(path, handle); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, path+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := class.projectDirectory(path, handle); code(err) != e.CodeUnverifiedRef {
		t.Fatalf("replaced project = %v, want %s", err, e.CodeUnverifiedRef)
	}
}

func TestDarwinPackerExecutableSnapshotAndCleanup(t *testing.T) {
	path, digest := writeFakePacker(t)
	class := darwinPackerExecution{}
	executable, err := class.open(t.Context(), path, heldfile.Options{
		Label: "packer-test", Executable: true, MaxBytes: maxPackerExecutableBytes, ExpectedDigest: digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()
	privatePath, err := class.hostToolPath(0, executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed original"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := executable.AssertUnchangedContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := executable.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(privatePath); !os.IsNotExist(err) {
		t.Fatalf("private executable remains: %v", err)
	}
	if err := executable.AssertUnchangedContext(t.Context()); code(err) != e.CodeEngineCapability {
		t.Fatalf("closed executable = %v", err)
	}
}

func TestDarwinPackerRealPinnedExecutable(t *testing.T) {
	path, digest := os.Getenv("NVKR_TEST_NATIVE_PACKER"), os.Getenv("NVKR_TEST_NATIVE_PACKER_SHA256")
	if path == "" && digest == "" {
		t.Skip("optional acquired native Packer execution")
	}
	if path == "" || digest == "" {
		t.Fatal("both native Packer path and SHA-256 pin are required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	toolchain, err := OpenPackerToolchain(ctx, path, digest, nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer toolchain.Close()
	var stdout bytes.Buffer
	if err := runPackerCommand(ctx, toolchain, t.TempDir(),
		[]string{"CHECKPOINT_DISABLE=1", "PATH=/nonexistent", "LANG=C", "LC_ALL=C", "HOME=" + t.TempDir(), "TMPDIR=" + os.TempDir(), "PACKER_NO_COLOR=1"},
		[]string{"version"}, &stdout, &stdout, "checking acquired native Packer"); err != nil {
		t.Fatalf("native Packer failed: %v; output: %s", err, stdout.String())
	}
	if !strings.HasPrefix(stdout.String(), "Packer v") {
		t.Fatalf("unexpected Packer version: %q", stdout.String())
	}
	t.Logf("native Packer execution through shared toolchain: %s", strings.TrimSpace(stdout.String()))
}

func TestDarwinPackerProtectedSystemToolsExecuteWithPinnedBytes(t *testing.T) {
	for _, row := range []struct {
		path string
		args []string
	}{
		{"/usr/bin/tar", []string{"--version"}},
		{"/usr/bin/gzip", []string{"--version"}},
		{"/usr/bin/grep", []string{"-q", "pinned"}},
		{"/usr/bin/sed", []string{"-n", "1p"}},
	} {
		t.Run(filepath.Base(row.path), func(t *testing.T) {
			resolved, err := filepath.EvalSymlinks(row.path)
			if err != nil {
				t.Fatal(err)
			}
			row.path = resolved
			raw, err := os.ReadFile(row.path)
			if err != nil {
				t.Fatal(err)
			}
			class := darwinPackerExecution{}
			executable, err := class.open(t.Context(), row.path, heldfile.Options{
				Label: "system-tool-test", ExpectedDigest: FileDigest(raw), MaxBytes: maxPackerHostToolBytes,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer executable.Close()
			path, err := class.hostToolPath(0, executable)
			if err != nil || path != row.path {
				t.Fatalf("system tool relocated: %q %v", path, err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var output bytes.Buffer
			if err := class.run(ctx, executable, nil, row.args, heldfile.RunOptions{
				Dir: t.TempDir(), Env: []string{"PATH=/nonexistent"},
				Stdin: strings.NewReader("pinned\n"), Stdout: &output, Stderr: &output,
			}); err != nil {
				t.Fatalf("protected tool failed: %v; %s", err, output.String())
			}
			if _, err := class.open(ctx, row.path, heldfile.Options{
				Label: "wrong-pin", ExpectedDigest: "sha256:" + strings.Repeat("f", 64), MaxBytes: maxPackerHostToolBytes,
			}); err == nil {
				t.Fatal("system tool admitted with wrong bytes")
			}
		})
	}
	for _, path := range []string{"/usr/local/bin/tool", "/tmp/tool", "/usr/bin/../bin/sed", "/usr/bin/subdirectory/tool"} {
		if err := admitPackerSystemTool(path); code(err) != e.CodeEngineCapability {
			t.Fatalf("unprotected path admitted: %q %v", path, err)
		}
	}
}
