// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func TestDarwinPackerPrivateTemporarySocketLifecycle(t *testing.T) {
	path, digest := writeFakePacker(t)
	class := darwinPackerExecution{}
	executable, err := class.open(t.Context(), path, heldfile.Options{
		Label: "socket-test", ExpectedDigest: digest, MaxBytes: maxPackerExecutableBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer executable.Close()
	for _, outcome := range []string{"success", "failure", "cancellation"} {
		t.Run(outcome, func(t *testing.T) {
			workspace := t.TempDir()
			longTemporary := filepath.Join(workspace, strings.Repeat("nested-", 20))
			if err := os.Mkdir(longTemporary, 0o700); err != nil {
				t.Fatal(err)
			}
			// Reproduce the real plugin's socket operation at the caller's path.
			if listener, err := net.Listen("unix", filepath.Join(longTemporary, "packer-plugin1234567890")); err == nil {
				_ = listener.Close()
				t.Fatal("fixture path does not exceed the host socket limit")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			var stdout, stderr bytes.Buffer
			err := class.run(ctx, executable, nil, []string{outcome}, heldfile.RunOptions{
				Dir: workspace, Env: []string{"PATH=/nonexistent", "NVKR_PACKER_SOCKET_PROBE=1", "TMPDIR=" + longTemporary},
				Stdout: &stdout, Stderr: &stderr,
			})
			if (err == nil) != (outcome == "success") || stderr.Len() != 0 {
				t.Fatalf("socket child outcome: %v; stderr: %s", err, stderr.String())
			}
			if outcome == "cancellation" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("canceled child returned %v", err)
			}
			temporary, permissions, found := strings.Cut(strings.TrimSpace(stdout.String()), "\t")
			mode, modeErr := strconv.ParseUint(permissions, 8, 32)
			if !found || modeErr != nil || mode != 0o700 || !strings.HasPrefix(temporary, "/private/tmp/nvkr-pkr-") {
				t.Fatalf("child did not use a short private directory: %q", stdout.String())
			}
			if _, err := os.Lstat(temporary); !os.IsNotExist(err) {
				t.Fatalf("child temporary directory remains after join: %v", err)
			}
			if _, err := os.Stat(longTemporary); err != nil {
				t.Fatalf("caller workspace was changed: %v", err)
			}
		})
	}
}

func TestDarwinPackerQEMUDataSnapshotAndCleanup(t *testing.T) {
	program, programDigest := writeFakePacker(t)
	qemu, qemuDigest := writeFakePackerHostTool(t)
	for _, outcome := range []string{"source changes isolated", "private changes rejected", "executable data rejected"} {
		t.Run(outcome, func(t *testing.T) {
			source := t.TempDir()
			relative := "nvkr/toolchain/qemu-data/keymaps/en-us"
			path := filepath.Join(source, relative)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			raw := []byte("pinned keymap bytes\n")
			executableData := outcome == "executable data rejected"
			if executableData {
				var err error
				raw, err = os.ReadFile(program)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			chain, err := OpenPackerToolchain(t.Context(), program, programDigest,
				[]PackerHostTool{{Name: "qemu-system-aarch64", Path: qemu, SHA256: qemuDigest}}, source,
				[]PackerPluginFile{{Path: relative, SHA256: FileDigest(raw), Executable: executableData}})
			if executableData {
				if code(err) != e.CodeInvalidCatalog || chain != nil {
					t.Fatalf("executable runtime data accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer chain.Close()
			native := chain.hostTools[0].file.(*darwinPackerExecutable)
			root := native.root
			data := filepath.Join(root, "share/qemu/keymaps/en-us")
			if actual, err := os.ReadFile(data); err != nil || !bytes.Equal(actual, raw) {
				t.Fatalf("private QEMU data differs: %v", err)
			}
			if err := os.WriteFile(path, []byte("source changed"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := verifyPackerToolchain(t.Context(), chain); err != nil {
				t.Fatalf("source change affected captured QEMU data: %v", err)
			}
			if outcome == "private changes rejected" {
				if err := os.Chmod(data, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(data, []byte("tampered"), 0o600); err != nil {
					t.Fatal(err)
				}
				if code(verifyPackerToolchain(t.Context(), chain)) != e.CodeUnverifiedRef {
					t.Fatal("modified private QEMU data accepted")
				}
			}
			if err := chain.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(root); !os.IsNotExist(err) {
				t.Fatalf("private QEMU prefix remains after close: %v", err)
			}
		})
	}
}

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
