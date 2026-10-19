// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/testutil"
)

func TestGitHubOperationLabels(t *testing.T) {
	for _, tc := range []struct {
		name string
		node Node
		want string
	}{
		{"image", Node{ID: "package/worker", Kind: NodePackage, PackageKind: PackageImage}, "Package container image: worker"},
		{"helm binding", Node{ID: "package/charts", Kind: NodePackage, PackageKind: PackageHelm, HelmImage: testHelmBinding("package/worker", "controller")}, "Bind image digest and package Helm: charts"},
		{"helm alone", Node{ID: "package/charts", Kind: NodePackage, PackageKind: PackageHelm}, "Package Helm bundle: charts"},
		{"binary", Node{ID: "package/cli", Kind: NodePackage, PackageKind: PackageBinaryBundle}, "Package binaries: cli"},
		{"apt", Node{ID: "package/packages", Kind: NodePackage, PackageKind: PackageAPT}, "Package APT repository: packages"},
		{"publication bundle", Node{ID: "package/worker-release", Kind: NodePackage, PackageKind: PackageOCI}, "Package publication bundle: worker-release"},
		{"observation", Node{Kind: NodeVerify, BuildTarget: "worker"}, "Verify built artifacts: worker"},
		{"sbom", Node{Kind: NodeSBOM, BuildTarget: "worker"}, "Generate SBOMs: worker"},
		{"publish", Node{ID: "publish/worker", Kind: NodePublish}, "Publish worker"},
		{"sign", Node{ID: "sign/worker", Kind: NodeSign}, "Sign and attest release and deliverables: worker"},
		{"verify publication", Node{ID: "verify-publication/worker", Kind: NodeVerifyPublication}, "Verify release and deliverable signatures: worker"},
		{"tool", Node{ID: "tool/cosign", Kind: NodeAcquire}, "Acquire pinned cosign"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := githubNodeLabel(&Plan{}, tc.node); got != tc.want {
				t.Fatalf("label = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestMaintainedWorkflowMakesImageBindingVisible(t *testing.T) {
	testutil.RequireExtended(t)
	plan, err := LoadPlanContext(t.Context(), "../../../.github/nvkr-run/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	found := false
	for _, job := range document.Jobs {
		if strings.Contains(job.Name, "terminal-observe") || strings.Contains(job.Name, "fetch-source-") {
			t.Errorf("internal identity used as job label: %s", job.Name)
		}
		for _, step := range job.Steps {
			if step.Name == "" {
				t.Errorf("unnamed step in %s", job.Name)
			}
			if step.ID == githubJobID("package/controller-chart") {
				found = true
				if job.Name != "Bind image digest and package Helm: controller-chart" || step.Name != job.Name {
					t.Fatalf("binding hidden: %s / %s", job.Name, step.Name)
				}
			}
		}
	}
	if !found {
		t.Fatal("maintained workflow omitted Helm binding")
	}
}
