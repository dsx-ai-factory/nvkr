// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestArtifactTestInputCommand(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, output := range []string{"nvkectl", "nvkectl-linux-arm64", "missing", ""} {
		t.Run(output, func(t *testing.T) {
			args := []string{"run", "artifact-test-inputs", "--plan", ".github/nvkr-run/plan.json"}
			if output != "" {
				args = append(args, "--output", output)
			}
			raw, err := invokeLifecycleCLI(t, args...)
			if output == "missing" || output == "" {
				assertLifecycleCommandCode(t, err, e.CodeUsage)
				if raw != "" {
					t.Fatalf("invalid selection produced output: %q, %v", raw, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var inputs factory.CLIArtifactTestInputs
			if err := json.Unmarshal([]byte(raw), &inputs); err != nil {
				t.Fatal(err)
			}
			plan, err := factory.LoadPlanContext(t.Context(), ".github/nvkr-run/plan.json")
			if err != nil {
				t.Fatal(err)
			}
			var want factory.CLIArtifactTestInputs
			for _, build := range plan.Builds {
				if build.Name != output {
					continue
				}
				component := build.CatalogSnapshot.Components["nvkectl"]
				want = factory.CLIArtifactTestInputs{Output: output, BuildDigest: build.BuildDigest, Platform: build.Platform, Repo: component.Repo, Commit: component.Commit, TreeHash: component.TreeHash, ExpectedVersion: component.VersionStamp["NKECTL_VERSION"]}
				for _, node := range build.Graph.Nodes {
					if node.Kind == ir.KindBuild && node.Component == "nvkectl" {
						want.ToolchainImage = node.Toolchain
					}
				}
			}
			if inputs != want {
				t.Fatalf("wire inputs: %+v, want %+v", inputs, want)
			}
		})
	}
}

func TestArtifactTestInputCommandRefusals(t *testing.T) {
	lifecycleCommandRepo(t)
	malformed := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(malformed, []byte(`{"schemaVersion":`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		code e.Code
	}{
		{"positional", []string{"--plan", ".github/nvkr-run/plan.json", "extra"}, e.CodeUsage},
		{"malformed", []string{"--plan", malformed}, e.CodePipelineDrift},
		{"missing file", []string{"--plan", "missing.json"}, e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := invokeLifecycleCLI(t, append([]string{"run", "artifact-test-inputs"}, tc.args...)...)
			assertLifecycleCommandCode(t, err, tc.code)
			if raw != "" {
				t.Fatalf("refusal emitted %q", raw)
			}
		})
	}
}

func TestArtifactTestInputCommandFlags(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, args := range [][]string{
		{"run", "artifact-test-inputs"},
		{"run", "artifact-test-inputs", "--unknown"},
		{"run", "artifact-test-inputs", "--plan"},
	} {
		raw, err := invokeLifecycleCLI(t, args...)
		if err == nil || json.Valid([]byte(raw)) {
			t.Fatalf("invalid flags emitted success: %q, %v", raw, err)
		}
	}
}
