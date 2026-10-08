// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLocalDeveloperPreflight(t *testing.T) {
	for _, tc := range []struct {
		name, system, arch, runtime, lane, state, disk, daemon, want string
		fail                                                         bool
	}{
		{"no runtime", "Linux", "aarch64", "missing", "providers", "running", "100000", "ok", "CONTAINER_RUNTIME must", true},
		{"Docker Desktop is explicit", "Darwin", "arm64", "docker", "os", "running", "100000", "ok", "Docker Desktop on macOS", true},
		{"stopped VM", "Darwin", "arm64", "podman", "providers", "stopped", "100000", "ok", "podman machine start", true},
		{"missing VM", "Darwin", "arm64", "podman", "providers", "absent", "100000", "ok", "Missing podman-machine-default", true},
		{"Mac forwards check only", "Darwin", "arm64", "podman", "providers", "running", "100000", "ok", "--check providers", false},
		{"foreign Linux", "Linux", "x86_64", "docker", "providers", "running", "100000", "ok", "AMD64 emulation is not admitted", true},
		{"capacity fails before pull", "Linux", "aarch64", "podman", "os", "running", "38000", "ok", "No downloads started", true},
		{"Docker daemon unavailable", "Linux", "aarch64", "docker", "providers", "running", "100000", "failed", "daemon is unreachable", true},
		{"Docker providers without Go", "Linux", "aarch64", "docker", "providers", "running", "100000", "ok", "Prerequisite diagnostics passed", false},
		{"remote context overrides local host", "Linux", "aarch64", "docker", "providers", "running", "100000", "remote", "Remote Docker endpoints", true},
		{"OS diagnostics without cached Packer", "Linux", "aarch64", "docker", "os", "running", "100000", "ok", "Prerequisite diagnostics passed", false},
		{"mutable bootstrap pin rejected", "Linux", "aarch64", "docker", "providers", "running", "100000", "ok", "admitted bootstrap image/platform/runtime", true},
		{"foreign bootstrap platform rejected", "Linux", "aarch64", "docker", "providers", "running", "100000", "ok", "admitted bootstrap image/platform/runtime", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			tools := filepath.Join(dir, "tools")
			if err := os.Mkdir(tools, 0700); err != nil {
				t.Fatal(err)
			}
			// Copy the real launcher into an isolated repository so any accidental
			// attempt creation can be detected without touching a developer's Run.
			if err := os.Mkdir(filepath.Join(dir, "scripts"), 0700); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile("nke-full-arm64-local.sh")
			if err != nil {
				t.Fatal(err)
			}
			launcher := filepath.Join(dir, "scripts", "nke-full-arm64-local.sh")
			if err := os.WriteFile(launcher, data, 0600); err != nil {
				t.Fatal(err)
			}
			inputs := filepath.Join(dir, "definitions", "nke-laptop-arm64")
			if err := os.MkdirAll(inputs, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"environment-local.json", "environment-local-docker.json"} {
				env, err := os.ReadFile(filepath.Join("..", "definitions", "nke-laptop-arm64", name))
				if err != nil {
					t.Fatal(err)
				}
				switch tc.name {
				case "mutable bootstrap pin rejected":
					env = []byte(strings.ReplaceAll(string(env), "golang@sha256:6c2a5538f964f1c82f97ad14988bf05de100d922d159d0e398b54c7b0ca0c6c9", "golang:latest"))
				case "foreign bootstrap platform rejected":
					env = []byte(strings.ReplaceAll(string(env), "linux/arm64", "linux/amd64"))
				}
				if err := os.WriteFile(filepath.Join(inputs, name), env, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, name := range []string{"bash", "dirname", "git", "jq", "timeout", "awk"} {
				path, err := exec.LookPath(name)
				if err != nil {
					t.Fatalf("test prerequisite %s: %v", name, err)
				}
				if err := os.Symlink(path, filepath.Join(tools, name)); err != nil {
					t.Fatal(err)
				}
			}
			for name, body := range map[string]string{
				"uname":   `if [ "$1" = -s ]; then echo "$TEST_SYSTEM"; else echo "$TEST_ARCH"; fi`,
				"id":      "echo 1000",
				"getconf": "echo 8",
				"df":      "printf 'Filesystem blocks used available capacity mounted\nmock 200000 10000 %s 5%% /\n' \"$TEST_DISK\"",
				"podman": `printf '%s\n' "$*" >>"$TEST_CALLS"
case "$1 $2" in
  'machine inspect') [ "$TEST_STATE" != absent ] || exit 1; case "$*" in *--format*) echo "$TEST_STATE";; esac ;;
  'machine ssh') printf '%s\n' "$*" ;;
  'version --format') [ "$TEST_DAEMON" != failed ] ;;
  *) exit 99 ;;
esac`,
				"docker": `printf '%s\n' "$*" >>"$TEST_CALLS"
case "$1 $2" in
  'version --format') [ "$TEST_DAEMON" != failed ] ;;
  'info --format') echo linux/arm64 ;;
  'context inspect') if [ "$TEST_DAEMON" = remote ]; then echo ssh://remote; else echo unix:///var/run/docker.sock; fi ;;
  'buildx version') exit 0 ;;
  *) exit 99 ;;
esac`,
			} {
				if err := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			// Supply a bounded fixture for Linux memory without reading macOS /proc.
			awk, _ := exec.LookPath("awk")
			if err := os.Remove(filepath.Join(tools, "awk")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tools, "awk"), []byte("#!/bin/sh\ncase \"$*\" in */proc/meminfo*) echo 16000;; *) exec \""+awk+"\" \"$@\";; esac\n"), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, filepath.Join(tools, "bash"), launcher, "--check", tc.lane)
			cmd.Env = append(os.Environ(), "PATH="+tools, "CONTAINER_RUNTIME="+tc.runtime, "NKE_LOCAL_CLONE=", "DOCKER_HOST=", "DOCKER_CONTEXT=",
				"TEST_SYSTEM="+tc.system, "TEST_ARCH="+tc.arch, "TEST_STATE="+tc.state, "TEST_DISK="+tc.disk, "TEST_DAEMON="+tc.daemon,
				"TEST_CALLS="+filepath.Join(dir, "calls"))
			if tc.daemon == "remote" {
				cmd.Env = append(cmd.Env, "DOCKER_CONTEXT=remote-arm64", "DOCKER_HOST=unix:///var/run/docker.sock")
			}
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail || !strings.Contains(string(out), tc.want) {
				t.Fatalf("error=%v output=%s, want fail=%t message=%s", err, out, tc.fail, tc.want)
			}
			if _, err := os.Stat(filepath.Join(dir, ".nkd")); !os.IsNotExist(err) {
				t.Fatalf("check created attempt storage: %v", err)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			for _, forbidden := range []string{"pull ", "run ", "buildx build", "image inspect", "machine init", "machine start"} {
				if strings.Contains(string(calls), forbidden) {
					t.Fatalf("check performed mutation/producer work %q: %s", forbidden, calls)
				}
			}
		})
	}
}
