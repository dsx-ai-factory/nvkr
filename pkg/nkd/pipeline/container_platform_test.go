// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestContainerPlatformAdmission(t *testing.T) {
	image := "example.test/tool@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, raw, platform string
		code                e.Code
	}{
		{"amd64", `{"Os":"linux","Architecture":"amd64","RepoDigests":["` + image + `"]}`, "linux/amd64", ""},
		{"arm64", `{"Os":"linux","Architecture":"arm64","RepoDigests":["` + image + `"]}`, "linux/arm64", ""},
		{"wrong architecture", `{"Os":"linux","Architecture":"amd64","RepoDigests":["` + image + `"]}`, "linux/arm64", e.CodeEngineCapability},
		{"wrong OS", `{"Os":"darwin","Architecture":"arm64","RepoDigests":["` + image + `"]}`, "linux/arm64", e.CodeEngineCapability},
		{"wrong identity", `{"Os":"linux","Architecture":"arm64","RepoDigests":["example.test/tool@sha256:` + strings.Repeat("b", 64) + `"]}`, "linux/arm64", e.CodePreflightFailed},
		{"missing identity", `{"Os":"linux","Architecture":"arm64"}`, "linux/arm64", e.CodePreflightFailed},
		{"duplicate architecture", `{"Os":"linux","Architecture":"amd64","Architecture":"arm64","RepoDigests":["` + image + `"]}`, "linux/arm64", e.CodeEngineCapability},
		{"malformed", `{`, "linux/arm64", e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkContainerPlatform([]byte(tc.raw), image, tc.platform)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
}

func TestContainerDaemonPlatformAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"Docker ARM64", `{"OSType":"linux","Architecture":"aarch64"}`, true},
		{"Podman ARM64", `{"host":{"os":"linux","arch":"arm64"}}`, true},
		{"foreign daemon", `{"OSType":"linux","Architecture":"x86_64"}`, false},
		{"missing", `{}`, false},
		{"duplicate daemon architecture", `{"OSType":"linux","Architecture":"amd64","Architecture":"arm64"}`, false},
		{"duplicate host architecture", `{"host":{"os":"linux","arch":"amd64","arch":"arm64"}}`, false},
		{"partial mixed fields", `{"OSType":"linux","host":{"arch":"arm64"}}`, false},
		{"ambiguous", `{"OSType":"linux","Architecture":"arm64","host":{"os":"linux","arch":"arm64"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkContainerDaemonPlatform([]byte(tc.raw), "linux/arm64")
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
				t.Fatalf("want capability error, got %v", err)
			}
		})
	}
}

// This opt-in records real runtime admission separately from contract fixtures.
func TestContainerPlatformRealRuntime(t *testing.T) {
	image := os.Getenv("NKD_TEST_NATIVE_IMAGE")
	if image == "" {
		t.Skip("set NKD_TEST_NATIVE_IMAGE and NKD_TEST_NATIVE_PLATFORM for real runtime evidence")
	}
	platform := os.Getenv("NKD_TEST_NATIVE_PLATFORM")
	if err := CheckContainerPlatform(t.Context(), "docker", image, platform); err != nil {
		t.Fatal(err)
	}
	foreign := "linux/amd64"
	if platform == foreign {
		foreign = "linux/arm64"
	}
	var coded *e.Error
	if err := CheckContainerPlatform(t.Context(), "docker", image, foreign); !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("foreign daemon admission: %v", err)
	}
}

func TestContainerAdmissionChecksDaemonBeforePull(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell protocol fixture requires Unix")
	}
	image := "example.test/tool@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, platform, daemon, wantCalls string
		code                              e.Code
	}{
		{"native amd64", "linux/amd64", "amd64", "info\npull\nimage\n", ""},
		{"native arm64", "linux/arm64", "arm64", "info\npull\nimage\n", ""},
		{"foreign", "linux/arm64", "amd64", "info\n", e.CodeEngineCapability},
		{"unsupported", "linux/riscv64", "arm64", "", e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			runtimePath := filepath.Join(dir, "docker")
			t.Setenv("NKD_TEST_CALLS", log)
			script := "#!/bin/sh\nprintf '%s\n' \"$1\" >> \"$NKD_TEST_CALLS\"\ncase \"$1\" in\ninfo) printf '%s\n' '{\"OSType\":\"linux\",\"Architecture\":\"" + tc.daemon + "\"}';;\npull) exit 0;;\nimage) printf '%s\n' '{\"Os\":\"linux\",\"Architecture\":\"" + tc.daemon + "\",\"RepoDigests\":[\"" + image + "\"]}';;\nesac\n"
			if err := os.WriteFile(runtimePath, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			err := CheckContainerPlatform(t.Context(), runtimePath, image, tc.platform)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.code {
					t.Fatalf("want %s, got %v", tc.code, err)
				}
			}
			calls, readErr := os.ReadFile(log)
			if readErr != nil && !os.IsNotExist(readErr) {
				t.Fatal(readErr)
			}
			if string(calls) != tc.wantCalls {
				t.Fatalf("calls %q, want %q", calls, tc.wantCalls)
			}
		})
	}
}

func TestContainerPlatformAdmissionCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := CheckContainerPlatform(ctx, "docker", "example.test/tool@sha256:"+strings.Repeat("a", 64), "linux/arm64")
	if code(err) != e.CodeTimeout {
		t.Fatalf("canceled admission: %v", err)
	}
}
