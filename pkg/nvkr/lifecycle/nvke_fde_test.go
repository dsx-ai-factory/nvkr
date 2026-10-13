// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestNVKEFDEOnlyGeneratedSelection(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	const definition = "definitions/nvke-fde/definition.yaml"
	const environment = "definitions/nvke-fde/environment-github.yaml"
	plan, err := CompileFiles(t.Context(), definition, environment, "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Name != "nvke-node-fde-amd64" || len(plan.Builds) != 1 {
		t.Fatal("FDE iteration must select exactly one named disk build")
	}
	if plan.Environment.Publication != nil || plan.Environment.Signing != nil || plan.Qualification != factory.TierStaticUnqualified {
		t.Fatal("FDE iteration must remain build-only and static-unqualified")
	}
	if len(plan.Environment.GitHubDispatchBranches) != 1 || plan.Environment.GitHubDispatchBranches[0] != "codex/fde-image-pipeline" {
		t.Fatal("FDE iteration lost its exact feature-branch dispatch policy")
	}
	roots := 0
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.BuilderType != ir.BuilderVMImage {
			continue
		}
		roots++
		if node.Component != "nvke-node-fde-amd64" || node.Disk.Encryption != "luks2" || node.ArtifactPath != "fde/nke-node.qcow2" ||
			node.Packer.BuildName != "nke-node-fde.qemu.nke-fde" || node.SourceCommit != "ff21ba8c07cafff74b68e238a8876af11e5beded" {
			t.Fatal("FDE producer no longer selects the audited source contract")
		}
		if err := pipeline.ValidateVMImagePackerDiskExecution(node); err != nil {
			t.Fatal(err)
		}
	}
	if roots != 1 || len(plan.Builds[0].ResolvedComponents) != 11 {
		t.Fatal("FDE iteration lost its exact root and material closure")
	}
	root := t.TempDir()
	digest, err := GenerateFiles(t.Context(), definition, environment, "test (deadbeef)", factory.EngineGitHub, root, false)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := GenerateFiles(t.Context(), definition, environment, "test (deadbeef)", factory.EngineGitHub, root, true)
	if err != nil || checked != digest {
		t.Fatalf("FDE generation drift: %s/%s, %v", digest, checked, err)
	}
}
