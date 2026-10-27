// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNativeSeedISOAdmitsOnlyInstalledHostAlternatives(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		tools          []string
		nonExecutable  bool
		explicitPath   bool
		accepted       bool
	}{
		{"Mac cloud-localds", "darwin", []string{"cloud-localds"}, false, true, true},
		{"Mac mkisofs", "darwin", []string{"mkisofs"}, false, true, true},
		{"Mac genisoimage", "darwin", []string{"genisoimage"}, false, true, true},
		{"Mac all absent", "darwin", nil, false, true, false},
		{"Mac nonexecutable ISO tool", "darwin", []string{"mkisofs"}, true, true, false},
		{"Mac ambient PATH cannot supply ISO tool", "darwin", []string{"mkisofs"}, false, false, false},
		{"Linux cloud-localds", "linux", []string{"cloud-localds"}, false, true, true},
		{"Linux cannot use Mac fallback", "linux", []string{"mkisofs", "genisoimage"}, false, true, false},
		{"Linux all absent", "linux", nil, false, true, false},
		{"unsupported host", "windows", []string{"cloud-localds"}, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := t.TempDir()
			mode := os.FileMode(0700)
			if tc.nonExecutable {
				mode = 0600
			}
			for _, name := range tc.tools {
				// Admission must resolve the executable without invoking it.
				if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\nexit 99\n"), mode); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", tools)
			env := []string{}
			if tc.explicitPath {
				env = []string{"PATH=" + tools}
			}
			err := admitNativeSeedISO(t.Context(), tc.platform, env)
			if tc.accepted {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability {
				t.Fatalf("missing ISO capability: %v", err)
			}
		})
	}
}

func TestNativeSeedISOCancellationPreservesTimeout(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if cause == context.DeadlineExceeded {
				ctx, cancel = context.WithDeadline(t.Context(), time.Unix(0, 0))
				defer cancel()
			}
			err := admitNativeSeedISO(ctx, "darwin", nil)
			var coded *nvkrerr.Error
			if !errors.Is(err, cause) || !errors.As(err, &coded) || coded.Code != nvkrerr.CodeTimeout {
				t.Fatalf("canceled ISO admission: %v", err)
			}
		})
	}
}

func TestMissingNativeSeedISORejectsBeforeOutputOrCommands(t *testing.T) {
	accelerator := "kvm"
	if runtime.GOOS == "darwin" {
		accelerator = "tcg"
	}
	if runtime.GOARCH != "arm64" || runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native demo requires Darwin or Linux ARM64")
	}
	for _, boundary := range []string{"native admission", "deployment", "guest", "site"} {
		t.Run(boundary, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "command-started")
			for _, name := range []string{"sudo", "sqlite3", "limactl", "nerdctl.lima", "qemu-img", "qemu-system-aarch64", "swtpm"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("#!/bin/sh\nprintf invoked > '"+marker+"'\nexit 99\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", root)
			out := filepath.Join(root, "must-not-exist")
			var err error
			switch boundary {
			case "native admission":
				err = AdmitNativeDeployment(t.Context(), accelerator)
			case "deployment":
				err = AdmitDemoDeployment(t.Context(), Config{Accelerator: accelerator, Cleanup: true, Out: out})
			case "guest":
				err = RunNativeGuest(t.Context(), NativeGuestConfig{Accelerator: accelerator, DemoExecutable: "/not-opened/nvke-demo", Out: out})
			case "site":
				err = RunNative(t.Context(), NativeConfig{Accelerator: accelerator, DemoExecutable: "/not-opened/nvke-demo", Out: out})
			}
			code := nvkrerr.CodePreflightFailed
			if boundary == "deployment" || boundary == "native admission" {
				code = nvkrerr.CodeEngineCapability
			}
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != code || !strings.Contains(err.Error(), string(nvkrerr.CodeEngineCapability)) || !strings.Contains(err.Error(), "seed ISO requires") {
				t.Fatalf("missing ISO tool reached later work: %v", err)
			}
			for _, path := range []string{out, marker} {
				if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
					t.Fatalf("ISO refusal created output or started command: %s: %v", path, statErr)
				}
			}
		})
	}
}
