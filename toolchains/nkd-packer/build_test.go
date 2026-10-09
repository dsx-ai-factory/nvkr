// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package packer_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestToolchainGoMatchesBootstrap(t *testing.T) {
	raw, err := os.ReadFile("toolchain.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock struct {
		Go struct{ Version string } `json:"go"`
	}
	if err := json.Unmarshal(raw, &lock); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	// Replay the compiler version selected by the image against the same
	// Makefile gate used by scripts/nke-os-local.sh's containerized bootstrap.
	const goProbe = `#!/bin/sh
test "$*" = 'env GOVERSION' || exit 99
printf 'go%s\n' "$TEST_GO_VERSION"
`
	if err := os.WriteFile(filepath.Join(dir, "go"), []byte(goProbe), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "make", "-C", "../..", "go-version-check")
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GOTOOLCHAIN=local", "TEST_GO_VERSION="+lock.Go.Version)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("locked Packer compiler cannot build the NKD bootstrap: %v:\n%s", err, output)
	}
}

func TestToolchainRejectsBootstrapGoDriftBeforeRuntime(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatal("toolchain script requires jq")
	}
	script, err := os.ReadFile("build.sh")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile("toolchain.lock.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, checkOnly := range []bool{false, true} {
		for _, tc := range []struct{ name, pin string }{
			{name: "mismatched", pin: "0.0.0\n"},
			{name: "empty", pin: "\n"},
			{name: "missing"},
		} {
			name := tc.name
			if checkOnly {
				name += "/check"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				here := filepath.Join(dir, "toolchains", "nkd-packer")
				if err := os.MkdirAll(here, 0o700); err != nil {
					t.Fatal(err)
				}
				files := map[string][]byte{
					filepath.Join(here, "build.sh"):            script,
					filepath.Join(here, "toolchain.lock.json"): lock,
					filepath.Join(dir, "docker"):               []byte("#!/bin/sh\ntouch \"$TEST_MARKER\"\nexit 99\n"),
				}
				if tc.pin != "" {
					files[filepath.Join(dir, ".go-version")] = []byte(tc.pin)
				}
				for path, data := range files {
					if err := os.WriteFile(path, data, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				marker := filepath.Join(dir, "called")
				args := []string{filepath.Join(here, "build.sh"), "--arch", runtime.GOARCH}
				if checkOnly {
					args = append(args, "--check")
				}
				ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, "bash", args...)
				cmd.Env = append(os.Environ(), "CONTAINER_RUNTIME="+filepath.Join(dir, "docker"),
					"TEST_MARKER="+marker, "NKD_BUILD_ARCHIVES=", "NKD_BUILD_CA_BUNDLE=")
				output, err := cmd.CombinedOutput()
				if err == nil || !strings.Contains(string(output), ".go-version") {
					t.Fatalf("bootstrap Go drift was not diagnosed: %v:\n%s", err, output)
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatalf("runtime reached before Go pin admission: %v", err)
				}
			})
		}
	}
}

func TestToolchainDockerExportAndLoad(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("toolchain script requires jq")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("toolchain supports amd64 and arm64 hosts")
	}
	for _, tc := range []struct {
		name        string
		buildExit   string
		loadExit    string
		wantError   bool
		localInputs bool
	}{
		{name: "archive loaded before validation", buildExit: "0", loadExit: "0"},
		{name: "local archives and build CA", buildExit: "0", loadExit: "0", localInputs: true},
		{name: "export failure stops validation", buildExit: "42", loadExit: "0", wantError: true},
		{name: "load failure stops validation", buildExit: "0", loadExit: "43", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			fake := filepath.Join(dir, "docker")
			const script = `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >>"${TEST_CALLS}"
case "$1 $2" in
  "buildx version"|"version --format") exit 0 ;;
  "buildx build")
    [[ " $* " == *" --output type=docker,dest=-,rewrite-timestamp=true "* ]]
    [[ " $* " == *" --build-arg SOURCE_DATE_EPOCH=0 "* ]]
    printf 'test-image-archive\n'
    exit "${TEST_BUILD_EXIT}"
    ;;
  "image load")
    test "$(cat)" = test-image-archive
    exit "${TEST_LOAD_EXIT}"
    ;;
  "image inspect")
    if [[ "$3" == --format ]]; then
      printf 'sha256:%064d\n' 1
    else
      printf '[{"Os":"linux","Architecture":"%s","Config":{"Env":["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]}}]\n' "${TEST_ARCH}"
    fi
    ;;
  "run --rm") exit 0 ;;
  *) echo "unexpected runtime command: $*" >&2; exit 99 ;;
esac
`
			if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "build.sh", "--arch", runtime.GOARCH)
			cmd.Env = append(os.Environ(), "CONTAINER_RUNTIME="+fake, "TEST_CALLS="+log,
				"TEST_BUILD_EXIT="+tc.buildExit, "TEST_LOAD_EXIT="+tc.loadExit, "TEST_ARCH="+runtime.GOARCH,
				"NKD_BUILD_ARCHIVES=", "NKD_BUILD_CA_BUNDLE=")
			if tc.localInputs {
				cmd.Env = append(cmd.Env, "NKD_BUILD_ARCHIVES="+dir, "NKD_BUILD_CA_BUNDLE="+fake)
			}
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("build error = %v, want error %v:\n%s", err, tc.wantError, output)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if tc.localInputs {
				for _, flag := range []string{"--build-context nkd_archives=" + dir, "--build-arg NKD_ARCHIVE_MODE=local", "--secret id=nkd_ca_bundle,src=" + fake} {
					if count := strings.Count(string(calls), flag); count != 1 {
						t.Fatalf("input binding %q appears %d times, want exactly once: %s", flag, count, calls)
					}
				}
			}
			if !strings.Contains(string(calls), "image load\n") {
				t.Fatalf("archive was not explicitly loaded:\n%s", calls)
			}
			validated := strings.Contains(string(calls), "image inspect ")
			if validated == tc.wantError {
				t.Fatalf("validation reached = %v, want %v:\n%s", validated, !tc.wantError, calls)
			}
		})
	}
}

func TestToolchainRejectsInvalidBuildInputsBeforeRuntime(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("requires jq")
	}
	for _, tc := range []struct{ name, runtime, key, value, message string }{
		{"missing archives", "docker", "NKD_BUILD_ARCHIVES", "absent", "archive context is not a directory"},
		{"file archives", "docker", "NKD_BUILD_ARCHIVES", "runtime", "archive context is not a directory"},
		{"missing CA", "docker", "NKD_BUILD_CA_BUNDLE", "absent", "build CA bundle is not a regular file"},
		{"unsupported runtime", "podman", "NKD_BUILD_ARCHIVES", ".", "require Docker Buildx"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "called")
			fake := filepath.Join(dir, tc.runtime)
			if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch \"$TEST_MARKER\"\nexit 99\n"), 0700); err != nil {
				t.Fatal(err)
			}
			value := filepath.Join(dir, tc.value)
			if tc.value == "runtime" {
				value = fake
			}
			cmd := exec.CommandContext(t.Context(), "bash", "build.sh", "--arch", runtime.GOARCH)
			cmd.Env = append(os.Environ(), "CONTAINER_RUNTIME="+fake, "TEST_MARKER="+marker, "NKD_BUILD_ARCHIVES=", "NKD_BUILD_CA_BUNDLE=", tc.key+"="+value)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.message) {
				t.Fatalf("got %v: %s", err, out)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("runtime reached before input admission: %v", err)
			}
		})
	}
}
