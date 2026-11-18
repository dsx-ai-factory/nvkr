// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// The small guest exercises the production owner and consumer, without claiming
// the maintained NVKE payload, hosted provider admission or publication.
func nativeRuntimeProofPlan(t *testing.T, directory, commit, tree string) *Plan {
	t.Helper()
	input, environment, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityRequired)
	component := stack.Catalog.Components["image"]
	component.Repo, component.Commit, component.TreeHash = "https://github.com/njhensley/nvkr", commit, tree
	component.Tag = "v0.0.0" // Fixture version; source identity is the exact commit/tree below.
	component.BuildCommand = nil
	component.Platforms = []string{"linux/amd64"}
	component.BuildNetwork = ir.NetworkDefault
	component.ArtifactPath = "build/disk.qcow2"
	component.BuildRequirements.Resources = &execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 1024, EphemeralStorageMiB: 16384}
	component.BuildRequirements.TimeoutSeconds = 600
	component.Packer = &catalog.PackerConfig{
		ProjectPath: "pkg/nvkr/factory/testdata/runtime-proof", PackerVersion: "1.16.2", BuildName: "proof.qemu.disk",
		ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator",
		MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory",
		BuildLogDirectoryVariable: "build_log_directory",
		SSHPrivateKeyVariable:     "ssh_private_key_file", SSHPublicKeyVariable: "ssh_public_key_file",
	}
	stack.Catalog.Components["image"] = component
	stack, err := catalog.ApplyStack(stack.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(directory))
	if err != nil {
		t.Fatal(err)
	}
	runtimeStack, err := catalog.ApplyStack(cat)
	if err != nil {
		t.Fatal(err)
	}
	input.InternalTools = []definition.InternalTool{{Name: "runtime", Format: definition.OutputOCI,
		Platform: "linux/amd64", Components: []string{"packer-amd64", "packer-plugin-qemu-amd64", "vm-runtime-amd64"},
		Result: "vm-runtime-amd64", Catalog: &definition.Catalog{Base: "runtime-catalog"}}}
	environment.Runtime.ProducedRuntimes = map[string]string{"node-image": "runtime"}
	environment.Runtime.BuildNetworkPolicy = definition.BuildNetworkProducerDeclared
	environment.ExecutionClasses = []execution.Class{{Name: "bounded-proof", RunsOn: "arc-amd64-kvm",
		HostPlatform: execution.HostPlatformLinuxAMD64,
		Resources:    execution.ResourceCapacity{CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 16384},
		Capabilities: []execution.CapabilityName{execution.CapabilityKVM}, MaxTimeoutSeconds: 600}}
	plan, err := compileWithTargetStacks(t.Context(), input, environment, stack,
		map[string]*catalog.Stack{"internal/runtime": runtimeStack}, "runtime-proof", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestNativeRuntimeProofPlan(t *testing.T) {
	directory, err := filepath.Abs("../../../toolchains/nvkr-vm-runtime")
	if err != nil {
		t.Fatal(err)
	}
	plan := nativeRuntimeProofPlan(t, directory, strings.Repeat("a", 40), strings.Repeat("b", 40))
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if plan.Qualification != TierStaticUnqualified || len(plan.InternalTools) != 1 || len(plan.LifecycleOutputs) != 1 {
		t.Fatal("proof changed its evidence lane or runtime membership")
	}
	for _, node := range topologicalFactoryNodes(plan.Graph) {
		t.Logf("%s (%s), target=%s", node.ID, node.Kind, node.BuildTarget)
		if node.Kind != NodeFetch && node.Kind != NodeVerify && node.ID != "node-image/build-image" && node.BuildTarget != "internal/runtime" {
			t.Fatalf("unsupported proof operation %s", node.ID)
		}
	}
}
