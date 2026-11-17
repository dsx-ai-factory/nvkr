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
	plan, err := factory.LoadPlanContext(t.Context(), filepath.Join(root, factory.FactoryPlanPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "output")
			command := exec.CommandContext(t.Context(), "bash", "-euc", script)
			command.Dir = root
			command.Env = append(os.Environ(), "NATIVE_ARCH="+arch, "GITHUB_OUTPUT="+output)
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
	for _, test := range []struct{ name, step string }{
		{"empty steps", ""},
		{"mutable Packer pin", `{"platform":"linux/amd64","executionGraph":{"nodes":[{"toolchain":"ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer-native@latest"}]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			steps := filepath.Join(root, ".github/nvkr-run/steps")
			if err := os.MkdirAll(steps, 0755); err != nil {
				t.Fatal(err)
			}
			if test.step != "" {
				if err := os.WriteFile(filepath.Join(steps, "step.json"), []byte(test.step), 0644); err != nil {
					t.Fatal(err)
				}
			}
			// A valid inspector lets an empty Packer selection reach the output
			// on xargs implementations that skip the command for empty input.
			rootPlan := `{"environment":{"diskInspectionProfiles":{"linux/amd64":{"image":"ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/inspector-native@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}}`
			if err := os.WriteFile(filepath.Join(root, factory.FactoryPlanPath), []byte(rootPlan), 0644); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "output")
			command := exec.CommandContext(t.Context(), "bash", "-euc", script)
			command.Dir = root
			command.Env = append(os.Environ(), "NATIVE_ARCH=amd64", "GITHUB_OUTPUT="+output)
			if raw, err := command.CombinedOutput(); err == nil {
				t.Fatalf("invalid Packer selection accepted: %s", raw)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Fatalf("invalid Packer selection wrote output: %v", err)
			}
		})
	}
}
