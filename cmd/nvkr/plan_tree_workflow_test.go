// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"go.yaml.in/yaml/v3"
)

func TestNativeToolchainWorkflowSelectsPlanTreePins(t *testing.T) {
	root := filepath.Dir(filepath.Dir(demoScript(t, "nvke-os-local.sh")))
	raw, err := os.ReadFile(filepath.Join(root, ".github/workflows/nvkr-native-toolchains.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct{ Jobs map[string]osWorkflowJob }
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	var script string
	for _, step := range workflow.Jobs["verify-published"].Steps {
		if step.ID == "pins" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("missing published-image selection step")
	}
	helperDirectory := t.TempDir()
	build := exec.CommandContext(t.Context(), "go", "build", "-o", filepath.Join(helperDirectory, "nvkr-plan-inspect"), "./cmd/nvkr")
	build.Dir = root
	if raw, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Plan inspection helper: %v: %s", err, raw)
	}
	const planPath = ".github/nvkr-run-nvke/plan.json"
	plan, err := factory.LoadPlanContext(t.Context(), filepath.Join(root, planPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output")
			command := exec.CommandContext(t.Context(), "bash", "-euc", script)
			command.Dir = root
			command.Env = append(os.Environ(), "NATIVE_ARCH="+arch, "GITHUB_OUTPUT="+output, "RUNNER_TEMP="+helperDirectory)
			if raw, err := command.CombinedOutput(); err != nil {
				t.Fatalf("pin selection failed: %v: %s", err, raw)
			}
			selected := map[string]bool{}
			platform := "linux/" + arch
			for _, build := range plan.Builds {
				if build.Platform != platform {
					continue
				}
				for _, node := range build.Graph.Nodes {
					if strings.HasPrefix(node.Toolchain, "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer-native@") {
						selected[node.Toolchain] = true
					}
				}
			}
			if len(selected) != 1 {
				t.Fatal("Plan does not select one Packer image")
			}
			raw, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			for pin := range selected {
				want := "packer=" + pin + "\ninspector=" + plan.Environment.DiskInspectionProfiles[platform].Image + "\n"
				if string(raw) != want {
					t.Fatalf("workflow pins differ from Plan: %s", raw)
				}
			}
		})
	}
	bundle, err := factory.RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"missing frozen record", "altered frozen record"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			if err := factory.WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
				t.Fatal(err)
			}
			var member string
			for name := range bundle.Files {
				if strings.HasPrefix(name, factory.PlanStoreDirectory+"/") && strings.HasSuffix(name, ".json") {
					member = filepath.Join(root, name)
					break
				}
			}
			if member == "" {
				t.Fatal("fixture has no frozen record")
			}
			if scenario == "missing frozen record" {
				err = os.Rename(member, filepath.Join(root, "saved-object"))
			} else {
				err = os.WriteFile(member, []byte("{}\n"), 0644)
			}
			if err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "output")
			command := exec.CommandContext(t.Context(), "bash", "-euc", script)
			command.Dir = root
			command.Env = append(os.Environ(), "NATIVE_ARCH=amd64", "GITHUB_OUTPUT="+output, "RUNNER_TEMP="+helperDirectory)
			if raw, err := command.CombinedOutput(); err == nil {
				t.Fatalf("invalid frozen Plan accepted: %s", raw)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("invalid frozen Plan wrote output: %v", err)
			}
		})
	}
}
