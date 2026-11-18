// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestRuntimeProducerSelectionRequiresExactNativeInternalClosure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Plan, *definition.Environment)
	}{
		{"missing declaration", func(p *Plan, _ *definition.Environment) { p.Definition.InternalTools = nil }},
		{"unknown tool", func(_ *Plan, env *definition.Environment) { env.Runtime.ProducedRuntimes["linux/amd64"] = "unknown" }},
		{"wrong host", func(_ *Plan, env *definition.Environment) {
			env.Runtime.ProducedRuntimes = map[string]string{"linux/arm64": "runtime"}
		}},
		{"wrong result", func(p *Plan, _ *definition.Environment) { p.Definition.InternalTools[0].Result = "packer-amd64" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := runtimeOwnerPlanFixture(t)
			env := definition.Environment{Runtime: definition.Runtime{ProducedRuntimes: map[string]string{"linux/amd64": "runtime"}}}
			tc.change(plan, &env)
			got, err := selectedRuntimeProducers(plan.Definition, env, plan.Builds)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if got != nil {
				t.Fatal("invalid selection returned partial runtime bindings")
			}
		})
	}
}

func runtimeConsumerPlanFixture(t *testing.T) *Plan {
	t.Helper()
	producer := runtimeOwnerPlanFixture(t)
	input, env, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityRequired)
	component := stack.Catalog.Components["image"]
	component.BuildCommand = nil
	component.Packer = &catalog.PackerConfig{ProjectPath: ".", PackerVersion: "1.16.2", BuildName: "image.qemu.node",
		ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator",
		MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory"}
	stack.Catalog.Components["image"] = component
	stack, err := catalog.ApplyStack(stack.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := filepath.Abs("../../../toolchains/nvkr-vm-runtime")
	if err != nil {
		t.Fatal(err)
	}
	runtimeCatalog, err := catalog.LoadContext(t.Context(), catalog.Dir(directory))
	if err != nil {
		t.Fatal(err)
	}
	runtimeStack, err := catalog.ApplyStack(runtimeCatalog)
	if err != nil {
		t.Fatal(err)
	}
	input.InternalTools = producer.Definition.InternalTools
	input.InternalTools[0].Catalog = &definition.Catalog{Base: "runtime-catalog"}
	env.Runtime.ProducedRuntimes = map[string]string{"linux/amd64": "runtime"}
	plan, err := compileWithTargetStacks(t.Context(), input, env, stack, map[string]*catalog.Stack{"internal/runtime": runtimeStack}, "test", "", "")
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestRuntimeConsumerPlanRequiresProductionAndEvidenceInputs(t *testing.T) {
	plan := runtimeConsumerPlanFixture(t)
	tool := plan.InternalTools[0]
	var consumer Node
	for _, node := range plan.Graph.Nodes {
		if node.ID == "node-image/build-image" {
			consumer = node
		}
	}
	if !slices.Equal(consumer.InternalTools, []string{"runtime"}) {
		t.Fatal("consumer has no exact runtime requirement")
	}
	for _, input := range tool.inputs() {
		if !slices.Contains(consumer.Inputs, input) {
			t.Fatalf("consumer omitted runtime input %s", input)
		}
	}
	if len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Name != "node-image" {
		t.Fatal("runtime became a delivered output")
	}
	execution, err := plan.ExecutionFor(consumer)
	if err != nil {
		t.Fatal(err)
	}
	if execution.Container != "" {
		t.Fatal("produced runtime entered image execution")
	}
	// Compilation is descriptive. No existing local or hosted executor may
	// treat the new reference as a container or a reconstructed capability.
	assertFactoryErrorCode(t, admitInternalToolExecution(plan), e.CodeEngineCapability)
	clone := cloneDiskProducerInstruction(plan.Builds[0].Graph.Nodes[0])
	if clone.Runtime != nil {
		clone.Runtime.Tool = "changed"
		if plan.Builds[0].Graph.Nodes[0].Runtime.Tool != "runtime" {
			t.Fatal("disk instruction clone aliases runtime selection")
		}
	}
	for i := range plan.Graph.Nodes {
		if plan.Graph.Nodes[i].ID == consumer.ID {
			plan.Graph.Nodes[i].Inputs = slices.DeleteFunc(plan.Graph.Nodes[i].Inputs, func(value string) bool { return value == tool.SBOMArtifactID })
		}
	}
	assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
}

func TestRuntimeEvidenceSchedulingCannotAuthorizeOtherOperations(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Node)
	}{
		{"image fallback", func(n *Node) { n.Container = fixtureUtilityContainer }},
		{"network", func(n *Node) { n.Network = ir.NetworkDefault }},
		{"wrong host", func(n *Node) { n.Platform = "linux/arm64" }},
		{"foreign owner", func(n *Node) { n.RuntimeEvidence = "sibling" }},
		{"publication", func(n *Node) { n.Kind = NodePublish }},
		{"nested tool use", func(n *Node) { n.InternalTools = []string{"other"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := runtimeConsumerPlanFixture(t)
			for i := range plan.Graph.Nodes {
				if plan.Graph.Nodes[i].ID == "internal/runtime/sbom" {
					tc.change(&plan.Graph.Nodes[i])
				}
			}
			assertFactoryErrorCode(t, plan.Graph.Validate(), e.CodeEngineCapability)
		})
	}
}

func TestReleaseCompanionSchedulerNamesPreserveExactFilenames(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range []string{"license.txt", "LICENSE.txt", "License.txt", "name", "4c4943454e53452e747874"} {
		id := releaseCompanionID("internal/runtime", "packer", name)
		if !safeGraphID(id) || seen[id] {
			t.Fatalf("companion %q has unsafe or colliding ID %q", name, id)
		}
		seen[id] = true
	}
	plan := runtimeConsumerPlanFixture(t)
	count := 0
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.Kind != ArtifactReleaseCompanion {
			continue
		}
		if artifact.Name != "LICENSE.txt" || filepath.Base(artifact.Path) != artifact.Name {
			t.Fatal("scheduler encoding renamed a license file")
		}
		count++
	}
	if count != 2 {
		t.Fatalf("license companions = %d, want both pinned tool licenses", count)
	}
}

