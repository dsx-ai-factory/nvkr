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

func launcherUtilityImage(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "inputs/environments/local-arm64-kvm.json"))
	if err != nil {
		t.Fatal(err)
	}
	var selected struct {
		UtilityContainer string `json:"utilityContainer"`
	}
	if err := json.Unmarshal(raw, &selected); err != nil || selected.UtilityContainer == "" {
		t.Fatalf("read selected bootstrap image: %v", err)
	}
	return selected.UtilityContainer
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
		{"missing accelerator", []string{"--build-accelerator"}, "Usage:"},
		{"unknown accelerator", []string{"--build-accelerator", "guessed", "--check", "all"}, "Build accelerator must"},
		{"foreign accelerator", []string{"--build-accelerator", "hvf", "--check", "all"}, "Build accelerator must"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "scripts"), 0700); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile("nvke-full-arm64-local.sh")
			if err != nil {
				t.Fatal(err)
			}
			launcher := filepath.Join(dir, "scripts", "nvke-full-arm64-local.sh")
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
			if _, err := os.Stat(filepath.Join(dir, ".nvkr")); !os.IsNotExist(err) {
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
		{"relative clone rejected", "Linux", "aarch64", "docker", "providers", "running", "100000", "ok", "NVKE_LOCAL_CLONE must be an absolute path", true},
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
			data, err := os.ReadFile("nvke-full-arm64-local.sh")
			if err != nil {
				t.Fatal(err)
			}
			launcher := filepath.Join(dir, "scripts", "nvke-full-arm64-local.sh")
			if err := os.WriteFile(launcher, data, 0600); err != nil {
				t.Fatal(err)
			}
			inputs := filepath.Join(dir, "inputs/environments")
			if err := os.MkdirAll(inputs, 0700); err != nil {
				t.Fatal(err)
			}
			env, err := os.ReadFile(filepath.Join("..", "inputs/environments/local-arm64-kvm.json"))
			if err != nil {
				t.Fatal(err)
			}
			switch tc.name {
			case "mutable bootstrap pin rejected":
				env = []byte(strings.ReplaceAll(string(env), launcherUtilityImage(t), "golang:latest"))
			case "foreign bootstrap platform rejected":
				env = []byte(strings.ReplaceAll(string(env), "linux/arm64", "linux/amd64"))
			}
			if err := os.WriteFile(filepath.Join(inputs, "local-arm64-kvm.json"), env, 0600); err != nil {
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
			cmd.Env = append(os.Environ(), "PATH="+tools, "CONTAINER_RUNTIME="+tc.runtime, "NVKE_LOCAL_CLONE=", "DOCKER_HOST=", "DOCKER_CONTEXT=", "COLIMA_PROFILE=",
				"TEST_SYSTEM="+tc.system, "TEST_ARCH="+tc.arch, "TEST_STATE="+tc.state, "TEST_DISK="+tc.disk, "TEST_DAEMON="+tc.daemon,
				"TEST_CALLS="+filepath.Join(dir, "calls"))
			if tc.name == "relative clone rejected" {
				cmd.Env = append(cmd.Env, "NVKE_LOCAL_CLONE=../nke")
			}
			switch tc.name {
			case "invalid Git count":
				cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=not-a-count")
			case "overflowing Git count":
				cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=999999999999999999999999999999")
			case "no room for local clone redirects":
				cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=15", "NVKE_LOCAL_CLONE=/must-not-be-inspected")
			}
			if strings.HasPrefix(tc.name, "Colima ") {
				profile := "nvkr-demo"
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
			if strings.Contains(string(out), "nvkr command:") {
				t.Fatal("prerequisite check logged an nvkr invocation")
			}
			if tc.daemon == "missing-buildx" {
				for _, required := range []string{"sudo apt-get install docker-buildx", "sudo apt-get install docker-buildx-plugin", "docker buildx version", "No downloads or attempt creation"} {
					if !strings.Contains(string(out), required) {
						t.Fatalf("missing Buildx recovery instruction %q: %s", required, out)
					}
				}
			}
			if _, err := os.Stat(filepath.Join(dir, ".nvkr")); !os.IsNotExist(err) {
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
				for _, required := range []string{"ssh --profile nvkr-demo -- bash -c", "env -u DOCKER_HOST -u DOCKER_CONTEXT CONTAINER_RUNTIME=docker", "--check providers"} {
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
			cmd := exec.CommandContext(ctx, "jq", "-L", ".", "-e", `include "nvke-os-plan"; simulator_toolchain_scope | toolchain_overlay("arm64"; .pin; "fresh"; .catalogDigest; .components)`)
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
	const plan = `{"builds":[{"name":"controller-chart","catalogDigest":"catalog","components":["nvke-controller-chart"],"catalogSnapshot":{"Components":{"nvke-controller-chart":{"fromSource":true,"toolchainImages":{"arm64":"old"}}}}},{"name":"nktp","catalogDigest":"catalog","components":["nktp-chart"],"catalogSnapshot":{"Components":{"nktp-chart":{"fromSource":true,"toolchainImages":{"arm64":"old"}}}}},{"name":"cwe","catalogDigest":"other","components":["nke-controller"]}]}`
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
			cmd := exec.CommandContext(ctx, "jq", "-L", ".", "-e", `include "nvke-os-plan"; site_helm_toolchain_scope | toolchain_overlay("arm64"; .pin; "fresh"; .catalogDigest; .components)`)
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
			if overlay.Catalog != "catalog" || len(overlay.Subs) != 1 || overlay.Subs[0].Arch != "arm64" || overlay.Subs[0].From != "old" || overlay.Subs[0].To != "fresh" || !slices.Equal(overlay.Subs[0].Components, []string{"nktp-chart", "nvke-controller-chart"}) {
				t.Fatalf("unexpected site substitution: %+v", overlay)
			}
		})
	}
}

func TestAllLocalToolchainScopes(t *testing.T) {
	const plan = `{"builds":[
      {"name":"nvke-node-image-arm64","catalogDigest":"os","components":["nvke-node-image","nvke-agent"],"executionGraph":{"nodes":[{"kind":"build","builderType":"vm-image","component":"nvke-node-image"}]},"catalogSnapshot":{"Components":{"nvke-node-image":{"fromSource":true,"toolchainImages":{"arm64":"packer"}},"nvke-agent":{"fromSource":true,"toolchainImages":{"arm64":"go"}}}}},
      {"name":"nicosim-tools-arm64","catalogDigest":"simulator","components":["nico-sim"],"executionGraph":{"nodes":[]},"catalogSnapshot":{"Components":{"nico-sim":{"fromSource":true,"toolchainImages":{"arm64":"gcc"}}}}},
      {"name":"controller-chart","catalogDigest":"site","components":["nvke-controller-chart"],"executionGraph":{"nodes":[]},"catalogSnapshot":{"Components":{"nvke-controller-chart":{"fromSource":true,"toolchainImages":{"arm64":"helm"}}}}},
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
			cmd := exec.CommandContext(ctx, "jq", "-L", ".", "-e", `include "nvke-os-plan"; local_toolchain_scopes("all")`)
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
				!slices.Equal(scopes[0].Components, []string{"nvke-node-image"}) ||
				!slices.Equal(scopes[0].Outputs, []string{"nvke-node-image-arm64"}) ||
				!slices.Equal(scopes[1].Outputs, []string{"nicosim-tools-arm64"}) ||
				!slices.Equal(scopes[2].Outputs, []string{"controller-chart", "cwe", "nktp"}) {
				t.Fatalf("unexpected catalog-scoped selection: %+v", scopes)
			}
		})
	}
}

func TestLocalToolchainDefinitionRetainsScopedRelativeInputs(t *testing.T) {
	for _, tc := range []struct {
		name, base string
		fail       bool
	}{
		{"repository relative", "../../catalogs/os", false},
		{"absolute rejected", "/private/catalog", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := `{"catalog":{"base":"` + tc.base + `"},"outputs":[{"name":"disk"},{"name":"other","catalog":{"base":"../../catalogs/site","overlays":["./original.json"]}}]}`
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "jq", "-L", ".", "-e", `include "nvke-os-plan"; local_toolchain_definition("inputs/definitions/demo"; "../../../"; [{outputs:["disk"]}])`)
			cmd.Stdin = strings.NewReader(input)
			raw, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("projection: %v %s", err, raw)
			}
			if tc.fail {
				return
			}
			var result struct {
				Outputs []struct {
					Catalog struct {
						Base     string
						Overlays []string
					}
				}
			}
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			if len(result.Outputs) != 2 || filepath.Clean(result.Outputs[0].Catalog.Base) != "../../../inputs/catalogs/os" ||
				!slices.Equal(result.Outputs[0].Catalog.Overlays, []string{"./toolchain-attempt.json"}) ||
				!slices.Equal(result.Outputs[1].Catalog.Overlays, []string{"../../../inputs/definitions/demo/./original.json"}) {
				t.Fatalf("changed catalog scoping or relative inputs: %s", raw)
			}
		})
	}
}

func TestLocalToolchainSubstitutionsKeepOnlyChangedPins(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "jq", "-L", ".", "-c", `include "nvke-os-plan"; local_toolchain_substitutions("same")`)
	cmd.Stdin = strings.NewReader(`[{"pin":"same","outputs":["unchanged"]},{"pin":"old","outputs":["changed"]}]`)
	raw, err := cmd.CombinedOutput()
	if err != nil || string(raw) != "[{\"pin\":\"old\",\"outputs\":[\"changed\"]}]\n" {
		t.Fatalf("unchanged image received an invalid self-substitution: %s %v", raw, err)
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
				"scripts/nvke-full-arm64-local.sh", "scripts/nvke-os-plan.jq",
				"inputs/environments/local-arm64-kvm.json",
			} {
				data, err := os.ReadFile(filepath.Join("..", path))
				if err != nil {
					t.Fatal(err)
				}
				write(path, data, 0600)
			}
			write("toolchains/nvkr-packer/build.sh", []byte("#!/bin/sh\nprintf 'unexpected toolchain build\\n' >>\"$TEST_CALLS\"\nexit 99\n"), 0700)
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
  'run --rm') cp "$TEST_REPO/tools/runner" "$TEST_REPO/.nvkr/laptop-bootstrap/scope-probe/nvkr" ;;
  *) exit 99 ;;
esac
`), 0700)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			linkLauncherTestTimeout(t, filepath.Join(dir, "tools"))
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(dir, "scripts", "nvke-full-arm64-local.sh"), "scope-probe", "1", "simulator")
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"CONTAINER_RUNTIME=docker", "NVKE_LOCAL_CLONE=", "DOCKER_HOST=", "DOCKER_CONTEXT=", "COLIMA_PROFILE=",
				"TEST_REPO="+dir, "TEST_PLAN="+tc.plan, "TEST_CALLS="+filepath.Join(dir, "calls"))
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("error=%v output=%s, want scope rejection %s", err, out, tc.want)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{launcherUtilityImage(t), "GOTOOLCHAIN=local", "make -C /src build BIN=/out/nvkr"} {
				if !strings.Contains(string(calls), required) {
					t.Fatalf("bootstrap omitted pinned SDK/build contract %q: %s", required, calls)
				}
			}
			for _, forbidden := range []string{"NVKR_BOOTSTRAP_GO_VERSION", "go.dev/dl", "tar --extract", "export PATH=\"/out/go/bin"} {
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
		{"selected Plan failure prevents execution", validated, "0", true, false, "", false},
		{"preserve OVN redirect with NVKE clone", validated, "0", false, false, "", false},
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
			for _, path := range []string{"scripts/nvke-full-arm64-local.sh", "scripts/nvke-os-plan.jq",
				"inputs/environments/local-arm64-kvm.json"} {
				data, err := os.ReadFile(filepath.Join("..", path))
				if err != nil {
					t.Fatal(err)
				}
				write(path, data, 0600)
			}
			write("inputs/definitions/diagnostics/demo-arm64/simulator.json", []byte(`{"catalog":{"base":"../../../catalogs/tools/native"},"outputs":[{"name":"nicosim-tools-arm64"}]}`), 0600)
			write("toolchains/nvkr-packer/build.sh", []byte(`#!/bin/sh
printf 'toolchain-build %s\n' "$*" >>"$TEST_CALLS"
case " $* " in *' --print-validated-image-id '*) ;; *) exit 99;; esac
printf '%s\n' "$TEST_RETURNED"
exit "$TEST_BUILD_EXIT"
`), 0700)
			write("tools/runner", []byte(`#!/bin/sh
printf '%s\n' "$*" >>"$TEST_CALLS"
case "$1 $2" in
  'run plan')
    if [ "$4" = .nvkr/laptop-bootstrap/id-probe/definition.json ]; then
      [ "$TEST_SELECTED_PLAN_EXIT" = 0 ] || exit "$TEST_SELECTED_PLAN_EXIT"
      jq -e '.outputs[0].catalog.overlays[-1] == "./toolchain-attempt.json" and
        .catalog.base == "../../../inputs/definitions/diagnostics/demo-arm64/../../../catalogs/tools/native" and
        .outputs[0].catalog.base == .catalog.base' "$4" >/dev/null || exit 99
      printf '%s\n' "$TEST_PLAN" | jq '. + {selected: true}'
    else printf '%s\n' "$TEST_PLAN"; fi;;
  'run graph')
    [ "$TEST_GRAPH_EXIT" = 0 ] || exit "$TEST_GRAPH_EXIT"
    printf 'flowchart LR\n  fetch --> build\n';;
  'run local')
    case " $* " in *' --capture-inputs-root=. '*) ;; *) exit 99;; esac
    jq -e --arg id "$TEST_VALIDATED" '.toolchainSubstitutions[0].to == $id' "$TEST_REPO/.nvkr/laptop-bootstrap/id-probe/toolchain-attempt.json" || exit 99
    if [ -n "${TEST_CAPTURE_REDIRECTS:-}" ]; then
      printf '%s\n' "$GIT_CONFIG_COUNT" >"$TEST_CAPTURE_REDIRECTS"
      git config --get-all url./local-ovn.git.insteadOf >>"$TEST_CAPTURE_REDIRECTS"
      git config --get-all "url.$TEST_NVKE_CLONE.insteadOf" >>"$TEST_CAPTURE_REDIRECTS"
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
  'run --rm') cp "$TEST_REPO/tools/runner" "$TEST_REPO/.nvkr/laptop-bootstrap/id-probe/nvkr";;
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
				write(".nvkr/laptop-bootstrap/first-os/nvkr", runner, 0700)
				write(".nvkr/laptop-bootstrap/first-os/toolchain-image", []byte(tc.returned+"\n"), 0600)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			linkLauncherTestTimeout(t, filepath.Join(dir, "tools"))
			graphExit := "0"
			if tc.graphFail {
				graphExit = "42"
			}
			selectedPlanExit := "0"
			if tc.name == "selected Plan failure prevents execution" {
				selectedPlanExit = "42"
			}
			args := []string{filepath.Join(dir, "scripts/nvke-full-arm64-local.sh"), "id-probe", "1", "simulator"}
			if tc.name == "private preparation excludes final execution" {
				args = append(args[:1], append([]string{"--prepare-only"}, args[1:]...)...)
			}
			cmd := exec.CommandContext(ctx, "bash", args...)
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(dir, "tools")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"CONTAINER_RUNTIME=docker", "NVKE_LOCAL_CLONE=", "DOCKER_HOST=", "DOCKER_CONTEXT=", "COLIMA_PROFILE=",
				"TEST_REPO="+dir, "TEST_PLAN="+plan, "TEST_RETURNED="+tc.returned, "TEST_BUILD_EXIT="+tc.buildExit,
				"TEST_VALIDATED="+validated, "TEST_CALLS="+filepath.Join(dir, "calls"), "NVKR_LOCAL_BOOTSTRAP="+shared, "TEST_INSPECTED="+tc.inspected,
				"NVKE_SOURCE_TOKEN=command-log-environment-sentinel", "TEST_GRAPH_EXIT="+graphExit, "TEST_SELECTED_PLAN_EXIT="+selectedPlanExit)
			if tc.name == "preserve OVN redirect with NVKE clone" {
				clone := filepath.Join(dir, "nvke-clone")
				init := exec.CommandContext(ctx, "git", "init", "--quiet", clone)
				if output, err := init.CombinedOutput(); err != nil {
					t.Fatalf("prepare local Git fixture: %v %s", err, output)
				}
				clone, err := filepath.EvalSymlinks(clone)
				if err != nil {
					t.Fatal(err)
				}
				cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url./local-ovn.git.insteadOf",
					"GIT_CONFIG_VALUE_0=https://github.com/dsx-ai-factory/ovn-kubernetes.git", "NVKE_LOCAL_CLONE="+clone,
					"TEST_NVKE_CLONE="+clone, "TEST_CAPTURE_REDIRECTS="+filepath.Join(dir, "redirects"))
			}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v stdout=%s stderr=%s, want failure=%t", err, &stdout, &stderr, tc.wantError)
			}
			if strings.Contains(stdout.String(), "nvkr command:") {
				t.Fatal("command logging contaminated stdout")
			}
			if tc.name == "preserve OVN redirect with NVKE clone" {
				redirects, err := os.ReadFile(filepath.Join(dir, "redirects"))
				const want = "3\nhttps://github.com/dsx-ai-factory/ovn-kubernetes.git\nhttps://github.com/dsx-ai-factory/nvke.git\nhttps://github.com/dsx-ai-factory/nvke\n"
				if err != nil || string(redirects) != want {
					t.Fatalf("Git did not observe preserved OVN and appended NVKE redirects: %v %q", err, redirects)
				}
			}
			if strings.Contains(stdout.String()+stderr.String(), "command-log-environment-sentinel") {
				t.Fatal("command logging exposed an environment value")
			}
			baseline, err := os.ReadFile(filepath.Join(dir, ".nvkr/laptop-bootstrap/id-probe/baseline-plan.json"))
			if err != nil || !json.Valid(baseline) || string(baseline) != plan+"\n" {
				t.Fatalf("baseline Plan JSON changed: read error=%v", err)
			}
			if !tc.graphFail {
				physicalDir, err := filepath.EvalSymlinks(dir)
				if err != nil {
					t.Fatal(err)
				}
				graph, err := os.ReadFile(filepath.Join(dir, ".nvkr/laptop-bootstrap/id-probe/build-graph.md"))
				if err != nil || !strings.Contains(string(graph), "```mermaid\nflowchart LR\n  fetch --> build\n```\n") {
					t.Fatalf("complete owner-rendered graph missing: read error=%v", err)
				}
				for _, required := range []string{"Build graph: " + physicalDir + "/.nvkr/laptop-bootstrap/id-probe/build-graph.md", "Build graph: planned outputs: 1; producer steps: 2; producer dependency edges: 1", "  output: nicosim-tools-arm64 [executable]: nico-sim"} {
					if !strings.Contains(stderr.String(), required) {
						t.Fatalf("missing compiled graph summary %q: %s", required, &stderr)
					}
				}
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			if err != nil {
				t.Fatal(err)
			}
			runner := ".nvkr/laptop-bootstrap/id-probe/nvkr"
			if tc.shared {
				runner = ".nvkr/laptop-bootstrap/first-os/nvkr"
			}
			var logged, invoked []string
			for _, line := range strings.Split(stderr.String(), "\n") {
				if strings.HasPrefix(line, "nvkr command:") {
					logged = append(logged, line)
				}
			}
			for _, line := range strings.Split(string(calls), "\n") {
				if strings.HasPrefix(line, "run plan ") || strings.HasPrefix(line, "run graph ") || strings.HasPrefix(line, "run local ") {
					invoked = append(invoked, "nvkr command: "+runner+" "+line)
				}
			}
			wantCount := 5 // Baseline Plan/graph, selected Plan, dry-run preview, and final exec.
			if tc.name == "private preparation excludes final execution" {
				wantCount = 4
				path := filepath.Join(dir, ".nvkr/laptop-bootstrap/id-probe/build-invocation.json")
				info, err := os.Lstat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("not private invocation: %v", err)
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var args []string
				if err := json.Unmarshal(raw, &args); err != nil || len(args) != 19 || args[0] != ".nvkr/laptop-bootstrap/id-probe/nvkr" || args[8] != "id-probe" || args[14] != "1" || args[16] != "docker" || args[17] != "--capture-inputs-root=." || args[18] != "--ui=plain" {
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
			if selectedPlanExit != "0" {
				wantCount = 3 // Selected Plan rejection prevents preview and execution.
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
				frozen, err := os.ReadFile(filepath.Join(dir, ".nvkr/laptop-bootstrap/id-probe/definition.json"))
				if err != nil {
					t.Fatal(err)
				}
				type catalogLocation struct{ Base string }
				var authored struct {
					Catalog catalogLocation
					Outputs []struct{ Catalog catalogLocation }
				}
				if err := json.Unmarshal(frozen, &authored); err != nil {
					t.Fatal(err)
				}
				physicalDir, err := filepath.EvalSymlinks(dir)
				if err != nil {
					t.Fatal(err)
				}
				wantCatalog := filepath.Join(physicalDir, "inputs/catalogs/tools/native")
				definitionDir := filepath.Join(physicalDir, ".nvkr/laptop-bootstrap/id-probe")
				if filepath.IsAbs(authored.Catalog.Base) || filepath.Join(definitionDir, authored.Catalog.Base) != wantCatalog || len(authored.Outputs) != 1 ||
					filepath.IsAbs(authored.Outputs[0].Catalog.Base) || filepath.Join(definitionDir, authored.Outputs[0].Catalog.Base) != wantCatalog {
					t.Fatal("frozen catalogs did not resolve relative to the selected Definition")
				}
				selectedPlan, err := os.ReadFile(filepath.Join(dir, ".nvkr/laptop-bootstrap/id-probe/selected-plan.json"))
				var selected struct{ Selected bool }
				if err != nil || json.Unmarshal(selectedPlan, &selected) != nil || !selected.Selected {
					t.Fatal("selected owner Plan was not preserved after toolchain substitution", err)
				}
				selectedCommand := "run plan --definition .nvkr/laptop-bootstrap/id-probe/definition.json"
				if strings.Index(string(calls), selectedCommand) < strings.Index(string(calls), "toolchain-build") {
					t.Fatalf("selected Plan did not follow toolchain substitution: %s", calls)
				}
			}
		})
	}
}
