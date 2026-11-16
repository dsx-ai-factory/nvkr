// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package packer_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Copied scripts keep their repository-relative compiler pin, matching the
// production layout rather than bypassing build.sh's Go admission check.
func toolchainFixtureDir(t *testing.T, root string) string {
	t.Helper()
	pin, err := os.ReadFile("../../.go-version")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".go-version"), pin, 0600); err != nil {
		t.Fatal(err)
	}
	here := filepath.Join(root, "toolchains", "nvkr-packer")
	if err := os.MkdirAll(here, 0700); err != nil {
		t.Fatal(err)
	}
	return here
}

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
		buildExit                  string
		cleanupCommand             string
		wantExit                   int
		flags                      []string
		wantError, wantID          bool
	}{
		{name: "exact validated ID", inspected: measured, smokeExit: "0", wantID: true},
		{name: "normalize bare ID", inspected: strings.TrimPrefix(measured, "sha256:"), smokeExit: "0", wantID: true},
		{name: "native smoke failure emits no ID", inspected: measured, smokeExit: "42", wantError: true},
		{name: "failed export emits no ID", inspected: measured, smokeExit: "0", buildExit: "42", wantError: true, wantExit: 42},
		{name: "unlink failure emits no ID", inspected: measured, smokeExit: "0", cleanupCommand: "rm", wantError: true, wantExit: 1},
		{name: "directory cleanup failure emits no ID", inspected: measured, smokeExit: "0", cleanupCommand: "rmdir", wantError: true, wantExit: 1},
		{name: "export exit survives unlink failure", inspected: measured, smokeExit: "0", buildExit: "42", cleanupCommand: "rm", wantError: true, wantExit: 42},
		{name: "export exit survives directory cleanup failure", inspected: measured, smokeExit: "0", buildExit: "42", cleanupCommand: "rmdir", wantError: true, wantExit: 42},
		{name: "invalid inspected ID emits no ID", inspected: "mutable:tag", smokeExit: "0", wantError: true},
		{name: "lock check is not validation", inspected: measured, smokeExit: "0", flags: []string{"--check"}, wantError: true},
		{name: "cross mode is not native proof", inspected: measured, smokeExit: "0", flags: []string{"--cross-build-only"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			here := toolchainFixtureDir(t, dir)
			for _, name := range []string{"build.sh", "toolchain.lock.json"} {
				data, err := os.ReadFile(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(here, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			fake := filepath.Join(dir, "docker")
			log := filepath.Join(dir, "calls")
			const script = `#!/bin/sh
printf '%s\n' "$1 $2 $3" >>"$TEST_CALLS"
case "$1 $2" in
  'buildx version'|'version --format') echo 'runtime diagnostic';;
  'buildx inspect') echo 'Driver: docker-container';;
  'info --format') echo '[["Backing Filesystem","extfs"]]';;
  'buildx build') printf 'archive\n'; exit "${TEST_BUILD_EXIT:-0}";;
  'image load') [ "$3" = --input ] && [ -f "$4" ] && [ -s "$4" ] || exit 96
    cat "$4" >/dev/null; echo 'load diagnostic';;
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
			if tc.cleanupCommand != "" {
				// Inject only the owning cleanup command. This contract fixture
				// does not mutate a real runtime or claim image-build evidence.
				if err := os.WriteFile(filepath.Join(dir, tc.cleanupCommand), []byte("#!/bin/sh\nprintf 'injected cleanup failure\\n' >&2\nexit 59\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			args := append([]string{filepath.Join(here, "build.sh"), "--arch", runtime.GOARCH,
				"--diagnostic-local", "--print-validated-image-id"}, tc.flags...)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", args...)
			cmd.Env = append(os.Environ(), "CONTAINER_RUNTIME="+fake, "TEST_CALLS="+log,
				"TEST_ARCH="+runtime.GOARCH, "TEST_IMAGE_ID="+tc.inspected, "TEST_EXPECTED_ID="+measured,
				"TEST_SMOKE_EXIT="+tc.smokeExit, "TEST_BUILD_EXIT="+tc.buildExit, "NVKR_BUILD_ARCHIVES=", "NVKR_BUILD_CA_BUNDLE=",
				"TMPDIR="+dir, "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			var diagnostics bytes.Buffer
			cmd.Stderr = &diagnostics
			out, err := cmd.Output()
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v stdout=%s stderr=%s; want error=%t", err, out, diagnostics.String(), tc.wantError)
			}
			if tc.wantExit != 0 {
				var failure *exec.ExitError
				if !errors.As(err, &failure) || failure.ExitCode() != tc.wantExit {
					t.Fatalf("exit=%v; want %d; diagnostics=%s", err, tc.wantExit, diagnostics.String())
				}
			}
			if tc.cleanupCommand != "" && (!strings.Contains(diagnostics.String(), "injected cleanup failure") || strings.Contains(diagnostics.String(), "validated local toolchain")) {
				t.Fatalf("cleanup failure lost or successful validation reported: %s", diagnostics.String())
			}
			if tc.cleanupCommand != "" && tc.buildExit != "42" && !strings.Contains(diagnostics.String(), "native smoke diagnostic") {
				t.Fatalf("cleanup regression did not reach successful native smoke: %s", diagnostics.String())
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
			if tc.buildExit == "42" && strings.Contains(string(calls), "image load") {
				t.Fatalf("failed export loaded an archive: %s", calls)
			}
		})
	}
}

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
	// Makefile gate used by scripts/nvke-os-local.sh's containerized bootstrap.
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
		t.Fatalf("locked Packer compiler cannot build the NVKR bootstrap: %v:\n%s", err, output)
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
				here := filepath.Join(dir, "toolchains", "nvkr-packer")
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
					"TEST_MARKER="+marker, "NVKR_BUILD_ARCHIVES=", "NVKR_BUILD_CA_BUNDLE=")
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
		name         string
		buildExit    string
		loadExit     string
		wantError    bool
		localInputs  bool
		nightly      bool
		emptyArchive bool
		wantExit     int
	}{
		{name: "archive loaded before validation", buildExit: "0", loadExit: "0"},
		{name: "nightly Packer and plugin reach build and smoke", buildExit: "0", loadExit: "0", nightly: true},
		{name: "local archives and build CA", buildExit: "0", loadExit: "0", localInputs: true},
		{name: "partial export failure does not load or validate", buildExit: "42", loadExit: "0", wantError: true, wantExit: 42},
		{name: "empty export failure preserves primary error", buildExit: "42", loadExit: "0", emptyArchive: true, wantError: true, wantExit: 42},
		{name: "empty successful export refuses load", buildExit: "0", loadExit: "0", emptyArchive: true, wantError: true, wantExit: 1},
		{name: "load failure stops validation", buildExit: "0", loadExit: "43", wantError: true, wantExit: 43},
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
  "buildx inspect") echo 'Driver: docker-container' ;;
  "info --format") echo '[["Backing Filesystem","extfs"]]' ;;
  "buildx build")
    [[ " $* " == *" --output type=docker,dest=-,rewrite-timestamp=true "* ]]
    [[ " $* " == *" --build-arg SOURCE_DATE_EPOCH=0 "* ]]
    if [[ "${TEST_EMPTY_ARCHIVE}" != true ]]; then printf 'test-image-archive\n'; fi
    if [[ "${TEST_BUILD_EXIT}" != 0 ]]; then printf 'injected upstream build failure\n' >&2; fi
    exit "${TEST_BUILD_EXIT}"
    ;;
  "image load")
    [[ "$3" == --input && -f "$4" && ! -L "$4" && -s "$4" ]]
    test "$(cat "$4")" = test-image-archive
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
			arch := runtime.GOARCH
			args := []string{"build.sh", "--arch", arch}
			if tc.nightly {
				arch = "amd64"
				args = []string{"build.sh", "--arch", arch, "--nightly", "--diagnostic-local"}
				// The runtime is a stub; report its host architecture without
				// starting an emulated image or claiming native execution.
				if err := os.WriteFile(filepath.Join(dir, "uname"), []byte("#!/bin/sh\ntest \"$1\" = -m || exit 99\necho x86_64\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.CommandContext(ctx, "bash", args...)
			cmd.Env = append(os.Environ(), "CONTAINER_RUNTIME="+fake, "TEST_CALLS="+log,
				"TEST_BUILD_EXIT="+tc.buildExit, "TEST_LOAD_EXIT="+tc.loadExit, "TEST_ARCH="+arch,
				"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
				"NVKR_BUILD_ARCHIVES=", "NVKR_BUILD_CA_BUNDLE=", "TMPDIR="+dir,
				fmt.Sprintf("TEST_EMPTY_ARCHIVE=%t", tc.emptyArchive))
			if tc.localInputs {
				cmd.Env = append(cmd.Env, "NVKR_BUILD_ARCHIVES="+dir, "NVKR_BUILD_CA_BUNDLE="+fake)
			}
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("build error = %v, want error %v:\n%s", err, tc.wantError, output)
			}
			if tc.wantError {
				var failure *exec.ExitError
				if !errors.As(err, &failure) || failure.ExitCode() != tc.wantExit {
					t.Fatalf("primary exit status changed: %v, want %d:\n%s", err, tc.wantExit, output)
				}
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if tc.localInputs {
				for _, flag := range []string{"--build-context nvkr_archives=" + dir, "--build-arg NVKR_ARCHIVE_MODE=local", "--secret id=nvkr_ca_bundle,src=" + fake} {
					if count := strings.Count(string(calls), flag); count != 1 {
						t.Fatalf("input binding %q appears %d times, want exactly once: %s", flag, count, calls)
					}
				}
			}
			if !tc.wantError {
				packer, plugin := "1.16.2", "1.1.8"
				if tc.nightly {
					packer, plugin = "1.15.4", "1.1.6"
				}
				for _, flag := range []string{"--build-arg PACKER_VERSION=" + packer, "--build-arg QEMU_PLUGIN_VERSION=" + plugin,
					"--env EXPECTED_PACKER_VERSION=" + packer, "--env EXPECTED_QEMU_PLUGIN_VERSION=" + plugin} {
					if strings.Count(string(calls), flag) != 1 {
						t.Fatalf("Packer selection lost before build/validation: missing unique %q", flag)
					}
				}
				if tc.nightly {
					for _, digest := range []string{
						"PACKER_ARCHIVE_SHA256=15f97a6a99645c7d5308c609973b5280837b38e112beac413ccbce80da927cf1",
						"PACKER_BINARY_SHA256=6f016067b41152b9f49981727eb9f02c8dddc46cbdf5684d6f35f6c02b4958db",
						"QEMU_PLUGIN_ARCHIVE_SHA256=3f735539fbdd0368785babda272b85738866f736415dce59d04b4cb550c4db87",
						"QEMU_PLUGIN_BINARY_SHA256=4b336cdddbbc91e30eb54ad6d5ca62f02edcce70c1cf54667104d33c25c1b9b5",
					} {
						if !strings.Contains(string(calls), "--build-arg "+digest) {
							t.Fatalf("nightly version used different archive/executable bytes: %s", digest)
						}
					}
				}
			}
			loaded := strings.Contains(string(calls), "image load --input ")
			wantLoad := tc.buildExit == "0" && !tc.emptyArchive
			if loaded != wantLoad {
				t.Fatalf("archive load reached=%t want=%t:\n%s", loaded, wantLoad, calls)
			}
			validated := strings.Contains(string(calls), "image inspect ")
			if validated == tc.wantError {
				t.Fatalf("validation reached = %v, want %v:\n%s", validated, !tc.wantError, calls)
			}
			if tc.buildExit != "0" && (!strings.Contains(string(output), "injected upstream build failure") || !strings.Contains(string(output), "export failed (exit "+tc.buildExit+")")) {
				t.Fatalf("primary build diagnostics lost:\n%s", output)
			}
			if tc.wantError && strings.Contains(string(output), "validated local toolchain") {
				t.Fatalf("failed export/load reported successful native validation:\n%s", output)
			}
			remaining, err := filepath.Glob(filepath.Join(dir, "nvkr-packer-identity.*"))
			if err != nil || len(remaining) != 0 {
				t.Fatalf("private export temporary files retained: %v %v", remaining, err)
			}
		})
	}
}

func TestNightlyToolchainAdmissionBeforeRuntime(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("requires jq")
	}
	for _, tc := range []struct {
		name  string
		flags []string
		valid bool
	}{
		{"check", []string{"--arch", "amd64", "--check"}, true},
		{"requires diagnostic mode", []string{"--arch", "amd64"}, false},
		{"requires explicit arch", []string{"--diagnostic-local"}, false},
		{"refuses ARM64", []string{"--arch", "arm64", "--diagnostic-local"}, false},
		{"refuses cross build", []string{"--arch", "amd64", "--diagnostic-local", "--cross-build-only"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "called")
			fake := filepath.Join(dir, "docker")
			if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch \"$TEST_MARKER\"\nexit 99\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", append([]string{"build.sh", "--nightly"}, tc.flags...)...)
			cmd.Env = append(os.Environ(), "CONTAINER_RUNTIME="+fake, "TEST_MARKER="+marker)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.valid {
				t.Fatalf("admission: %v, want valid %t: %s", err, tc.valid, output)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("runtime reached before admission: %v", err)
			}
		})
	}
}

// This source contract proves both image stages author strict APT updates; it
// does not claim successful upstream snapshot acquisition or a real image build.
func TestToolchainAPTUpdateFailsOnAnyFetchError(t *testing.T) {
	raw, err := os.ReadFile("Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	updates := 0
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.Contains(line, "apt-get update") {
			updates++
			if !strings.Contains(line, "apt-get update --error-on=any") {
				t.Fatalf("APT stage permits degraded update: %s", line)
			}
		}
	}
	if updates != 2 {
		t.Fatalf("strict APT update boundaries=%d want=2", updates)
	}
}

func TestToolchainRejectsInvalidBuildInputsBeforeRuntime(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("requires jq")
	}
	for _, tc := range []struct{ name, runtime, key, value, message string }{
		{"missing archives", "docker", "NVKR_BUILD_ARCHIVES", "absent", "archive context is not a directory"},
		{"file archives", "docker", "NVKR_BUILD_ARCHIVES", "runtime", "archive context is not a directory"},
		{"missing CA", "docker", "NVKR_BUILD_CA_BUNDLE", "absent", "build CA bundle is not a regular file"},
		{"unsupported runtime", "podman", "NVKR_BUILD_ARCHIVES", ".", "requires Docker Buildx"},
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
			cmd.Env = append(os.Environ(), "CONTAINER_RUNTIME="+fake, "TEST_MARKER="+marker, "NVKR_BUILD_ARCHIVES=", "NVKR_BUILD_CA_BUNDLE=", tc.key+"="+value)
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
			here := toolchainFixtureDir(t, dir)
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
					lock["localDockerImages"] = json.RawMessage(`{"amd64":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","arm64":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`)
					data, err = json.Marshal(lock)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(here, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			fake := filepath.Join(dir, "docker")
			log := filepath.Join(dir, "calls")
			const script = `#!/bin/sh
printf '%s\n' "$*" >>"$TEST_CALLS"
case "$1 $2" in
  'buildx version'|'version --format') exit 0 ;;
  'buildx inspect') echo 'Driver: docker-container' ;;
  'info --format') echo '[["Backing Filesystem","extfs"]]' ;;
  'buildx build') printf 'archive\n' ;;
  'image load') [ "$3" = --input ] && [ -f "$4" ] && [ -s "$4" ] || exit 96 ;;
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
			args := append([]string{filepath.Join(here, "build.sh"), "--arch", tc.arch}, tc.flags...)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", args...)
			cmd.Env = append(os.Environ(), "CONTAINER_RUNTIME="+fake, "TEST_CALLS="+log,
				"TEST_ARCH="+tc.arch, "TEST_IMAGE_ID="+measured, "NVKR_BUILD_ARCHIVES=", "NVKR_BUILD_CA_BUNDLE=")
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

func TestToolchainRefusesDockerDriverWithoutContainerdStore(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("toolchain script requires jq")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skip("toolchain supports amd64 and arm64 hosts")
	}
	for _, tc := range []struct {
		name    string
		driver  string
		storage string
		refuse  bool
	}{
		{name: "docker driver on the classic store", driver: "docker", storage: `[["Backing Filesystem","extfs"]]`, refuse: true},
		{name: "docker driver on the containerd store", driver: "docker", storage: `[["driver-type","io.containerd.snapshotter.v1"]]`},
		{name: "container driver on the classic store", driver: "docker-container", storage: `[["Backing Filesystem","extfs"]]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			fake := filepath.Join(dir, "docker")
			// A build that is admitted fails with a distinct status, so the
			// test separates refusal from an attempted export.
			const script = `#!/bin/sh
printf '%s\n' "$*" >>"$TEST_CALLS"
case "$1 $2" in
  'buildx version'|'version --format') exit 0 ;;
  'buildx inspect') printf 'Name: test\nDriver: %s\n' "$TEST_DRIVER" ;;
  'info --format') [ "$TEST_DRIVER" = docker ] || exit 98; printf '%s\n' "$TEST_STORAGE" ;;
  'buildx build') exit 42 ;;
  *) exit 99 ;;
esac
`
			if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "build.sh", "--arch", runtime.GOARCH)
			cmd.Env = append(os.Environ(), "CONTAINER_RUNTIME="+fake, "TEST_CALLS="+log,
				"TEST_DRIVER="+tc.driver, "TEST_STORAGE="+tc.storage,
				"NVKR_BUILD_ARCHIVES=", "NVKR_BUILD_CA_BUNDLE=", "TMPDIR="+dir)
			output, err := cmd.CombinedOutput()
			var failure *exec.ExitError
			if !errors.As(err, &failure) {
				t.Fatalf("build error = %v, want an exit status:\n%s", err, output)
			}
			calls, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			built := strings.Contains(string(calls), "buildx build")
			if tc.refuse {
				if failure.ExitCode() != 1 || built || !strings.Contains(string(output), "--driver docker-container") {
					t.Fatalf("exit %d, build attempted %t, want refusal before building:\n%s", failure.ExitCode(), built, output)
				}
				return
			}
			if failure.ExitCode() != 42 || !built {
				t.Fatalf("exit %d, build attempted %t, want the admitted build's own failure:\n%s", failure.ExitCode(), built, output)
			}
		})
	}
}
