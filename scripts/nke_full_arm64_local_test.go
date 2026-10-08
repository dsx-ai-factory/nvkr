// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// The fixture models a Linux launcher; the host gate also admits macOS gtimeout.
func linkLauncherTestTimeout(t *testing.T, tools string) {
	t.Helper()
	path, err := exec.LookPath("timeout")
	if err != nil {
		path, err = exec.LookPath("gtimeout")
	}
	if err != nil {
		t.Fatalf("test prerequisite timeout or gtimeout: %v", err)
	}
	if err := os.Symlink(path, filepath.Join(tools, "timeout")); err != nil {
		t.Fatal(err)
	}
}

func TestLauncherTimeoutHostNames(t *testing.T) {
	for _, name := range []string{"timeout", "gtimeout"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			host, tools := filepath.Join(dir, "host"), filepath.Join(dir, "tools")
			for _, path := range []string{host, tools} {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			binary := filepath.Join(host, name)
			if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", host)
			linkLauncherTestTimeout(t, tools)
			target, err := os.Readlink(filepath.Join(tools, "timeout"))
			if err != nil || target != binary {
				t.Fatalf("timeout target=%q error=%v; want %q", target, err, binary)
			}
		})
	}
}

func TestLocalDeveloperPreflight(t *testing.T) {
	for _, tc := range []struct {
		name, system, arch, runtime, lane, state, disk, daemon, want string
		fail                                                         bool
	}{
		{"no runtime", "Linux", "aarch64", "missing", "providers", "running", "100000", "ok", "CONTAINER_RUNTIME must", true},
		{"Docker Desktop is explicit", "Darwin", "arm64", "docker", "os", "running", "100000", "ok", "Docker Desktop on macOS", true},
		{"Colima Mac forwards check only", "Darwin", "arm64", "docker", "providers", "running", "100000", "ok", "--check providers", false},
		{"Colima without Mac Docker CLI", "Darwin", "arm64", "docker", "providers", "running", "100000", "ok", "--check providers", false},
		{"Colima OS rejects shared workspace", "Darwin", "arm64", "docker", "os", "running", "100000", "ok", "native ext4", true},
		{"Colima all rejects shared workspace", "Darwin", "arm64", "docker", "all", "running", "100000", "ok", "native ext4", true},
		{"Colima missing CLI", "Darwin", "arm64", "docker", "os", "running", "100000", "ok", "Missing Colima", true},
		{"Colima stopped profile", "Darwin", "arm64", "docker", "os", "stopped", "100000", "ok", "absent or stopped", true},
		{"Colima absent profile", "Darwin", "arm64", "docker", "os", "absent", "100000", "ok", "absent or stopped", true},
		{"Colima invalid profile", "Darwin", "arm64", "docker", "os", "running", "100000", "ok", "simple profile name", true},
		{"stopped VM", "Darwin", "arm64", "podman", "providers", "stopped", "100000", "ok", "podman machine start", true},
		{"missing VM", "Darwin", "arm64", "podman", "providers", "absent", "100000", "ok", "Missing podman-machine-default", true},
		{"Mac forwards check only", "Darwin", "arm64", "podman", "providers", "running", "100000", "ok", "--check providers", false},
		{"foreign Linux", "Linux", "x86_64", "docker", "providers", "running", "100000", "ok", "AMD64 emulation is not admitted", true},
		{"capacity fails before pull", "Linux", "aarch64", "podman", "os", "running", "38000", "ok", "No downloads started", true},
		{"Docker daemon unavailable", "Linux", "aarch64", "docker", "providers", "running", "100000", "failed", "daemon is unreachable", true},
		{"Docker providers without Go", "Linux", "aarch64", "docker", "providers", "running", "100000", "ok", "Prerequisite diagnostics passed", false},
		{"default check uses components", "Linux", "aarch64", "docker", "", "running", "100000", "ok", "Prerequisite diagnostics passed for components/docker", false},
		{"relative clone rejected", "Linux", "aarch64", "docker", "providers", "running", "100000", "ok", "NKE_LOCAL_CLONE must be an absolute path", true},
		{"Docker site without OS capacity", "Linux", "aarch64", "docker", "site", "running", "32000", "ok", "Prerequisite diagnostics passed for site/docker", false},
		{"Podman site without OS capacity", "Linux", "aarch64", "podman", "site", "running", "32000", "ok", "Prerequisite diagnostics passed for site/podman", false},
		{"Docker tools without OS capacity", "Linux", "aarch64", "docker", "tools", "running", "32000", "ok", "Prerequisite diagnostics passed for tools/docker", false},
		{"Podman tools without OS capacity", "Linux", "aarch64", "podman", "tools", "running", "32000", "ok", "Prerequisite diagnostics passed for tools/podman", false},
		{"Colima tools forwards check only", "Darwin", "arm64", "docker", "tools", "running", "32000", "ok", "--check tools", false},
		{"Docker profiles without OS capacity", "Linux", "aarch64", "docker", "profiles", "running", "32000", "ok", "Prerequisite diagnostics passed for profiles/docker", false},
		{"Podman profiles without OS capacity", "Linux", "aarch64", "podman", "profiles", "running", "32000", "ok", "Prerequisite diagnostics passed for profiles/podman", false},
		{"Colima profiles forwards check only", "Darwin", "arm64", "docker", "profiles", "running", "32000", "ok", "--check profiles", false},
		{"simulator without OS capacity", "Linux", "aarch64", "podman", "simulator", "running", "32000", "ok", "Prerequisite diagnostics passed for simulator/podman", false},
		{"simulator without cached toolchain", "Linux", "aarch64", "podman", "simulator", "running", "32000", "missing-toolchain", "Prerequisite diagnostics passed", false},
		{"simulator Docker without OS capacity", "Linux", "aarch64", "docker", "simulator", "running", "32000", "ok", "Prerequisite diagnostics passed for simulator/docker", false},
		{"simulator Docker requires Buildx", "Linux", "aarch64", "docker", "simulator", "running", "32000", "missing-buildx", "Buildx plugin", true},
		{"site Docker requires Buildx", "Linux", "aarch64", "docker", "site", "running", "32000", "missing-buildx", "Buildx plugin", true},
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
			inputs := filepath.Join(dir, "definitions", "nke-local-dev-arm64")
			if err := os.MkdirAll(inputs, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"environment-local.json", "environment-local-docker.json"} {
				env, err := os.ReadFile(filepath.Join("..", "definitions", "nke-local-dev-arm64", name))
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
			toolchainDir := filepath.Join(dir, "toolchains", "nkd-packer")
			if err := os.MkdirAll(toolchainDir, 0700); err != nil {
				t.Fatal(err)
			}
			lock, err := os.ReadFile(filepath.Join("..", "toolchains", "nkd-packer", "toolchain.lock.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(toolchainDir, "toolchain.lock.json"), lock, 0600); err != nil {
				t.Fatal(err)
			}
			linkLauncherTestTimeout(t, tools)
			for _, name := range []string{"bash", "dirname", "git", "jq", "awk"} {
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
  'image inspect') [ "$TEST_DAEMON" != missing-toolchain ] ;;
  *) exit 99 ;;
esac`,
				"docker": `printf '%s\n' "$*" >>"$TEST_CALLS"
case "$1 $2" in
  'version --format') [ "$TEST_DAEMON" != failed ] ;;
  'info --format') echo linux/arm64 ;;
  'context inspect') if [ "$TEST_DAEMON" = remote ]; then echo ssh://remote; else echo unix:///var/run/docker.sock; fi ;;
  'buildx version') [ "$TEST_DAEMON" != missing-buildx ] ;;
  *) exit 99 ;;
