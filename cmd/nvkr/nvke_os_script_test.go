// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestOSPlanQueriesSelectDiskAndEmitLoadableOverlay(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("OS wrapper requires jq")
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			raw, plan := nvkeOSScriptPlan(t, arch)
			rootRaw, err := runOSPlanQuery(t, raw, "disk_root")
			if err != nil {
				t.Fatalf("disk root query: %v: %s", err, rootRaw)
			}
			var root string
			if err := json.Unmarshal(rootRaw, &root); err != nil {
				t.Fatal(err)
			}
			build := plan.Builds[0]
			baseline := build.CatalogSnapshot.Components[root].ToolchainImages[arch]
			var components []string
			var disk ir.Node
			for _, node := range build.Graph.Nodes {
				if node.Kind == ir.KindBuild && node.BuilderType == ir.BuilderVMImage {
					disk = node
				}
			}
			if disk.Component != root {
				t.Fatalf("query selected %q, want %q", root, disk.Component)
			}
			for _, name := range build.Components {
				component := build.CatalogSnapshot.Components[name]
				if component.FromSource && component.ToolchainImages[arch] == baseline {
					components = append(components, name)
				}
			}
			encodedComponents, err := json.Marshal(components)
			if err != nil {
				t.Fatal(err)
			}
			image := "sha256:" + strings.Repeat("e", 64)
			overlayRaw, err := runOSPlanQuery(t, nil,
				"toolchain_overlay($arch; $from; $to; $digest; $components)", "-n",
				"--arg", "arch", arch, "--arg", "from", baseline, "--arg", "to", image,
				"--arg", "digest", build.CatalogDigest, "--argjson", "components", string(encodedComponents))
			if err != nil {
				t.Fatalf("overlay emission: %v: %s", err, overlayRaw)
			}
			path := filepath.Join(t.TempDir(), "overlay.json")
			if err := os.WriteFile(path, overlayRaw, 0o600); err != nil {
				t.Fatal(err)
			}
			overlay, err := catalog.LoadOverlayContext(t.Context(), path)
			if err != nil {
				t.Fatalf("script overlay cannot be loaded: %v", err)
			}
			stack, err := catalog.ApplyStack(build.CatalogSnapshot, overlay)
			if err != nil || stack.Catalog.Components[root].ToolchainImages[arch] != image {
				t.Fatalf("script overlay cannot bind the observed image: %v", err)
			}
			for _, test := range []struct {
				name  string
				count int
			}{
				{name: "missing disk"},
				{name: "ambiguous disks", count: 2},
			} {
				t.Run(test.name, func(t *testing.T) {
					graph := *build.Graph
					graph.Nodes = slices.DeleteFunc(slices.Clone(graph.Nodes), func(node ir.Node) bool {
						return node.Kind == ir.KindBuild && node.BuilderType == ir.BuilderVMImage
					})
					for range test.count {
						graph.Nodes = append(graph.Nodes, disk)
					}
					changed := *plan
					changed.Builds = slices.Clone(plan.Builds)
					changed.Builds[0].Graph = &graph
					payload, err := json.Marshal(changed)
					if err != nil {
						t.Fatal(err)
					}
					output, err := runOSPlanQuery(t, payload, "disk_root")
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 5 || !strings.Contains(string(output), "expected exactly one disk root") {
						t.Fatalf("query did not reject %d disk roots as ambiguous: %v: %s", test.count, err, output)
					}
				})
			}
		})
	}
}

