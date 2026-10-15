// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	yaml "go.yaml.in/yaml/v3"
)

func TestQualificationRejectsProducerChildNamespaceBeforeInputIO(t *testing.T) {
	for _, workflow := range []string{
		".github/workflows/nvkr-run__host-amd64-node.yml",
		".github/workflows/nvkr-dev__host-amd64-node.yml",
		".github/workflows/nvkr-run-nightly__host-amd64-node.yml",
	} {
		for _, role := range []string{"qualifier", "worker"} {
			t.Run(workflow+"/"+role, func(t *testing.T) {
				plan, policy := generationFixture(t)
				if role == "qualifier" {
					policy.QualificationWorkflow = workflow
				} else {
					policy.Smoke.Workflow = workflow
				}
				bundle, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
				requireCode(t, err, e.CodeEngineCapability)
				if bundle != nil {
					t.Fatal("reserved namespace emitted qualification files")
				}
				inputs := t.TempDir()
				raw, err := yaml.Marshal(policy)
				if err != nil {
					t.Fatal(err)
				}
				writeQualificationTestFile(t, inputs, "qualification.yaml", raw)
				t.Chdir(inputs)
				for _, check := range []bool{false, true} {
					output := filepath.Join(t.TempDir(), "uncreated")
					digest, err := GenerateFiles(t.Context(), "missing-definition.yaml", "missing-environment.yaml", "qualification.yaml", plan.GeneratorVersion, "github", output, check)
					requireCode(t, err, e.CodeEngineCapability)
					if digest != "" {
						t.Fatal("rejected generation returned a Plan digest")
					}
					if _, err := os.Lstat(output); !os.IsNotExist(err) {
						t.Fatal("reserved namespace created output")
					}
				}
			})
		}
	}
}

func TestQualificationManifestCannotOwnProducerChildren(t *testing.T) {
	for _, role := range []string{"qualifier", "worker"} {
		t.Run(role, func(t *testing.T) {
			bundle, policy := generationBundle(t)
			root := t.TempDir()
			mustWriteQualification(t, root, bundle)
			manifest := bundle.manifest
			manifest.Members = append([]factory.GeneratedMember(nil), manifest.Members...)
			priorPath := manifest.QualificationWorkflow
			child := ".github/workflows/nvkr-run__host-amd64-node.yml"
			if role == "qualifier" {
				manifest.QualificationWorkflow = child
			} else {
				priorPath = manifest.SmokeWorkflow
				manifest.SmokeWorkflow = child
			}
			for index := range manifest.Members {
				if manifest.Members[index].Path == priorPath {
					manifest.Members[index].Path = child
				}
			}
			slices.SortFunc(manifest.Members, func(a, b factory.GeneratedMember) int { return strings.Compare(a.Path, b.Path) })
			raw, err := canonical.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			writeQualificationTestFile(t, root, QualificationManifestPath, append(raw, '\n'))
			requireCode(t, CheckGeneratedPolicy(t.Context(), root, policy), e.CodePipelineDrift)
			requireCode(t, WriteWorkflows(t.Context(), root, bundle), e.CodePipelineDrift)
		})
	}
}

func TestQualificationCustomWorkflowNamespaceRemainsSupported(t *testing.T) {
	plan, policy := generationFixture(t)
	policy.QualificationWorkflow = ".github/workflows/operator-qualification.yml"
	policy.Smoke.Workflow = ".github/workflows/operator-smoke.yml"
	if _, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml"); err != nil {
		t.Fatal(err)
	}
}
