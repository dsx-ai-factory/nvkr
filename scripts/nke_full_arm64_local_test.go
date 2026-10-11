// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"bytes"
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

func TestBuildNetworkSelectionFailsBeforeProducerWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing mode", []string{"--network"}, "Usage:"},
		{"unknown mode", []string{"--network", "guessed", "--check", "all"}, "Build network must"},
		{"Calico is not an artifact selection", []string{"--network", "kind-calico", "--check", "all"}, "Build network must"},
		{"upstream subset", []string{"--network", "upstream-ovn", "--check", "profiles"}, "requires the complete all selection"},
		{"upstream implicit subset", []string{"--network", "upstream-ovn", "fresh"}, "requires the complete all selection"},
		{"private upstream subset", []string{"--prepare-only", "--network", "upstream-ovn", "fresh", "1", "os"}, "requires the complete all selection"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "scripts"), 0700); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile("nke-full-arm64-local.sh")
			if err != nil {
				t.Fatal(err)
			}
			launcher := filepath.Join(dir, "scripts", "nke-full-arm64-local.sh")
			if err := os.WriteFile(launcher, body, 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", append([]string{launcher}, tc.args...)...)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("error=%v output=%s, want %s", err, out, tc.want)
			}
			if _, err := os.Stat(filepath.Join(dir, ".nkd")); !os.IsNotExist(err) {
				t.Fatalf("rejected mode created attempt: %v", err)
			}
		})
	}
}

