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
		{"unknown tool", func(_ *Plan, env *definition.Environment) { env.Runtime.ProducedRuntimes["node-image"] = "unknown" }},
		{"unknown output", func(_ *Plan, env *definition.Environment) {
			env.Runtime.ProducedRuntimes = map[string]string{"unknown": "runtime"}
		}},
		{"non-disk output", func(p *Plan, _ *definition.Environment) { p.Definition.Outputs[0].Format = definition.OutputOCI }},
		{"shared requirement", func(p *Plan, env *definition.Environment) {
			p.Definition.Outputs = append(p.Definition.Outputs, definition.DefinitionOutput{Name: "second", Format: definition.OutputDiskImage})
			env.Runtime.ProducedRuntimes["second"] = "runtime"
		}},
		{"wrong result", func(p *Plan, _ *definition.Environment) { p.Definition.InternalTools[0].Result = "packer-amd64" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := runtimeOwnerPlanFixture(t)
			plan.Definition.Outputs = []definition.DefinitionOutput{{Name: "node-image", Format: definition.OutputDiskImage}}
			env := definition.Environment{Runtime: definition.Runtime{ProducedRuntimes: map[string]string{"node-image": "runtime"}}}
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
	return runtimeConsumersPlanFixture(t, []string{"node-image"})
}

func runtimeConsumersPlanFixture(t *testing.T, outputs []string) *Plan {
	t.Helper()
	outputs = slices.Clone(outputs)
	slices.Sort(outputs)
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
	target := input.Outputs[0]
	input.Outputs = nil
	env.Runtime.ProducedRuntimes = make(map[string]string, len(outputs))
	stacks := make(map[string]*catalog.Stack, len(outputs))
	for _, output := range outputs {
		tool := producer.Definition.InternalTools[0]
		if len(outputs) > 1 {
			tool.Name = "runtime-" + output
		}
		tool.Catalog = &definition.Catalog{Base: "runtime-catalog"}
		input.InternalTools = append(input.InternalTools, tool)
		target.Name = output
		input.Outputs = append(input.Outputs, target)
		env.Runtime.ProducedRuntimes[output] = tool.Name
		stacks["internal/"+tool.Name] = runtimeStack
	}
	plan, err := compileWithTargetStacks(t.Context(), input, env, stack, stacks, "test", "", "")
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
		{"unknown output", func(n *Node) { n.Platform = "linux/arm64" }},
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
	env.Runtime.ProducedRuntimes = map[string]string{"node-image": "runtime"}
	producer.Definition.Outputs = input.Outputs
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
	selection := runtimes["node-image"]
	if selection.BuildDigest != producer.Builds[0].BuildDigest || selection.Invocation != ir.RuntimeInvocation {
		t.Fatal("consumer did not derive its runtime from the producer graph")
	}
	selection.BuildDigest = "sha256:" + strings.Repeat("c", 64)
	runtimes["node-image"] = selection
	err = validateBuildCatalog(build, input.Outputs[0], env, runtimes)
	assertFactoryErrorCode(t, err, e.CodeUnverifiedRef)
	env.Runtime.ProducedRuntimes = nil
	err = validateBuildCatalog(build, input.Outputs[0], env, nil)
	assertFactoryErrorCode(t, err, e.CodeUnverifiedRef)
}

func TestFourRuntimeConsumersKeepIndependentProductionRequirements(t *testing.T) {
	outputs := []string{"fde", "fde-ubuntu24", "image", "image-ubuntu24"}
	plan := runtimeConsumersPlanFixture(t, outputs)
	if len(plan.InternalTools) != len(outputs) || len(plan.LifecycleOutputs) != len(outputs) {
		t.Fatal("internal runtime requirements changed delivered output membership")
	}
	seen := map[string]bool{}
	digest := plan.InternalTools[0].BuildDigest
	for _, tool := range plan.InternalTools {
		if tool.BuildDigest != digest {
			t.Fatal("separate uses changed runtime content identity")
		}
		for _, input := range tool.inputs() {
			if seen[input] {
				t.Fatalf("runtime requirements share evidence or artifact %q", input)
			}
			seen[input] = true
		}
	}
	for _, output := range outputs {
		tool := "runtime-" + output
		found := false
		for _, node := range plan.Graph.Nodes {
			if node.ID != output+"/build-image" {
				continue
			}
			found = true
			if !slices.Equal(node.InternalTools, []string{tool}) {
				t.Fatalf("%s uses %v, want its own %s", output, node.InternalTools, tool)
			}
		}
		if !found {
			t.Fatalf("missing consumer %s", output)
		}
	}
}

func TestRuntimeNativeGroupsKeepSeparateOwners(t *testing.T) {
	plan := runtimeConsumersPlanFixture(t, []string{"first", "second", "third", "fourth"})
	plan.Environment.GitHubARC = "native-kvm-v1"
	if err := admitInternalToolExecution(plan); err != nil {
		t.Fatal(err)
	}
	for _, tool := range plan.InternalTools {
		expected := strings.TrimPrefix(tool.Name, "runtime-")
		output, err := internalToolDiskOutput(plan, tool)
		if err != nil || output != expected {
			t.Fatalf("%s owner=%q error=%v", tool.Name, output, err)
		}
		for _, node := range plan.Graph.Nodes {
			if node.BuildTarget != tool.BuildTarget {
				continue
			}
			want := expected
			if node.Kind == NodeFetch {
				want = ""
			}
			if actual := githubDiskOutput(plan, node); actual != want {
				t.Fatalf("%s group=%q want=%q", node.ID, actual, want)
			}
		}
	}
	// The compiler never emits this cross-owner substitution. The scheduling
	// boundary still rejects it independently if passed a changed description.
	tool := plan.InternalTools[0]
	for i := range plan.Graph.Nodes {
		node := &plan.Graph.Nodes[i]
		if node.ID == "second/build-image" {
			node.InternalTools = []string{tool.Name}
		}
	}
	_, err := internalToolDiskOutput(plan, tool)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
}