esac`,
				"colima": `printf '%s\n' "$*" >>"$TEST_CALLS"
case "$1 $2" in
  'status --profile') [ "$TEST_STATE" = running ] ;;
  'ssh --profile') printf '%s\n' "$*" ;;
  *) exit 99 ;;
esac`,
			} {
				if tc.name == "Colima without Mac Docker CLI" && name == "docker" || tc.name == "Colima missing CLI" && name == "colima" {
					continue
				}
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
			cmd.Env = append(os.Environ(), "PATH="+tools, "CONTAINER_RUNTIME="+tc.runtime, "NKE_LOCAL_CLONE=", "DOCKER_HOST=", "DOCKER_CONTEXT=", "COLIMA_PROFILE=",
				"TEST_SYSTEM="+tc.system, "TEST_ARCH="+tc.arch, "TEST_STATE="+tc.state, "TEST_DISK="+tc.disk, "TEST_DAEMON="+tc.daemon,
				"TEST_CALLS="+filepath.Join(dir, "calls"))
			if tc.name == "relative clone rejected" {
				cmd.Env = append(cmd.Env, "NKE_LOCAL_CLONE=../nke")
			}
			if strings.HasPrefix(tc.name, "Colima ") {
				profile := "nkd-demo"
				if tc.name == "Colima invalid profile" {
					profile = "../default"
				}
				cmd.Env = append(cmd.Env, "COLIMA_PROFILE="+profile)
			}
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
			for _, forbidden := range []string{"pull ", "run ", "buildx build", "machine init", "machine start", "start --", "context use"} {
				if strings.Contains(string(calls), forbidden) {
					t.Fatalf("check performed mutation/producer work %q: %s", forbidden, calls)
				}
			}
			if tc.name == "Colima Mac forwards check only" {
				for _, required := range []string{"ssh --profile nkd-demo -- bash -c", "env -u DOCKER_HOST -u DOCKER_CONTEXT CONTAINER_RUNTIME=docker", "--check providers"} {
					if !strings.Contains(string(calls), required) {
						t.Fatalf("missing explicit VM forwarding %q: %s", required, calls)
					}
				}
			}
			if strings.Contains(tc.name, "rejects shared workspace") && strings.Contains(string(calls), "ssh --profile") {
				t.Fatalf("unsupported shared workspace forwarded execution: %s", calls)
			}
			if strings.Contains(string(calls), "image inspect") {
				t.Fatalf("diagnostic preflight inspected a cached toolchain: %s", calls)
			}
		})
	}
}

func TestSimulatorToolchainScope(t *testing.T) {
	for _, tc := range []struct {
		name, components, entries, builds, want string
		fail                                    bool
	}{
		{"sorted selected producers only", `["nico-simctl","nico-sim"]`, `"nico-sim":{"fromSource":true,"toolchainImages":{"arm64":"old"}},"nico-simctl":{"fromSource":true,"toolchainImages":{"arm64":"old"}},"unrelated":{"fromSource":true,"toolchainImages":{"arm64":"other"}}`, "", "old", false},
		{"runtime assets share the selected toolchain", `["nico-simctl","nico-sim-runtime-assets","nico-sim"]`, `"nico-sim":{"fromSource":true,"toolchainImages":{"arm64":"old"}},"nico-simctl":{"fromSource":true,"toolchainImages":{"arm64":"old"}},"nico-sim-runtime-assets":{"fromSource":true,"toolchainImages":{"arm64":"old"}}`, "", "old", false},
		{"empty selection", `[]`, ``, "", "expected one simulator source toolchain", true},
		{"missing producer", `["nico-sim"]`, ``, "", "lacks a source toolchain", true},
		{"acquired producer", `["nico-sim"]`, `"nico-sim":{"fromSource":false,"toolchainImages":{"arm64":"old"}}`, "", "lacks a source toolchain", true},
		{"missing native pin", `["nico-sim"]`, `"nico-sim":{"fromSource":true,"toolchainImages":{"amd64":"old"}}`, "", "lacks a source toolchain", true},
		{"mixed toolchains", `["nico-sim","nico-simctl"]`, `"nico-sim":{"fromSource":true,"toolchainImages":{"arm64":"old"}},"nico-simctl":{"fromSource":true,"toolchainImages":{"arm64":"other"}}`, "", "expected one simulator source toolchain", true},
		{"no selected build", `[]`, ``, `[]`, "expected one simulator tools build", true},
		{"ambiguous build", `[]`, ``, `[{"name":"nicosim-tools-arm64"},{"name":"nicosim-tools-arm64"}]`, "expected one simulator tools build", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			builds := tc.builds
			if builds == "" {
				builds = `[{"name":"nicosim-tools-arm64","catalogDigest":"catalog","components":` + tc.components + `,"catalogSnapshot":{"Components":{` + tc.entries + `}}}]`
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "jq", "-L", ".", "-e", `include "nke-os-plan"; simulator_toolchain_scope | toolchain_overlay("arm64"; .pin; "fresh"; .catalogDigest; .components)`)
			cmd.Stdin = strings.NewReader(`{"builds":` + builds + `}`)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail || !strings.Contains(string(out), tc.want) {
				t.Fatalf("error=%v output=%s, want fail=%t message=%s", err, out, tc.fail, tc.want)
			}
			if tc.fail {
				return
			}
			var overlay struct {
				Catalog string `json:"requiresCatalogDigest"`
				Subs    []struct {
					Arch, From, To string
					Components     []string
				} `json:"toolchainSubstitutions"`
			}
			if err := json.Unmarshal(out, &overlay); err != nil {
				t.Fatal(err)
			}
			var selected []string
			if err := json.Unmarshal([]byte(tc.components), &selected); err != nil {
				t.Fatal(err)
			}
			slices.Sort(selected)
			if overlay.Catalog != "catalog" || len(overlay.Subs) != 1 || overlay.Subs[0].Arch != "arm64" || overlay.Subs[0].From != "old" || overlay.Subs[0].To != "fresh" || !slices.Equal(overlay.Subs[0].Components, selected) {
				t.Fatalf("unexpected substitution: %+v", overlay)
			}
		})
	}
}

func TestSiteHelmToolchainScope(t *testing.T) {
	const plan = `{"builds":[{"name":"controller-chart","catalogDigest":"catalog","components":["nke-controller-chart"],"catalogSnapshot":{"Components":{"nke-controller-chart":{"fromSource":true,"toolchainImages":{"arm64":"old"}}}}},{"name":"nktp","catalogDigest":"catalog","components":["nktp-chart"],"catalogSnapshot":{"Components":{"nktp-chart":{"fromSource":true,"toolchainImages":{"arm64":"old"}}}}},{"name":"cwe","catalogDigest":"other","components":["nke-controller"]}]}`
	for _, tc := range []struct {
		name, input, want string
		fail              bool
	}{
		{"only the two charts", plan, "old", false},
		{"missing charts", `{"builds":[]}`, "expected two site chart builds", true},
		{"different catalogs", strings.Replace(plan, `"name":"nktp","catalogDigest":"catalog"`, `"name":"nktp","catalogDigest":"other"`, 1), "site charts must share one catalog", true},
		{"different toolchains", strings.Replace(plan, `"nktp-chart":{"fromSource":true,"toolchainImages":{"arm64":"old"}}`, `"nktp-chart":{"fromSource":true,"toolchainImages":{"arm64":"different"}}`, 1), "expected one site Helm toolchain", true},
		{"acquired chart", strings.Replace(plan, `"nktp-chart":{"fromSource":true`, `"nktp-chart":{"fromSource":false`, 1), "site chart lacks a source toolchain", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "jq", "-L", ".", "-e", `include "nke-os-plan"; site_helm_toolchain_scope | toolchain_overlay("arm64"; .pin; "fresh"; .catalogDigest; .components)`)
			cmd.Stdin = strings.NewReader(tc.input)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail || !strings.Contains(string(out), tc.want) {
				t.Fatalf("error=%v output=%s, want fail=%t message=%s", err, out, tc.fail, tc.want)
			}
			if tc.fail {
				return
			}
			var overlay struct {
				Catalog string `json:"requiresCatalogDigest"`
				Subs    []struct {
					Arch, From, To string
					Components     []string
				} `json:"toolchainSubstitutions"`
			}
			if err := json.Unmarshal(out, &overlay); err != nil {
				t.Fatal(err)
			}
			if overlay.Catalog != "catalog" || len(overlay.Subs) != 1 || overlay.Subs[0].Arch != "arm64" || overlay.Subs[0].From != "old" || overlay.Subs[0].To != "fresh" || !slices.Equal(overlay.Subs[0].Components, []string{"nke-controller-chart", "nktp-chart"}) {
				t.Fatalf("unexpected site substitution: %+v", overlay)
			}
		})
	}
}

func TestSimulatorScopeRejectsBeforeToolchainWork(t *testing.T) {
	for _, tc := range []struct{ name, plan, want string }{
		{"unsupported build", `{"builds":[]}`, "expected one simulator tools build"},
		{"mixed pins", `{"builds":[{"name":"nicosim-tools-arm64","components":["nico-sim","nico-simctl"],"catalogSnapshot":{"Components":{"nico-sim":{"fromSource":true,"toolchainImages":{"arm64":"old"}},"nico-simctl":{"fromSource":true,"toolchainImages":{"arm64":"different"}}}}}]}`, "expected one simulator source toolchain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name string, data []byte, mode os.FileMode) {
				t.Helper()
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, mode); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{
				"scripts/nke-full-arm64-local.sh", "scripts/nke-os-plan.jq",
				"definitions/nke-local-dev-arm64/environment-local-docker.json",
				"toolchains/nkd-packer/toolchain.lock.json",
			} {
				data, err := os.ReadFile(filepath.Join("..", path))
				if err != nil {
					t.Fatal(err)
				}
				write(path, data, 0600)
			}
			write("toolchains/nkd-packer/build.sh", []byte("#!/bin/sh\nprintf 'unexpected toolchain build\\n' >>\"$TEST_CALLS\"\nexit 99\n"), 0700)
			write("tools/runner", []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$TEST_CALLS\"\n[ \"$1 $2\" = 'run plan' ] || exit 99\nprintf '%s\\n' \"$TEST_PLAN\"\n"), 0700)
			write("tools/uname", []byte("#!/bin/sh\nif [ \"$1\" = -s ]; then echo Linux; else echo aarch64; fi\n"), 0700)
			write("tools/id", []byte("#!/bin/sh\necho 1000\n"), 0700)
			write("tools/docker", []byte(`#!/bin/sh
printf '%s\n' "$*" >>"$TEST_CALLS"
case "$1 $2" in
  'version --format'|'buildx version') exit 0 ;;
  'info --format') echo linux/arm64 ;;
  'context inspect') echo unix:///var/run/docker.sock ;;
  'pull --platform') exit 0 ;;
  'run --rm') cp "$TEST_REPO/tools/runner" "$TEST_REPO/.nkd/laptop-bootstrap/scope-probe/nkdctl" ;;
  *) exit 99 ;;
