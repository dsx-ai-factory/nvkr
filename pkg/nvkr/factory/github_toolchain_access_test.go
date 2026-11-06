// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubForeignToolchainsReceiveNoReader(t *testing.T) {
	for _, root := range []string{"ghcr.io/foreign/nvkr/toolchains", "ghcr.io/example/nvkr/toolchains-mirror"} {
		t.Run(root, func(t *testing.T) {
			base := privateImageTestPlan(t, ProductionRegistryWriteCredential)
			foreign := recompileGitHubStabilityPlan(t, base, func(in *githubStabilityInputs) {
				component := in.catalog.Components["image"]
				for arch, image := range component.ToolchainImages {
					component.ToolchainImages[arch] = root + "/packer-" + arch + image[strings.Index(image, "@sha256:"):]
				}
				in.catalog.Components["image"] = component
			})
			bundle, err := RenderGitHub(t.Context(), foreign)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if bundle != nil {
				t.Fatal("unclassified foreign root emitted a bundle")
			}
			public := recompileGitHubStabilityPlan(t, foreign, func(in *githubStabilityInputs) {
				in.environment.ToolchainAccess.PublicRoots = append(in.environment.ToolchainAccess.PublicRoots, root)
				slices.Sort(in.environment.ToolchainAccess.PublicRoots)
			})
			bundle, err = RenderGitHub(t.Context(), public)
			if err != nil {
				t.Fatal(err)
			}
			assertNoPrivateToolchainPreparation(t, bundle)
		})
	}
}

func TestGitHubToolchainAccessRefusesBeforeGeneration(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*githubStabilityInputs)
		message string
	}{
		{"missing declaration", func(in *githubStabilityInputs) { in.environment.ToolchainAccess = nil }, "no declared toolchainAccess root"},
		{"undeclared private root", func(in *githubStabilityInputs) {
			in.environment.ToolchainAccess.PrivateRoots = []string{"ghcr.io/foreign/tools"}
		}, "no declared toolchainAccess root"},
		{"missing reader", func(in *githubStabilityInputs) { in.environment.ToolchainAccess.Reader = nil }, "declared toolchainAccess reader"},
		{"undeclared public root", func(in *githubStabilityInputs) { in.environment.ToolchainAccess.PublicRoots = nil }, "no declared toolchainAccess root"},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := recompileGitHubStabilityPlan(t, privateImageTestPlan(t, ProductionRegistryWriteCredential), test.mutate)
			bundle, err := RenderGitHub(t.Context(), plan)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if bundle != nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("generation must refuse with no bundle: %v, %v", bundle, err)
			}
			assertFactoryErrorCode(t, admitGitHubPlan(t.Context(), plan), e.CodeEngineCapability)
		})
	}
}

func TestGitHubToolchainAccessIndependentOfPublication(t *testing.T) {
	for _, publication := range []bool{false, true} {
		t.Run(map[bool]string{false: "build only", true: "unrelated publication"}[publication], func(t *testing.T) {
			base := privateImageTestPlan(t, ProductionRegistryWriteCredential)
			plan := recompileGitHubStabilityPlan(t, base, func(in *githubStabilityInputs) {
				in.environment.ToolchainAccess.Reader.CredentialRef = "github:DECLARED_READER"
				in.environment.ToolchainAccess.Reader.UsernameRef = "github:DECLARED_ACCOUNT"
				if publication {
					in.environment.Publication.RepositoryPrefix = "ghcr.io/other/new-product"
				} else {
					in.environment.Publication = nil
					in.environment.Signing = nil
					in.definition.CosignTool = ""
				}
			})
			if plan.Builds[0].BuildDigest != base.Builds[0].BuildDigest {
				t.Fatal("transport access changed logical Build identity")
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			for _, doc := range githubEmittedWorkflows(t, plan, bundle) {
				for _, job := range doc.Jobs {
					for _, step := range job.Steps {
						if strings.Contains(step.Run, "factory github prepare-images ") {
							calls++
							if step.Env[githubImageToken] != "${{ secrets.DECLARED_READER }}" || step.Env[githubImageUser] != "${{ vars.DECLARED_ACCOUNT }}" {
								t.Fatalf("wrong reader: %v", step.Env)
							}
						}
					}
				}
			}
			if calls == 0 {
				t.Fatal("private selection omitted preparation")
			}
		})
	}
}

func TestGitHubToolchainAccessPrivateInspectorOnly(t *testing.T) {
	for _, reader := range []bool{true, false} {
		t.Run(map[bool]string{true: "reader admitted", false: "reader missing"}[reader], func(t *testing.T) {
			var inspector string
			plan := recompileGitHubStabilityPlan(t, arcTestPlan(t, "linux/amd64"), func(in *githubStabilityInputs) {
				profile := in.environment.DiskInspectionProfiles["linux/amd64"]
				inspector = in.environment.ToolchainAccess.PrivateRoots[0] + "/inspector@sha256:" + strings.Repeat("a", 64)
				profile.Image = inspector
				in.environment.DiskInspectionProfiles["linux/amd64"] = profile
				if !reader {
					in.environment.ToolchainAccess.Reader = nil
				}
			})
			if !githubDiskInspectionRequired(plan) {
				t.Fatal("fixture does not require strict inspection")
			}
			publicProducers := 0
			for _, node := range plan.Graph.Nodes {
				execution, err := plan.GitHubExecutionFor(node)
				if err != nil {
					t.Fatal(err)
				}
				if execution.Container == "" {
					continue
				}
				if execution.Container == inspector && node.Kind == NodeSBOM {
					continue
				}
				if private, err := plan.Environment.ToolchainAccess.ClassifyImage(execution.Container); err != nil || private {
					t.Fatalf("fixture has another private or unclassified image: %s, %v", execution.Container, err)
				}
				if node.Kind == NodeBuild {
					publicProducers++
				}
			}
			if publicProducers == 0 {
				t.Fatal("fixture has no public producer image")
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if !reader {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if bundle != nil || !strings.Contains(err.Error(), "reader") {
					t.Fatalf("missing inspector reader must refuse before output: %v, %v", bundle, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			prepared := false
			for _, document := range githubEmittedWorkflows(t, plan, bundle) {
				for _, job := range document.Jobs {
					for _, step := range job.Steps {
						if strings.Contains(step.Run, "factory github prepare-images ") {
							prepared = true
							if step.Env[githubImageToken] != "${{ secrets.NVKR_REGISTRY_READ }}" || step.Env[githubImageUser] != "${{ vars.NVKR_REGISTRY_READ_USER }}" {
								t.Fatalf("private inspector omitted its reader: %v", step.Env)
							}
						}
					}
				}
			}
			if !prepared {
				t.Fatal("private inspector omitted preparation")
			}
		})
	}
}
