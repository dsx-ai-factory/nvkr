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