esac
`), 0700)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			linkLauncherTestTimeout(t, filepath.Join(dir, "tools"))
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(dir, "scripts", "nke-full-arm64-local.sh"), "scope-probe", "1", "simulator")
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"CONTAINER_RUNTIME=docker", "NKE_LOCAL_CLONE=", "DOCKER_HOST=", "DOCKER_CONTEXT=", "COLIMA_PROFILE=",
				"TEST_REPO="+dir, "TEST_PLAN="+tc.plan, "TEST_CALLS="+filepath.Join(dir, "calls"))
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("error=%v output=%s, want scope rejection %s", err, out, tc.want)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(calls), "run plan") {
				t.Fatalf("never reached baseline admission: %s", calls)
			}
			for _, forbidden := range []string{"unexpected toolchain build", "image inspect", "run local"} {
				if strings.Contains(string(calls), forbidden) {
					t.Fatalf("scope rejection performed later work %q: %s", forbidden, calls)
				}
			}
		})
	}
}

func TestSimulatorLauncherUsesValidatedToolchainID(t *testing.T) {
	const validated = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const plan = `{"builds":[{"name":"nicosim-tools-arm64","catalogDigest":"catalog","components":["nico-sim"],"catalogSnapshot":{"Components":{"nico-sim":{"fromSource":true,"toolchainImages":{"arm64":"old"}}}}}]}`
	for _, tc := range []struct {
		name, returned, buildExit string
		wantError                 bool
	}{
		{"freeze validated ID without resolving tag", validated, "0", false},
		{"reject empty result", "", "0", true},
		{"reject result mixed with diagnostics", "diagnostic\n" + validated, "0", true},
		{"failed validation cannot proceed", validated, "42", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write := func(name string, data []byte, mode os.FileMode) {
				t.Helper()
				path := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, mode); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{"scripts/nke-full-arm64-local.sh", "scripts/nke-os-plan.jq",
				"definitions/nke-local-dev-arm64/environment-local-docker.json", "toolchains/nkd-packer/toolchain.lock.json"} {
				data, err := os.ReadFile(filepath.Join("..", path))
				if err != nil {
					t.Fatal(err)
				}
				write(path, data, 0600)
			}
			write("definitions/nke-local-dev-arm64/definition-simulator.json", []byte(`{"catalog":{"base":"simulator-catalog"},"outputs":[]}`), 0600)
			write("toolchains/nkd-packer/build.sh", []byte(`#!/bin/sh