func TestRuntimeConsumerCatalogRevalidationBindsProducerAndEnvironment(t *testing.T) {
	producer := runtimeOwnerPlanFixture(t)
	input, env, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityRequired)
	component := stack.Catalog.Components["image"]
	component.BuildCommand = nil
	component.Packer = &catalog.PackerConfig{ProjectPath: ".", PackerVersion: "1.16.2", BuildName: "image.qemu.node",
		ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator",
		MaterialManifestVariable: "material_manifest", OutputDirectoryVariable: "output_directory"}
	stack.Catalog.Components["image"] = component
	stack, err := catalog.ApplyStack(stack.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	env.Runtime.ProducedRuntimes = map[string]string{"linux/amd64": "runtime"}
	runtimes, err := selectedRuntimeProducers(producer.Definition, env, producer.Builds)
	if err != nil {
		t.Fatal(err)
	}
	build, err := compileOutputBuild(input.Outputs[0], env, stack, runtimes)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateBuildCatalog(build, input.Outputs[0], env, runtimes); err != nil {
		t.Fatal(err)
	}
	_, err = compileOutputBuild(input.Outputs[0], env, stack, nil)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	selection := runtimes["linux/amd64"]
	if selection.BuildDigest != producer.Builds[0].BuildDigest || selection.Invocation != ir.RuntimeInvocation {
		t.Fatal("consumer did not derive its runtime from the producer graph")
	}
	selection.BuildDigest = "sha256:" + strings.Repeat("c", 64)
	runtimes["linux/amd64"] = selection
	err = validateBuildCatalog(build, input.Outputs[0], env, runtimes)
	assertFactoryErrorCode(t, err, e.CodeUnverifiedRef)
	env.Runtime.ProducedRuntimes = nil
	err = validateBuildCatalog(build, input.Outputs[0], env, nil)
	assertFactoryErrorCode(t, err, e.CodeUnverifiedRef)
}