func TestOSPlanSourceClosureRejectsAliasAndPinDrift(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("OS validation requires jq")
	}
	_, plan := nvkeOSScriptPlan(t, "amd64")
	const repository = "https://github.com/dsx-ai-factory/nvke.git"
	var source ir.Node
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.Kind == ir.KindFetch && node.Repo == repository {
			source = node
			break
		}
	}
	if source.Commit == "" {
		t.Fatal("fixture lost its NVKE source")
	}
	pin, err := json.Marshal(map[string]string{"commit": source.Commit, "treeHash": source.TreeHash})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, repo string
		drift      bool
		want       bool
	}{
		{name: "canonical", repo: repository, want: true},
		{name: "canonical at another commit", repo: repository, drift: true},
		{name: "without suffix", repo: "https://github.com/dsx-ai-factory/nvke"},
		{name: "case alias", repo: "HTTPS://GITHUB.COM/DSX-AI-FACTORY/NVKE.git", drift: true},
		{name: "trailing slash", repo: repository + "/", drift: true},
		{name: "repeated trailing slash", repo: repository + "//", drift: true},
		{name: "query suffix", repo: repository + "?x", drift: true},
		{name: "fragment suffix", repo: repository + "#main", drift: true},
		{name: "HTTPS port", repo: "https://github.com:443/dsx-ai-factory/nvke.git", drift: true},
		{name: "www host", repo: "https://www.github.com/dsx-ai-factory/nvke.git", drift: true},
		{name: "same project on another host", repo: "https://example.test/dsx-ai-factory/nvke.git", drift: true},
		{name: "ssh URL", repo: "ssh://git@github.com/dsx-ai-factory/nvke.git", drift: true},
		{name: "SSH port", repo: "ssh://git@github.com:22/dsx-ai-factory/nvke.git", drift: true},
		{name: "scp URL", repo: "git@github.com:dsx-ai-factory/nvke.git", drift: true},
		{name: "git URL", repo: "git://github.com/dsx-ai-factory/nvke.git", drift: true},
		{name: "unrelated source", repo: "https://github.com/ovn-kubernetes/ovn-kubernetes", drift: true, want: true},
		{name: "prefix sibling repository", repo: "https://github.com/dsx-ai-factory/nvke-mirror.git", drift: true, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph := *plan.Builds[0].Graph
			graph.Nodes = slices.Clone(graph.Nodes)
			additional := source
			additional.Repo = test.repo
			if test.drift {
				additional.Commit = strings.Repeat("a", 40)
			}
			graph.Nodes = append(graph.Nodes, additional)
			changed := *plan
			changed.Builds = slices.Clone(plan.Builds)
			changed.Builds[0].Graph = &graph
			raw, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			output, err := runOSPlanQuery(t, raw, "nvke_source_closure($pin)", "--argjson", "pin", string(pin))
			if (err == nil) != test.want {
				t.Fatalf("source closure=%s, error=%v, want accepted=%t", output, err, test.want)
			}
			if !test.want {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 || strings.TrimSpace(string(output)) != "false" {
					t.Fatalf("source closure failed outside its rejection predicate: %v: %s", err, output)
				}
			}
		})
	}
}

func nvkeOSScriptPlan(t *testing.T, arch string) ([]byte, *factory.Plan) {
	t.Helper()
	root := filepath.Dir(filepath.Dir(demoScript(t, "nvke-os-local.sh")))
	t.Chdir(root)
	input := "inputs"
	raw, err := runCLIWithCapturedStdout(t, newApp(), "nvkr", "run", "plan",
		"--definition", filepath.Join(input, "definitions/diagnostics/os-"+arch+".yaml"),
		"--environment", filepath.Join(input, "environments/local-linux-"+arch+"-kvm.yaml"), "--json")
	if err != nil {
		t.Fatal(err)
	}
	var plan factory.Plan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	return raw, &plan
}

func runOSPlanQuery(t *testing.T, input []byte, query string, args ...string) ([]byte, error) {
	t.Helper()
	args = append([]string{"-L", "scripts", "-e"}, args...)
	cmd := exec.CommandContext(t.Context(), "jq", append(args, `include "nvke-os-plan"; `+query)...)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.CombinedOutput()
}

func TestNVKETransportOverrideDoesNotRewriteSiblingRepository(t *testing.T) {
	source, err := os.ReadFile(demoScript(t, "nvke-os-local.sh"))
	if err != nil {
		t.Fatal(err)
	}
	const repository = "https://github.com/dsx-ai-factory/nvke.git"
	if !strings.Contains(string(source), `export GIT_CONFIG_VALUE_0="`+repository+`"`) {
		t.Fatal("bootstrap does not use the canonical NVKE transport URL")
	}
	checkout := t.TempDir()
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+checkout+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", repository)
	for _, test := range []struct{ name, repository, want string }{
		{name: "NVKE", repository: repository, want: checkout},
		{name: "sibling", repository: "https://github.com/dsx-ai-factory/nvkr.git", want: "https://github.com/dsx-ai-factory/nvkr.git"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "git", "ls-remote", "--get-url", test.repository)
			output, err := cmd.CombinedOutput()
			if err != nil || strings.TrimSpace(string(output)) != test.want {
				t.Fatalf("transport URL=%q, error=%v, want %q", output, err, test.want)
			}
		})
	}
}