printf 'toolchain-build %s\n' "$*" >>"$TEST_CALLS"
case " $* " in *' --print-validated-image-id '*) ;; *) exit 99;; esac
printf '%s\n' "$TEST_RETURNED"
exit "$TEST_BUILD_EXIT"
`), 0700)
			write("tools/runner", []byte(`#!/bin/sh
printf '%s\n' "$*" >>"$TEST_CALLS"
case "$1 $2" in
  'run plan') printf '%s\n' "$TEST_PLAN";;
  'run local') jq -e --arg id "$TEST_VALIDATED" '.toolchainSubstitutions[0].to == $id' "$TEST_REPO/.nkd/laptop-bootstrap/id-probe/toolchain-attempt.json";;
  *) exit 99;;
esac
`), 0700)
			write("tools/uname", []byte("#!/bin/sh\nif [ \"$1\" = -s ]; then echo Linux; else echo aarch64; fi\n"), 0700)
			write("tools/id", []byte("#!/bin/sh\necho 1000\n"), 0700)
			write("tools/docker", []byte(`#!/bin/sh
printf '%s\n' "$*" >>"$TEST_CALLS"
case "$1 $2" in
  'version --format'|'buildx version'|'pull --platform') exit 0;;
  'info --format') echo linux/arm64;;
  'context inspect') echo unix:///var/run/docker.sock;;
  'run --rm') cp "$TEST_REPO/tools/runner" "$TEST_REPO/.nkd/laptop-bootstrap/id-probe/nkdctl";;
  'image inspect') echo 'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb';;
  *) exit 99;;
esac
`), 0700)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			linkLauncherTestTimeout(t, filepath.Join(dir, "tools"))
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(dir, "scripts/nke-full-arm64-local.sh"), "id-probe", "1", "simulator")
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"CONTAINER_RUNTIME=docker", "NKE_LOCAL_CLONE=", "DOCKER_HOST=", "DOCKER_CONTEXT=", "COLIMA_PROFILE=",
				"TEST_REPO="+dir, "TEST_PLAN="+plan, "TEST_RETURNED="+tc.returned, "TEST_BUILD_EXIT="+tc.buildExit,
				"TEST_VALIDATED="+validated, "TEST_CALLS="+filepath.Join(dir, "calls"))
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v output=%s, want failure=%t", err, out, tc.wantError)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(calls), "image inspect") {
				t.Fatalf("launcher resolved validated bytes through a mutable tag: %s", calls)
			}
			if reached := strings.Contains(string(calls), "run local"); reached == tc.wantError {
				t.Fatalf("local execution reached=%t; want %t: %s", reached, !tc.wantError, calls)
			}
		})
	}
}
