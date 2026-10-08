// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package packer_test

import (
	"bytes"
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

func TestToolchainValidatedImageID(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("requires jq")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("requires a supported native architecture")
	}
	const measured = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name, inspected, smokeExit string
		flags                      []string
		wantError, wantID          bool
	}{
		{name: "exact validated ID", inspected: measured, smokeExit: "0", wantID: true},
		{name: "normalize Podman-style ID", inspected: strings.TrimPrefix(measured, "sha256:"), smokeExit: "0", wantID: true},
		{name: "native smoke failure emits no ID", inspected: measured, smokeExit: "42", wantError: true},
		{name: "invalid inspected ID emits no ID", inspected: "mutable:tag", smokeExit: "0", wantError: true},
		{name: "lock check is not validation", inspected: measured, smokeExit: "0", flags: []string{"--check"}, wantError: true},
		{name: "cross mode is not native proof", inspected: measured, smokeExit: "0", flags: []string{"--cross-build-only"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"build.sh", "toolchain.lock.json"} {
				data, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			fake := filepath.Join(dir, "docker")
			log := filepath.Join(dir, "calls")
			const script = `#!/bin/sh
printf '%s\n' "$1 $2 $3" >>"$TEST_CALLS"
case "$1 $2" in
  'buildx version'|'version --format') echo 'runtime diagnostic';;
  'buildx build') printf 'archive\n';;
  'image load') cat >/dev/null; echo 'load diagnostic';;
  'image inspect')
    [ "$3" != --format ] || exit 97
    printf '[{"Id":"%s","Os":"linux","Architecture":"%s","Config":{"Env":["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]}}]\n' "$TEST_IMAGE_ID" "$TEST_ARCH";;
  'run --rm')
    case "$*" in *"$TEST_EXPECTED_ID -ec "*) ;; *) exit 98;; esac
    echo 'native smoke diagnostic'
    exit "$TEST_SMOKE_EXIT";;
  *) exit 99;;
esac
`
			if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			args := append([]string{filepath.Join(dir, "build.sh"), "--arch", runtime.GOARCH,
				"--diagnostic-local", "--print-validated-image-id"}, tc.flags...)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", args...)
			cmd.Env = append(os.Environ(), "CONTAINER_RUNTIME="+fake, "TEST_CALLS="+log,
				"TEST_ARCH="+runtime.GOARCH, "TEST_IMAGE_ID="+tc.inspected, "TEST_EXPECTED_ID="+measured,
				"TEST_SMOKE_EXIT="+tc.smokeExit, "NKD_BUILD_ARCHIVES=", "NKD_BUILD_CA_BUNDLE=")
			var diagnostics bytes.Buffer
			cmd.Stderr = &diagnostics
			out, err := cmd.Output()
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v stdout=%s stderr=%s; want error=%t", err, out, diagnostics.String(), tc.wantError)
			}
			want := ""
			if tc.wantID {
				want = measured + "\n"
				if !strings.Contains(diagnostics.String(), "native smoke diagnostic") {
					t.Fatalf("ID returned without native smoke: %s", diagnostics.String())
				}
			}
			if string(out) != want {
				t.Fatalf("stdout=%q; want only validated ID %q", out, want)
			}
			calls, _ := os.ReadFile(log)
			if strings.Contains(string(calls), "image inspect --format") {
				t.Fatalf("identity re-resolved through mutable tag: %s", calls)
			}
			if len(tc.flags) > 0 && len(calls) > 0 {
				t.Fatalf("unsupported output mode performed runtime work: %s", calls)
			}
		})
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
    [[ "$3" != --format ]]
    printf '[{"Id":"sha256:%064d","Os":"linux","Architecture":"%s","Config":{"Env":["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]}}]\n' 1 "${TEST_ARCH}"
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

func TestDiagnosticToolchainPinDoesNotGrantMaintainedAcceptance(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("requires jq")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("requires a supported native architecture")
	}
	foreignArch := "amd64"
	if runtime.GOARCH == foreignArch {
		foreignArch = "arm64"
	}
	const measured = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tc := range []struct {
		name, arch, message string
		flags               []string
		wantRuntime         bool
		wantError           bool
	}{
		{"maintained pin mismatch fails", runtime.GOARCH, "does not match locked", nil, true, true},
		{"diagnostic pin requires native smoke", runtime.GOARCH, "validated local toolchain", []string{"--diagnostic-local"}, true, false},
		{"diagnostic cross build is not native proof", foreignArch, "requires native validation", []string{"--diagnostic-local", "--cross-build-only"}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range []string{"build.sh", "toolchain.lock.json"} {
				data, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				if name == "toolchain.lock.json" {
					var lock map[string]json.RawMessage
					if err := json.Unmarshal(data, &lock); err != nil {
						t.Fatal(err)
					}
					// Synthetic local pins exercise either host architecture without
					// rewriting the maintained source lock or adding approval claims.
					lock["localPodmanImages"] = json.RawMessage(`{"amd64":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","arm64":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)
					data, err = json.Marshal(lock)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			fake := filepath.Join(dir, "podman")
			log := filepath.Join(dir, "calls")
			const script = `#!/bin/sh
printf '%s\n' "$*" >>"$TEST_CALLS"
case "$1 $2" in
  'build --platform') exit 0 ;;
  'image inspect')
    [ "$3" != --format ] || exit 97
    printf '[{"Id":"%s","Os":"linux","Architecture":"%s","Config":{"Env":["PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"]}}]\n' "$TEST_IMAGE_ID" "$TEST_ARCH" ;;
  'run --rm') case "$*" in *"$TEST_IMAGE_ID -ec "*) exit 0;; *) exit 98;; esac ;;
  *) exit 99 ;;
esac
`
			if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			args := append([]string{filepath.Join(dir, "build.sh"), "--arch", tc.arch}, tc.flags...)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", args...)
			cmd.Env = append(os.Environ(), "CONTAINER_RUNTIME="+fake, "TEST_CALLS="+log,
				"TEST_ARCH="+tc.arch, "TEST_IMAGE_ID="+measured, "NKD_BUILD_ARCHIVES=", "NKD_BUILD_CA_BUNDLE=")
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError || !strings.Contains(string(out), tc.message) {
				t.Fatalf("error=%v output=%s; want error=%t message=%s", err, out, tc.wantError, tc.message)
			}
			calls, _ := os.ReadFile(log)
			if (len(calls) > 0) != tc.wantRuntime {
				t.Fatalf("runtime reached=%t; want %t: %s", len(calls) > 0, tc.wantRuntime, calls)
			}
			smoke := strings.Contains(string(calls), "run --rm")
			if smoke == tc.wantError {
				t.Fatalf("native smoke=%t; want %t: %s", smoke, !tc.wantError, calls)
			}
		})
	}
}