func TestOSDockerBootstrapUsesPrivateSharedTemporaryDirectory(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("OS wrapper requires jq")
	}
	source, err := os.ReadFile(demoScript(t, "nvke-os-local.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				"scripts/nvke-os-local.sh":                              string(source),
				"inputs/definitions/diagnostics/os-" + arch + ".yaml":   "fixture",
				"inputs/environments/local-linux-" + arch + "-kvm.yaml": "fixture",
				"bin/docker":                      "#!/bin/sh\nexit 99\n",
				"bin/uname":                       "#!/bin/sh\ncase \"$1\" in -s) echo Linux;; -m) echo \"$TEST_MACHINE\";; *) exit 99;; esac\n",
				"toolchains/nvkr-packer/build.sh": "#!/bin/sh\nprintf '%s' \"$TMPDIR\" >\"$TEST_TMP_RECORD\"\nexit 42\n",
			}
			for path, contents := range files {
				path = filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			machine := "aarch64"
			if arch == "amd64" {
				machine = "x86_64"
			}
			record := filepath.Join(root, "tmpdir-record")
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(root, "scripts/nvke-os-local.sh"), "test-attempt")
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"),
				"CONTAINER_RUNTIME=docker", "NVKE_OS_ARCH="+arch, "NVKE_LOCAL_CLONE=",
				"TEST_MACHINE="+machine, "TEST_TMP_RECORD="+record)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 42 {
				t.Fatalf("did not reach toolchain preparation: %v\n%s", err, output)
			}
			raw, err := os.ReadFile(record)
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.Join(root, ".nvkr/bootstrap/test-attempt/tmp")
			if strings.TrimSpace(string(raw)) != want {
				t.Fatalf("TMPDIR = %q, want %q", raw, want)
			}
			info, err := os.Stat(want)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
				t.Fatalf("temporary directory is not private: info=%v, err=%v", info, err)
			}
		})
	}
}

func TestOSBootstrapRejectsUnsupportedRoutesBeforeOutput(t *testing.T) {
	source, err := os.ReadFile(demoScript(t, "nvke-os-local.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, runtime, os, machine, arch, message string
		code                                      int
	}{
		{"removed runtime", "podman", "Linux", "aarch64", "arm64", "CONTAINER_RUNTIME must be docker", 2},
		{"macOS requires laptop launcher", "docker", "Darwin", "arm64", "arm64", "requires native Linux with Docker and KVM", 1},
		{"crossed architecture", "docker", "Linux", "x86_64", "arm64", "requires a native arm64 host", 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{
				"scripts/nvke-os-local.sh": string(source),
				"bin/docker":               "#!/bin/sh\ntouch \"$TEST_RUNTIME_MARKER\"\nexit 99\n",
				"bin/uname":                "#!/bin/sh\ncase \"$1\" in -s) echo \"$TEST_HOST_OS\";; -m) echo \"$TEST_MACHINE\";; *) exit 99;; esac\n",
			}
			for path, contents := range files {
				path = filepath.Join(root, path)
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(root, "runtime-called")
			cmd := exec.CommandContext(t.Context(), "bash", filepath.Join(root, "scripts/nvke-os-local.sh"), "test-attempt")
			cmd.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"),
				"CONTAINER_RUNTIME="+test.runtime, "NVKE_OS_ARCH="+test.arch,
				"TEST_HOST_OS="+test.os, "TEST_MACHINE="+test.machine, "TEST_RUNTIME_MARKER="+marker)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != test.code || !strings.Contains(string(output), test.message) {
				t.Fatalf("route admission: %v: %s", err, output)
			}
			for _, path := range []string{marker, filepath.Join(root, ".nvkr")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("unsupported route performed work at %s: %v", path, err)
				}
			}
		})
	}
}