func TestLocalDeveloperPreflight(t *testing.T) {
	for _, tc := range []struct {
		name, system, arch, runtime, lane, state, disk, daemon, want string
		fail                                                         bool
	}{
		{"unsupported runtime", "Linux", "aarch64", "podman", "providers", "running", "100000", "ok", "CONTAINER_RUNTIME must", true},
		{"Docker Desktop is explicit", "Darwin", "arm64", "docker", "os", "running", "100000", "ok", "Docker Desktop on macOS", true},
		{"Colima Mac forwards check only", "Darwin", "arm64", "docker", "providers", "running", "100000", "ok", "--check providers", false},
		{"Colima without Mac Docker CLI", "Darwin", "arm64", "docker", "providers", "running", "100000", "ok", "--check providers", false},
		{"Colima OS rejects shared workspace", "Darwin", "arm64", "docker", "os", "running", "100000", "ok", "native ext4", true},
		{"Colima all rejects shared workspace", "Darwin", "arm64", "docker", "all", "running", "100000", "ok", "native ext4", true},
		{"Colima missing CLI", "Darwin", "arm64", "docker", "os", "running", "100000", "ok", "Missing Colima", true},
		{"Colima stopped profile", "Darwin", "arm64", "docker", "os", "stopped", "100000", "ok", "absent or stopped", true},
		{"Colima absent profile", "Darwin", "arm64", "docker", "os", "absent", "100000", "ok", "absent or stopped", true},
		{"Colima invalid profile", "Darwin", "arm64", "docker", "os", "running", "100000", "ok", "simple profile name", true},
		{"foreign Linux", "Linux", "x86_64", "docker", "providers", "running", "100000", "ok", "AMD64 emulation is not admitted", true},
		{"capacity fails before pull", "Linux", "aarch64", "docker", "os", "running", "38000", "ok", "No downloads started", true},
		{"Docker daemon unavailable", "Linux", "aarch64", "docker", "providers", "running", "100000", "failed", "daemon is unreachable", true},
		{"Docker providers without Go", "Linux", "aarch64", "docker", "providers", "running", "100000", "ok", "Prerequisite diagnostics passed", false},
		{"default check uses components and Docker", "Linux", "aarch64", "", "", "running", "100000", "ok", "Prerequisite diagnostics passed for components/docker", false},
		{"relative clone rejected", "Linux", "aarch64", "docker", "providers", "running", "100000", "ok", "NKE_LOCAL_CLONE must be an absolute path", true},
		{"Docker site without OS capacity", "Linux", "aarch64", "docker", "site", "running", "32000", "ok", "Prerequisite diagnostics passed for site/docker", false},
		{"Docker tools without OS capacity", "Linux", "aarch64", "docker", "tools", "running", "32000", "ok", "Prerequisite diagnostics passed for tools/docker", false},
		{"Colima tools forwards check only", "Darwin", "arm64", "docker", "tools", "running", "32000", "ok", "--check tools", false},
		{"Docker profiles without OS capacity", "Linux", "aarch64", "docker", "profiles", "running", "32000", "ok", "Prerequisite diagnostics passed for profiles/docker", false},
		{"Colima profiles forwards check only", "Darwin", "arm64", "docker", "profiles", "running", "32000", "ok", "--check profiles", false},
		{"simulator without cached toolchain", "Linux", "aarch64", "docker", "simulator", "running", "32000", "missing-toolchain", "Prerequisite diagnostics passed", false},
		{"simulator Docker without OS capacity", "Linux", "aarch64", "docker", "simulator", "running", "32000", "ok", "Prerequisite diagnostics passed for simulator/docker", false},
		{"simulator Docker requires Buildx", "Linux", "aarch64", "docker", "simulator", "running", "32000", "missing-buildx", "Buildx plugin", true},
		{"site Docker requires Buildx", "Linux", "aarch64", "docker", "site", "running", "32000", "missing-buildx", "Buildx plugin", true},
		{"OS Docker requires Buildx", "Linux", "aarch64", "docker", "os", "running", "100000", "missing-buildx", "Buildx plugin", true},
		{"all Docker requires Buildx", "Linux", "aarch64", "docker", "all", "running", "100000", "missing-buildx", "Buildx plugin", true},
		{"remote context overrides local host", "Linux", "aarch64", "docker", "providers", "running", "100000", "remote", "Remote Docker endpoints", true},
		{"OS diagnostics without cached Packer", "Linux", "aarch64", "docker", "os", "running", "100000", "ok", "Prerequisite diagnostics passed", false},
		{"mutable bootstrap pin rejected", "Linux", "aarch64", "docker", "providers", "running", "100000", "ok", "admitted bootstrap image/platform/runtime", true},
		{"foreign bootstrap platform rejected", "Linux", "aarch64", "docker", "providers", "running", "100000", "ok", "admitted bootstrap image/platform/runtime", true},
		{"invalid Git count", "Linux", "aarch64", "docker", "providers", "running", "100000", "ok", "GIT_CONFIG_COUNT must", true},
		{"overflowing Git count", "Linux", "aarch64", "docker", "providers", "running", "100000", "ok", "GIT_CONFIG_COUNT must", true},
		{"no room for local clone redirects", "Linux", "aarch64", "docker", "providers", "running", "100000", "ok", "two free Git configuration entries", true},
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
			env, err := os.ReadFile(filepath.Join("..", "definitions", "nke-local-dev-arm64", "environment-local-docker.json"))
			if err != nil {
				t.Fatal(err)
			}
			switch tc.name {
			case "mutable bootstrap pin rejected":
				env = []byte(strings.ReplaceAll(string(env), "golang@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c", "golang:latest"))
			case "foreign bootstrap platform rejected":
				env = []byte(strings.ReplaceAll(string(env), "linux/arm64", "linux/amd64"))
			}
			if err := os.WriteFile(filepath.Join(inputs, "environment-local-docker.json"), env, 0600); err != nil {
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
			switch tc.name {
			case "invalid Git count":
				cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=not-a-count")
			case "overflowing Git count":
				cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=999999999999999999999999999999")
			case "no room for local clone redirects":
				cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=15", "NKE_LOCAL_CLONE=/must-not-be-inspected")
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
			if strings.Contains(string(out), "nkdctl command:") {
				t.Fatal("prerequisite check logged an nkdctl invocation")
			}
			if tc.daemon == "missing-buildx" {
				for _, required := range []string{"sudo apt-get install docker-buildx", "sudo apt-get install docker-buildx-plugin", "docker buildx version", "No downloads or attempt creation"} {
					if !strings.Contains(string(out), required) {
						t.Fatalf("missing Buildx recovery instruction %q: %s", required, out)
					}
				}
			}
			if _, err := os.Stat(filepath.Join(dir, ".nkd")); !os.IsNotExist(err) {
				t.Fatalf("check created attempt storage: %v", err)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if strings.Contains(tc.name, "Git count") || tc.name == "no room for local clone redirects" {
				if len(calls) != 0 {
					t.Fatalf("invalid Git configuration reached host or producer work: %s", calls)
				}
			}
			for _, forbidden := range []string{"pull ", "run ", "buildx build", "start --", "context use"} {
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

func TestAllLocalToolchainScopes(t *testing.T) {
	const plan = `{"builds":[
      {"name":"nke-node-image-arm64","catalogDigest":"os","components":["nke-node-image","nke-agent"],"executionGraph":{"nodes":[{"kind":"build","builderType":"vm-image","component":"nke-node-image"}]},"catalogSnapshot":{"Components":{"nke-node-image":{"fromSource":true,"toolchainImages":{"arm64":"packer"}},"nke-agent":{"fromSource":true,"toolchainImages":{"arm64":"go"}}}}},
      {"name":"nicosim-tools-arm64","catalogDigest":"simulator","components":["nico-sim"],"executionGraph":{"nodes":[]},"catalogSnapshot":{"Components":{"nico-sim":{"fromSource":true,"toolchainImages":{"arm64":"gcc"}}}}},
      {"name":"controller-chart","catalogDigest":"site","components":["nke-controller-chart"],"executionGraph":{"nodes":[]},"catalogSnapshot":{"Components":{"nke-controller-chart":{"fromSource":true,"toolchainImages":{"arm64":"helm"}}}}},
      {"name":"nktp","catalogDigest":"site","components":["nktp-chart"],"executionGraph":{"nodes":[]},"catalogSnapshot":{"Components":{"nktp-chart":{"fromSource":true,"toolchainImages":{"arm64":"helm"}}}}},
      {"name":"cwe","catalogDigest":"site","executionGraph":{"nodes":[]}}
    ]}`
	for _, tc := range []struct {
		name, input, want string
		fail              bool
	}{
		{"all three catalog predecessors", plan, "", false},
		{"overlapping catalogs rejected", strings.ReplaceAll(plan, `"catalogDigest":"simulator"`, `"catalogDigest":"os"`), "distinct catalogs", true},
		{"missing simulator rejected", strings.ReplaceAll(plan, `"name":"nicosim-tools-arm64"`, `"name":"other"`), "expected one simulator tools build", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "jq", "-L", ".", "-e", `include "nke-os-plan"; local_toolchain_scopes("all")`)
			cmd.Stdin = strings.NewReader(tc.input)
			out, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail || !strings.Contains(string(out), tc.want) {
				t.Fatalf("error=%v output=%s, want failure=%t message=%s", err, out, tc.fail, tc.want)
			}
			if tc.fail {
				return
			}
			var scopes []struct {
				Pin, CatalogDigest  string
				Components, Outputs []string
			}
			if err := json.Unmarshal(out, &scopes); err != nil {
				t.Fatal(err)
			}
			if len(scopes) != 3 || scopes[0].Pin != "packer" || scopes[1].Pin != "gcc" || scopes[2].Pin != "helm" ||
				!slices.Equal(scopes[0].Components, []string{"nke-node-image"}) ||
				!slices.Equal(scopes[0].Outputs, []string{"nke-node-image-arm64"}) ||
				!slices.Equal(scopes[1].Outputs, []string{"nicosim-tools-arm64"}) ||
				!slices.Equal(scopes[2].Outputs, []string{"controller-chart", "cwe", "nktp"}) {
				t.Fatalf("unexpected catalog-scoped selection: %+v", scopes)
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
			} {
				data, err := os.ReadFile(filepath.Join("..", path))
				if err != nil {
					t.Fatal(err)
				}
				write(path, data, 0600)
			}
			write("toolchains/nkd-packer/build.sh", []byte("#!/bin/sh\nprintf 'unexpected toolchain build\\n' >>\"$TEST_CALLS\"\nexit 99\n"), 0700)
			write("tools/runner", []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >>\"$TEST_CALLS\"\ncase \"$1 $2\" in 'run plan') printf '%s\\n' \"$TEST_PLAN\";; 'run graph') printf 'flowchart LR\\n';; *) exit 99;; esac\n"), 0700)
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
			for _, required := range []string{"golang@sha256:5bc7f572bbaa98885a3a1fd9c0aa76b59e3e14e8628bfc316bbfd0c701e4818c", "GOTOOLCHAIN=local", "make -C /src build BIN=/out/nkdctl"} {
				if !strings.Contains(string(calls), required) {
					t.Fatalf("bootstrap omitted pinned SDK/build contract %q: %s", required, calls)
				}
			}
			for _, forbidden := range []string{"NKD_BOOTSTRAP_GO_VERSION", "go.dev/dl", "tar --extract", "export PATH=\"/out/go/bin"} {
				if strings.Contains(string(calls), forbidden) {
					t.Fatalf("bootstrap replaced the image SDK with a second download %q: %s", forbidden, calls)
				}
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
	const plan = `{"lifecycleOutputs":[{"name":"nicosim-tools-arm64","kind":"executable","components":["nico-sim"]}],"builds":[{"name":"nicosim-tools-arm64","catalogDigest":"catalog","components":["nico-sim"],"executionGraph":{"nodes":[{"id":"fetch","needs":[]},{"id":"build","needs":["fetch"]}]},"catalogSnapshot":{"Components":{"nico-sim":{"fromSource":true,"toolchainImages":{"arm64":"old"}}}}}]}`
	const selectedGraph = `{"vertices":[{"key":"operation/build","kind":"operation","label":"build (build)","reference":"build","build":"nicosim-tools-arm64"},{"key":"artifact/tools","kind":"artifact","label":"tools (executable)","reference":"tools","build":"nicosim-tools-arm64"},{"key":"output/nicosim-tools-arm64","kind":"output","label":"nicosim-tools-arm64 (required executable)","reference":"tools","build":"nicosim-tools-arm64"},{"key":"route/private","kind":"route","label":"private-secret-ref","reference":"private-ref"},{"key":"producer/build","kind":"producer","label":"duplicate producer","reference":"build"}],"edges":[{"from":"operation/build","to":"artifact/tools","relation":"produces"},{"from":"artifact/tools","to":"output/nicosim-tools-arm64","relation":"fulfills slot"},{"from":"artifact/tools","to":"route/private","relation":"publication route"},{"from":"producer/build","to":"operation/build","relation":"instruction"}]}`
	for _, tc := range []struct {
		name, returned, buildExit string
		wantError                 bool
		shared                    bool
		inspected                 string
		graphFail                 bool
	}{
		{"freeze validated ID without resolving tag", validated, "0", false, false, "", false},
		{"reject empty result", "", "0", true, false, "", false},
		{"reject result mixed with diagnostics", "diagnostic\n" + validated, "0", true, false, "", false},
		{"failed validation cannot proceed", validated, "42", true, false, "", false},
		{"reuse first lane without compiling or building", validated, "99", false, true, validated, false},
		{"shared image cannot be a tag", "local:latest", "99", true, true, validated, false},
		{"shared image unavailable in runtime", validated, "99", true, true, "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", false},
		{"graph failure prevents toolchain work", validated, "0", true, false, "", true},
		{"selected graph failure prevents execution", validated, "0", true, false, "", false},
		{"overlong display record omitted", validated, "0", false, false, "", false},
		{"preserve OVN redirect with NKE clone", validated, "0", false, false, "", false},
		{"private preparation excludes final execution", validated, "0", false, false, "", false},
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
				"definitions/nke-local-dev-arm64/environment-local-docker.json"} {
				data, err := os.ReadFile(filepath.Join("..", path))
				if err != nil {
					t.Fatal(err)
				}
				write(path, data, 0600)
			}
			write("definitions/nke-local-dev-arm64/definition-simulator.json", []byte(`{"catalog":{"base":"simulator-catalog"},"outputs":[{"name":"nicosim-tools-arm64"}]}`), 0600)
			write("toolchains/nkd-packer/build.sh", []byte(`#!/bin/sh
printf 'toolchain-build %s\n' "$*" >>"$TEST_CALLS"
case " $* " in *' --print-validated-image-id '*) ;; *) exit 99;; esac
printf '%s\n' "$TEST_RETURNED"
exit "$TEST_BUILD_EXIT"
`), 0700)
			write("tools/runner", []byte(`#!/bin/sh
printf '%s\n' "$*" >>"$TEST_CALLS"
case "$1 $2" in
  'run plan')
    if [ "$4" = .nkd/laptop-bootstrap/id-probe/definition.json ]; then
      jq -e '.outputs[0].catalog.overlays[-1] | endswith("/toolchain-attempt.json")' "$4" >/dev/null || exit 99
      printf '%s\n' "$TEST_PLAN" | jq '. + {selected: true}'
    else printf '%s\n' "$TEST_PLAN"; fi;;
  'run graph')
    [ "$TEST_GRAPH_EXIT" = 0 ] || exit "$TEST_GRAPH_EXIT"
    if [ "$8" = json ]; then
      [ "$TEST_SELECTED_GRAPH_EXIT" = 0 ] || exit "$TEST_SELECTED_GRAPH_EXIT"
      jq -e '.selected == true' "$4" >/dev/null || exit 99
      printf '%s\n' "$TEST_SELECTED_GRAPH"
    else printf 'flowchart LR\n  fetch --> build\n'; fi;;
  'run local')
    jq -e --arg id "$TEST_VALIDATED" '.toolchainSubstitutions[0].to == $id' "$TEST_REPO/.nkd/laptop-bootstrap/id-probe/toolchain-attempt.json" || exit 99
    if [ -n "${TEST_CAPTURE_REDIRECTS:-}" ]; then
      printf '%s\n' "$GIT_CONFIG_COUNT" >"$TEST_CAPTURE_REDIRECTS"
      git config --get-all url./local-ovn.git.insteadOf >>"$TEST_CAPTURE_REDIRECTS"
      git config --get-all "url.$TEST_NKE_CLONE.insteadOf" >>"$TEST_CAPTURE_REDIRECTS"
    fi;;
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
  'image inspect') echo "$TEST_INSPECTED";;
  *) exit 99;;
esac
`), 0700)
			shared := ""
			if tc.shared {
				shared = "first-os"
				runner, err := os.ReadFile(filepath.Join(dir, "tools/runner"))
				if err != nil {
					t.Fatal(err)
				}
				write(".nkd/laptop-bootstrap/first-os/nkdctl", runner, 0700)
				write(".nkd/laptop-bootstrap/first-os/toolchain-image", []byte(tc.returned+"\n"), 0600)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			linkLauncherTestTimeout(t, filepath.Join(dir, "tools"))
			graphExit := "0"
			if tc.graphFail {
				graphExit = "42"
			}
			selectedGraphExit := "0"
			if tc.name == "selected graph failure prevents execution" {
				selectedGraphExit = "42"
			}
			displayGraph := selectedGraph
			if tc.name == "overlong display record omitted" {
				displayGraph = strings.Replace(displayGraph, "build (build)", strings.Repeat("x", 513), 1)
			}
			args := []string{filepath.Join(dir, "scripts/nke-full-arm64-local.sh"), "id-probe", "1", "simulator"}
			if tc.name == "private preparation excludes final execution" {
				args = append(args[:1], append([]string{"--prepare-only"}, args[1:]...)...)
			}
			cmd := exec.CommandContext(ctx, "bash", args...)
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"CONTAINER_RUNTIME=docker", "NKE_LOCAL_CLONE=", "DOCKER_HOST=", "DOCKER_CONTEXT=", "COLIMA_PROFILE=",
				"TEST_REPO="+dir, "TEST_PLAN="+plan, "TEST_RETURNED="+tc.returned, "TEST_BUILD_EXIT="+tc.buildExit,
				"TEST_VALIDATED="+validated, "TEST_CALLS="+filepath.Join(dir, "calls"), "NKD_LOCAL_BOOTSTRAP="+shared, "TEST_INSPECTED="+tc.inspected,
				"NKE_SOURCE_TOKEN=command-log-environment-sentinel", "TEST_GRAPH_EXIT="+graphExit, "TEST_SELECTED_GRAPH="+displayGraph, "TEST_SELECTED_GRAPH_EXIT="+selectedGraphExit)
			if tc.name == "preserve OVN redirect with NKE clone" {
				clone := filepath.Join(dir, "nke-clone")
				init := exec.CommandContext(ctx, "git", "init", "--quiet", clone)
				if output, err := init.CombinedOutput(); err != nil {
					t.Fatalf("prepare local Git fixture: %v %s", err, output)
				}
				clone, err := filepath.EvalSymlinks(clone)
				if err != nil {
					t.Fatal(err)
				}
				cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url./local-ovn.git.insteadOf",
					"GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/ovn-kubernetes.git", "NKE_LOCAL_CLONE="+clone,
					"TEST_NKE_CLONE="+clone, "TEST_CAPTURE_REDIRECTS="+filepath.Join(dir, "redirects"))
			}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v stdout=%s stderr=%s, want failure=%t", err, &stdout, &stderr, tc.wantError)
			}
			if strings.Contains(stdout.String(), "nkdctl command:") {
				t.Fatal("command logging contaminated stdout")
			}
			if tc.name == "preserve OVN redirect with NKE clone" {
				redirects, err := os.ReadFile(filepath.Join(dir, "redirects"))
				const want = "3\nhttps://github.com/dsx-ai-factory/ovn-kubernetes.git\nhttps://github.com/dsx-ai-factory/nke.git\nhttps://github.com/dsx-ai-factory/nke\n"
				if err != nil || string(redirects) != want {
					t.Fatalf("Git did not observe preserved OVN and appended NKE redirects: %v %q", err, redirects)
				}
			}
			if strings.Contains(stdout.String()+stderr.String(), "command-log-environment-sentinel") {
				t.Fatal("command logging exposed an environment value")
			}
			baseline, err := os.ReadFile(filepath.Join(dir, ".nkd/laptop-bootstrap/id-probe/baseline-plan.json"))
			if err != nil || !json.Valid(baseline) || string(baseline) != plan+"\n" {
				t.Fatalf("baseline Plan JSON changed: read error=%v", err)
			}
			if !tc.graphFail {
				physicalDir, err := filepath.EvalSymlinks(dir)
				if err != nil {
					t.Fatal(err)
				}
				graph, err := os.ReadFile(filepath.Join(dir, ".nkd/laptop-bootstrap/id-probe/build-graph.md"))
				if err != nil || !strings.Contains(string(graph), "```mermaid\nflowchart LR\n  fetch --> build\n```\n") {
					t.Fatalf("complete owner-rendered graph missing: read error=%v", err)
				}
				for _, required := range []string{"Build graph: " + physicalDir + "/.nkd/laptop-bootstrap/id-probe/build-graph.md", "Build graph: planned outputs: 1; producer steps: 2; producer dependency edges: 1", "  output: nicosim-tools-arm64 [executable]: nico-sim"} {
					if !strings.Contains(stderr.String(), required) {
						t.Fatalf("missing compiled graph summary %q: %s", required, &stderr)
					}
				}
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			runner := ".nkd/laptop-bootstrap/id-probe/nkdctl"
			if tc.shared {
				runner = ".nkd/laptop-bootstrap/first-os/nkdctl"
			}
			var logged, invoked []string
			for _, line := range strings.Split(stderr.String(), "\n") {
				if strings.HasPrefix(line, "nkdctl command:") {
					logged = append(logged, line)
				}
			}
			for _, line := range strings.Split(string(calls), "\n") {
				if strings.HasPrefix(line, "run plan ") || strings.HasPrefix(line, "run graph ") || strings.HasPrefix(line, "run local ") {
					invoked = append(invoked, "nkdctl command: "+runner+" "+line)
				}
			}
			wantCount := 6 // Baseline and selected Plans/graphs, dry-run preview, and final exec.
			if tc.name == "private preparation excludes final execution" {
				wantCount = 5
				path := filepath.Join(dir, ".nkd/laptop-bootstrap/id-probe/build-invocation.json")
				info, err := os.Lstat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("not private invocation: %v", err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var args []string
				if err := json.Unmarshal(raw, &args); err != nil || len(args) != 17 || args[0] != ".nkd/laptop-bootstrap/id-probe/nkdctl" || args[8] != "id-probe" || args[14] != "1" || args[16] != "docker" {
					t.Fatalf("bad private invocation: %v %q", err, raw)
				}
				for _, arg := range args {
					if strings.Contains(arg, "build-activity") {
						t.Fatal("bootstrap adopted activity descriptor")
					}
				}
				if strings.Contains(string(calls), "--attempt id-probe --attempts-root") {
					t.Fatal("prepare-only executed final Run")
				}
			}
			if tc.wantError {
				wantCount = 2 // Graph or toolchain rejection prevents execution.
			}
			if selectedGraphExit != "0" {
				wantCount = 4 // Selected graph rejection prevents preview and execution.
			}
			if len(logged) != wantCount || !slices.Equal(logged, invoked) {
				t.Fatalf("logged commands=%q, executed commands=%q; want %d", logged, invoked, wantCount)
			}
			if graph := strings.Index(string(calls), "run graph "); graph < 0 || (strings.Contains(string(calls), "toolchain-build") && graph > strings.Index(string(calls), "toolchain-build")) {
				t.Fatalf("graph was not rendered before toolchain work: %s", calls)
			}
			if tc.graphFail && strings.Contains(string(calls), "toolchain-build") {
				t.Fatalf("graph failure performed toolchain work: %s", calls)
			}
			if !tc.shared && strings.Contains(string(calls), "image inspect") {
				t.Fatalf("launcher resolved validated bytes through a mutable tag: %s", calls)
			}
			if tc.shared && (strings.Contains(string(calls), "toolchain-build") || strings.Contains(string(calls), "pull --platform") || strings.Contains(string(calls), "make -C /src")) {
				t.Fatalf("shared bootstrap repeated build work: %s", calls)
			}
			if reached := strings.Contains(string(calls), "run local"); reached == tc.wantError {
				t.Fatalf("local execution reached=%t; want %t: %s", reached, !tc.wantError, calls)
			}
			if !tc.wantError {
				graph, err := os.ReadFile(filepath.Join(dir, ".nkd/laptop-bootstrap/id-probe/graph.json"))
				if err != nil || string(graph) != displayGraph+"\n" {
					t.Fatalf("selected owner graph was not preserved: %v %s", err, graph)
				}
				selected := "run plan --definition .nkd/laptop-bootstrap/id-probe/definition.json"
				if strings.Index(string(calls), selected) < strings.Index(string(calls), "toolchain-build") ||
					!strings.Contains(string(calls), "run graph --plan .nkd/laptop-bootstrap/id-probe/selected-plan.json --view complete --format json") {
					t.Fatalf("selected graph did not follow toolchain substitution: %s", calls)
				}
				requiredRecords := []string{
					`Build display: {"vertices":3,"edges":2,"truncated":false}`,
					`Build vertex: {"key":"operation/build","kind":"operation","label":"build (build)","reference":"build","build":"nicosim-tools-arm64"}`,
					`Build edge: {"from":"artifact/tools","to":"output/nicosim-tools-arm64","relation":"fulfills slot"}`,
				}
				if tc.name == "overlong display record omitted" {
					requiredRecords = []string{`Build display: {"vertices":2,"edges":1,"truncated":true}`, requiredRecords[2]}
					if strings.Contains(stderr.String(), `"key":"operation/build"`) || strings.Contains(stderr.String(), `"from":"operation/build"`) {
						t.Fatal("display kept an oversized record or its dangling edge")
					}
				}
				for _, required := range requiredRecords {
					if !strings.Contains(stderr.String(), required) {
						t.Fatalf("missing selected graph display record %q: %s", required, &stderr)
					}
				}
				if strings.Index(stderr.String(), "Build display:") < strings.LastIndex(stderr.String(), "Build edge:") ||
					strings.Index(stderr.String(), "Build display:") < strings.LastIndex(stderr.String(), "Build vertex:") {
					t.Fatal("display completion metadata preceded graph records")
				}
				if strings.Contains(stderr.String(), "private-secret-ref") || strings.Contains(stderr.String(), "producer/build") {
					t.Fatal("display leaked excluded route or duplicated producer records")
				}
			}
		})
	}
}
