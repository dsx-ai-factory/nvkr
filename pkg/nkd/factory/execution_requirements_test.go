// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func testExecutionRequirements() *execution.Requirements {
	return &execution.Requirements{
		Resources: &execution.ResourceRequest{
			CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192,
		},
		AllowedHostPlatforms: []execution.HostPlatform{
			execution.HostPlatformLinuxAMD64, execution.HostPlatformLinuxARM64,
		},
		Capabilities: []execution.CapabilityRequirement{{
			Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred,
		}},
		TimeoutSeconds: 7200,
	}
}

func testExecutionClasses() []execution.Class {
	return []execution.Class{
		{
			Name: "alpha-tcg", RunsOn: "linux-amd64-tcg", HostPlatform: execution.HostPlatformLinuxAMD64,
			Resources: execution.ResourceCapacity{
				CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536,
			},
			MaxTimeoutSeconds: 14400,
		},
		{
			Name: "beta-kvm", RunsOn: "linux-amd64-kvm", HostPlatform: execution.HostPlatformLinuxAMD64,
			Resources: execution.ResourceCapacity{
				CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536,
			},
			Capabilities:      []execution.CapabilityName{execution.CapabilityKVM},
			MaxTimeoutSeconds: 14400,
		},
	}
}

func bindTestPlanExecution(t *testing.T, plan *Plan, buildIndex, irIndex, nodeIndex int,
	requirements *execution.Requirements,
) {
	t.Helper()
	plan.Environment.ExecutionClasses = testExecutionClasses()
	digest, err := plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = digest

	irNode := &plan.Builds[buildIndex].Graph.Nodes[irIndex]
	irNode.Requirements = execution.Clone(requirements)
	binding, err := execution.Match(
		irNode.Requirements, plan.Environment.ExecutionClasses, matchOptionsForBuildNode(*irNode))
	if err != nil {
		t.Fatal(err)
	}
	irNode.ExecutionBinding = binding
	for i := range plan.Builds[buildIndex].ResolvedComponents {
		if plan.Builds[buildIndex].ResolvedComponents[i].Name == irNode.Component {
			plan.Builds[buildIndex].ResolvedComponents[i].BuildRequirements = execution.Clone(requirements)
		}
	}
	syncTestCatalogComponents(t, plan)

}

func bindFirstTestPlanExecution(t *testing.T, plan *Plan, requirements *execution.Requirements) int {
	t.Helper()
	for buildIndex := range plan.Builds {
		build := &plan.Builds[buildIndex]
		for irIndex := range build.Graph.Nodes {
			irNode := &build.Graph.Nodes[irIndex]
			if irNode.Kind != ir.KindBuild {
				continue
			}
			for nodeIndex := range plan.Graph.Nodes {
				node := &plan.Graph.Nodes[nodeIndex]
				if node.BuildTarget != build.Name || node.IRNode != irNode.ID {
					continue
				}
				bindTestPlanExecution(t, plan, buildIndex, irIndex, nodeIndex, requirements)
				for componentIndex := range build.ResolvedComponents {
					component := &build.ResolvedComponents[componentIndex]
					if component.Name == irNode.Component {
						component.BuildRequirements = execution.Clone(requirements)
						catalogComponent := build.CatalogSnapshot.Components[component.Name]
						catalogComponent.BuildRequirements = execution.Clone(requirements)
						build.CatalogSnapshot.Components[component.Name] = catalogComponent
						snapshotDigest, err := build.CatalogSnapshot.SnapshotDigest()
						if err != nil {
							t.Fatal(err)
						}
						build.CatalogDigest = snapshotDigest
						build.AppliedOverlays = nil
						if err := plan.Validate(); err != nil {
							t.Fatalf("bound test Plan is invalid: %v", err)
						}
						return nodeIndex
					}
				}
				t.Fatalf("build %q has no resolved component %q", build.Name, irNode.Component)
			}
		}
	}
	t.Fatal("test plan has no projected build node")
	return -1
}

func TestCompilerReferencesAuthoritativeProducerRequirements(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	component := base.Components["kubelet"]
	component.BuildRequirements = testExecutionRequirements()
	base.Components["kubelet"] = component
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, delivery := fixtureInputs("requirements", "worker", "")
	delivery.ExecutionClasses = testExecutionClasses()
	plan, err := Compile(context.Background(), definition, delivery, stack, "v0.0.0 (test)")
	if err != nil {
		t.Fatal(err)
	}

	var buildRequest, planRequest *execution.Requirements
	var buildBinding, planBinding *execution.Binding
	var planRunsOn string
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.Kind == ir.KindBuild && node.Component == "kubelet" {
			buildRequest = node.Requirements
			buildBinding = node.ExecutionBinding
		}
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeBuild && node.BuildTarget == "worker" && node.IRNode == "build-kubelet" {
			if node.Requirements != nil || node.ExecutionBinding != nil {
				t.Fatal("scheduler copied producer configuration")
			}
			node, err = plan.ExecutionFor(node)
			if err != nil {
				t.Fatal(err)
			}
			planRequest = node.Requirements
			planBinding = node.ExecutionBinding
			planRunsOn = node.RunsOn
		}
	}
	if !execution.Equal(buildRequest, component.BuildRequirements) || !execution.Equal(planRequest, component.BuildRequirements) {
		t.Fatalf("requirements were not preserved catalog -> IR -> Plan: IR=%#v Plan=%#v", buildRequest, planRequest)
	}
	if !execution.EqualBinding(buildBinding, planBinding) || buildBinding == nil ||
		buildBinding.Class.Name != "beta-kvm" || planRunsOn != buildBinding.Class.RunsOn {
		t.Fatalf("execution binding was not preserved: IR=%#v Plan=%#v runsOn=%q", buildBinding, planBinding, planRunsOn)
	}
	before, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	delivery.ExecutionClasses[1].Resources.MemoryMiB = 1
	delivery.ExecutionClasses[1].Capabilities[0] = "changed-after-compile"
	if err := plan.Validate(); err != nil {
		t.Fatalf("caller mutation changed the compiled Plan: %v", err)
	}
	after, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("caller-owned execution class storage aliases the compiled Plan")
	}
	unsatisfied := plan.Environment
	unsatisfied.ExecutionClasses = execution.CloneClasses(plan.Environment.ExecutionClasses)
	unsatisfied.ExecutionClasses[0].Resources.MemoryMiB = 1
	unsatisfied.ExecutionClasses[1].Resources.MemoryMiB = 1
	if failed, compileErr := Compile(context.Background(), definition, unsatisfied, stack, "v0.0.0 (test)"); failed != nil {
		t.Fatalf("unsatisfied execution requirements returned a partial Plan: %#v", failed)
	} else {
		assertFactoryErrorCode(t, compileErr, e.CodeEngineCapability)
	}
}

func TestExecutionClassFieldsChangeEnvironmentIdentity(t *testing.T) {
	_, base := fixtureInputs("identity", "worker", "")
	base.ExecutionClasses = testExecutionClasses()
	baseline, err := base.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*execution.Class)
	}{
		{name: "name", change: func(class *execution.Class) { class.Name = "gamma-kvm" }},
		{name: "route", change: func(class *execution.Class) { class.RunsOn = "other-kvm" }},
		{name: "host platform", change: func(class *execution.Class) { class.HostPlatform = execution.HostPlatformLinuxARM64 }},
		{name: "CPU", change: func(class *execution.Class) { class.Resources.CPUMillis++ }},
		{name: "memory", change: func(class *execution.Class) { class.Resources.MemoryMiB++ }},
		{name: "ephemeral storage", change: func(class *execution.Class) { class.Resources.EphemeralStorageMiB++ }},
		{name: "capabilities", change: func(class *execution.Class) { class.Capabilities = nil }},
		{name: "timeout", change: func(class *execution.Class) { class.MaxTimeoutSeconds++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := base
			changed.ExecutionClasses = execution.CloneClasses(base.ExecutionClasses)
			test.change(&changed.ExecutionClasses[1])
			digest, err := changed.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if digest == baseline {
				t.Fatalf("changing execution class %s did not change Environment identity", test.name)
			}
		})
	}
}

func TestPlanValidationRejectsAlternativeSatisfyingExecutionClass(t *testing.T) {
	plan := buildOnlyPlan(t, "ubuntu-24.04")
	buildIndex, irIndex, nodeIndex := -1, -1, -1
	for i := range plan.Builds {
		for j := range plan.Builds[i].Graph.Nodes {
			candidate := &plan.Builds[i].Graph.Nodes[j]
			if candidate.Kind != ir.KindBuild {
				continue
			}
			buildIndex, irIndex = i, j
			for k := range plan.Graph.Nodes {
				if plan.Graph.Nodes[k].BuildTarget == plan.Builds[i].Name && plan.Graph.Nodes[k].IRNode == candidate.ID {
					nodeIndex = k
					break
				}
			}
			break
		}
		if nodeIndex >= 0 {
			break
		}
	}
	if buildIndex < 0 || irIndex < 0 || nodeIndex < 0 {
		t.Fatal("test plan has no projected build node")
	}

	requirements := testExecutionRequirements()
	classes := testExecutionClasses()
	classes[0].Capabilities = []execution.CapabilityName{execution.CapabilityKVM}
	plan.Environment.ExecutionClasses = classes
	settingsDigest, err := plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = settingsDigest
	irNode := &plan.Builds[buildIndex].Graph.Nodes[irIndex]
	irNode.Requirements = execution.Clone(requirements)
	for i := range plan.Builds[buildIndex].ResolvedComponents {
		if plan.Builds[buildIndex].ResolvedComponents[i].Name == irNode.Component {
			plan.Builds[buildIndex].ResolvedComponents[i].BuildRequirements = execution.Clone(requirements)
		}
	}
	expected, err := execution.Match(requirements, classes, execution.MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if expected.Class.Name != "alpha-tcg" {
		t.Fatalf("deterministic tie selected %q, want alpha-tcg", expected.Class.Name)
	}
	irNode.ExecutionBinding = expected
	syncTestCatalogComponents(t, plan)
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}

	alternative, err := execution.Match(requirements, classes[1:], execution.MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	irNode.ExecutionBinding = alternative
	assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
}

func TestPlanValidationRejectsUnselectedProducerGraph(t *testing.T) {
	plan := buildOnlyPlan(t, "ubuntu-24.04")
	graph := plan.Builds[0].Graph
	for _, node := range graph.Nodes {
		if node.Kind != ir.KindBuild {
			continue
		}
		extra := node
		extra.ID = "build-unselected"
		extra.Component = "unselected"
		graph.Nodes = append(graph.Nodes, extra)
		for i := range graph.Nodes {
			if graph.Nodes[i].Kind == ir.KindVerify {
				graph.Nodes[i].Needs = append(graph.Nodes[i].Needs, extra.ID)
			}
		}
		break
	}
	graph.Normalize()
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.Builds[0].BuildDigest = digest
	assertFactoryErrorCode(t, plan.Validate(), e.CodeUnverifiedRef)
}

func TestPlanValidationRejectsExecutionRouteDrift(t *testing.T) {
	plan := buildOnlyPlan(t, "ubuntu-24.04")
	nodeIndex := bindFirstTestPlanExecution(t, plan, testExecutionRequirements())

	plan.Graph.Nodes[nodeIndex].RunsOn = "substituted-route"
	assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
}

func TestPlanValidationRejectsUnsatisfiedExecutionClassInventory(t *testing.T) {
	plan := buildOnlyPlan(t, "ubuntu-24.04")
	bindFirstTestPlanExecution(t, plan, testExecutionRequirements())

	for index := range plan.Environment.ExecutionClasses {
		plan.Environment.ExecutionClasses[index].Resources.MemoryMiB = 1
	}
	digest, err := plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = digest

	assertFactoryErrorCode(t, plan.Validate(), e.CodeEngineCapability)
}

func TestPlanBindsGenericNodeRequirementsAndChangesPlanIdentity(t *testing.T) {
	plan := buildOnlyPlan(t, "ubuntu-24.04")
	buildDigest := plan.Builds[0].BuildDigest

	buildIndex, irIndex, nodeIndex := -1, -1, -1
	for i := range plan.Builds {
		for j := range plan.Builds[i].Graph.Nodes {
			if plan.Builds[i].Graph.Nodes[j].Kind == ir.KindBuild {
				buildIndex, irIndex = i, j
				break
			}
		}
		if irIndex >= 0 {
			break
		}
	}
	if buildIndex < 0 || irIndex < 0 {
		t.Fatal("test plan has no build node")
	}
	irNode := &plan.Builds[buildIndex].Graph.Nodes[irIndex]
	for i := range plan.Graph.Nodes {
		if plan.Graph.Nodes[i].BuildTarget == plan.Builds[buildIndex].Name && plan.Graph.Nodes[i].IRNode == irNode.ID {
			nodeIndex = i
			break
		}
	}
	if nodeIndex < 0 {
		t.Fatal("factory plan does not project its IR build node")
	}
	componentIndex := -1
	for i := range plan.Builds[buildIndex].ResolvedComponents {
		if plan.Builds[buildIndex].ResolvedComponents[i].Name == irNode.Component {
			componentIndex = i
			break
		}
	}
	if componentIndex < 0 {
		t.Fatal("build plan does not retain its resolved build component")
	}

	plan.Environment.ExecutionClasses = testExecutionClasses()
	settingsDigest, err := plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = settingsDigest
	baselineDigest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}

	bindTestPlanExecution(t, plan, buildIndex, irIndex, nodeIndex, testExecutionRequirements())
	plan.Builds[buildIndex].ResolvedComponents[componentIndex].BuildRequirements = execution.Clone(irNode.Requirements)
	if err := plan.Validate(); err != nil {
		t.Fatalf("bound generic requirements made the Plan invalid: %v", err)
	}
	requestedDigest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if requestedDigest == baselineDigest {
		t.Fatal("generic execution requirements did not affect Plan identity")
	}
	if plan.Builds[buildIndex].BuildDigest != buildDigest {
		t.Fatal("execution binding changed the frozen Build identity")
	}
	bind := func(requirements *execution.Requirements) {
		bindTestPlanExecution(t, plan, buildIndex, irIndex, nodeIndex, requirements)
		plan.Builds[buildIndex].ResolvedComponents[componentIndex].BuildRequirements = execution.Clone(requirements)
	}
	for _, test := range []struct {
		name string
		edit func(*execution.Requirements)
	}{
		{name: "resources", edit: func(requirements *execution.Requirements) {
			requirements.Resources.MemoryMiB++
		}},
		{name: "host platforms", edit: func(requirements *execution.Requirements) {
			requirements.AllowedHostPlatforms = requirements.AllowedHostPlatforms[:1]
		}},
		{name: "capability policy", edit: func(requirements *execution.Requirements) {
			requirements.Capabilities[0].Policy = execution.CapabilityRequired
		}},
		{name: "timeout", edit: func(requirements *execution.Requirements) {
			requirements.TimeoutSeconds++
		}},
	} {
		t.Run(test.name+" changes Plan identity", func(t *testing.T) {
			changed := testExecutionRequirements()
			test.edit(changed)
			bind(changed)
			if err := plan.Validate(); err != nil {
				t.Fatalf("changed requirements made the Plan invalid: %v", err)
			}
			digest, err := plan.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if digest == requestedDigest {
				t.Fatalf("%s did not affect Plan identity", test.name)
			}
		})
	}

	bind(testExecutionRequirements())
	savedComponents := plan.Builds[buildIndex].ResolvedComponents
	missingComponents := append([]catalog.NamedComponent(nil), savedComponents[:componentIndex]...)
	missingComponents = append(missingComponents, savedComponents[componentIndex+1:]...)
	plan.Builds[buildIndex].ResolvedComponents = missingComponents
	assertFactoryErrorCode(t, plan.Validate(), e.CodeUnverifiedRef)
	plan.Builds[buildIndex].ResolvedComponents = savedComponents

	plan.Builds[buildIndex].ResolvedComponents = append(
		append([]catalog.NamedComponent(nil), savedComponents...), savedComponents[componentIndex])
	assertFactoryErrorCode(t, plan.Validate(), e.CodeUnverifiedRef)
	plan.Builds[buildIndex].ResolvedComponents = savedComponents

	plan.Graph.Nodes[nodeIndex].Requirements = execution.Clone(irNode.Requirements)
	plan.Graph.Nodes[nodeIndex].Requirements.Resources.MemoryMiB++
	assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)

	plan.Graph.Nodes[nodeIndex].Requirements = nil
	plan.Builds[buildIndex].ResolvedComponents[componentIndex].BuildRequirements.Resources.MemoryMiB++
	assertFactoryErrorCode(t, plan.Validate(), e.CodeUnverifiedRef)
}

func TestCurrentFactoryAdaptersRejectExecutionRequirementsBeforeOutput(t *testing.T) {
	plan := buildOnlyPlan(t, "ubuntu-24.04")
	baselineBundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}

	var buildNode *ir.Node
	for i := range plan.Builds[0].Graph.Nodes {
		if plan.Builds[0].Graph.Nodes[i].Kind == ir.KindBuild {
			buildNode = &plan.Builds[0].Graph.Nodes[i]
			break
		}
	}
	if buildNode == nil {
		t.Fatal("test plan has no build node")
	}
	buildNode.Requirements = testExecutionRequirements()
	plan.Environment.ExecutionClasses = testExecutionClasses()
	settingsDigest, err := plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.EnvironmentDigest = settingsDigest
	buildNode.ExecutionBinding, err = execution.Match(
		buildNode.Requirements, plan.Environment.ExecutionClasses, execution.MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	componentBound := false
	for i := range plan.Builds[0].ResolvedComponents {
		component := &plan.Builds[0].ResolvedComponents[i]
		if component.Name == buildNode.Component {
			component.BuildRequirements = execution.Clone(buildNode.Requirements)
			componentBound = true
			break
		}
	}
	if !componentBound {
		t.Fatal("test plan has no resolved component for its selected build node")
	}
	var factoryNodeID string
	for i := range plan.Graph.Nodes {
		node := &plan.Graph.Nodes[i]
		if node.BuildTarget == plan.Builds[0].Name && node.IRNode == buildNode.ID {
			factoryNodeID = node.ID
			break
		}
	}
	syncTestCatalogComponents(t, plan)
	if factoryNodeID == "" {
		t.Fatal("test plan has no projected build node")
	}
	var downstreamNodeID string
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodePackage {
			downstreamNodeID = node.ID
			break
		}
	}
	if downstreamNodeID == "" {
		t.Fatal("test plan has no downstream package node")
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	assertFactoryErrorCode(t,
		ValidateHostedRestores(nil, t.TempDir(), plan), //nolint:staticcheck // Intentional nil-context admission test.
		e.CodeUsage)
	assertFactoryErrorCode(t,
		ValidateHostedOutcomes(nil, t.TempDir(), plan), //nolint:staticcheck // Intentional nil-context admission test.
		e.CodeUsage)

	if bundle, err := RenderGitHub(context.Background(), plan); bundle != nil {
		t.Fatalf("GitHub rejection returned partial output: %#v", bundle)
	} else {
		assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	}

	assertFactoryErrorCode(t,
		AdmitLocal(context.Background(), plan, LocalRunOptions{DryRun: true, Workspace: t.TempDir()}),
		e.CodeEngineCapability)
	assertFactoryErrorCode(t,
		ValidateGitHubBundle(context.Background(), plan, baselineBundle),
		e.CodeEngineCapability)
	assertFactoryErrorCode(t,
		ExecuteNode(context.Background(), plan, factoryNodeID, nil, nil),
		e.CodeEngineCapability)
	assertFactoryErrorCode(t,
		ExecuteNode(context.Background(), plan, downstreamNodeID, nil, nil),
		e.CodeEngineCapability)
	assertFactoryErrorCode(t,
		ValidateHostedRestores(context.Background(), t.TempDir(), plan),
		e.CodeEngineCapability)
	assertFactoryErrorCode(t,
		ValidateHostedOutcomes(context.Background(), t.TempDir(), plan),
		e.CodeEngineCapability)

	for i := range plan.Graph.Nodes {
		if plan.Graph.Nodes[i].ID == factoryNodeID {
			plan.Graph.Nodes[i].Requirements = execution.Clone(buildNode.Requirements)
			break
		}
	}
	assertFactoryErrorCode(t,
		ValidateHostedRestores(context.Background(), t.TempDir(), plan),
		e.CodePipelineDrift)
	assertFactoryErrorCode(t,
		ValidateHostedOutcomes(context.Background(), t.TempDir(), plan),
		e.CodePipelineDrift)

	for i := range plan.Graph.Nodes {
		plan.Graph.Nodes[i].Requirements = nil
	}
	retainedNodes := plan.Graph.Nodes
	plan.Graph.Nodes = nil
	for _, node := range retainedNodes {
		if node.ID != factoryNodeID {
			plan.Graph.Nodes = append(plan.Graph.Nodes, node)
		}
	}
	assertFactoryErrorCode(t,
		rejectUnsupportedPlanRequirements(plan, "test adapter"),
		e.CodeEngineCapability)
	assertFactoryErrorCode(t,
		ValidateHostedRestores(context.Background(), t.TempDir(), plan),
		e.CodeInvalidCatalog)
	assertFactoryErrorCode(t,
		ValidateHostedOutcomes(context.Background(), t.TempDir(), plan),
		e.CodeInvalidCatalog)
}

func TestGeneratedBundleRejectsRequirementsInjectedIntoPlan(t *testing.T) {
	plan := buildOnlyPlan(t, "ubuntu-24.04")
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}

	path := bundle.PlanPath
	var decoded Plan
	if err := canonical.UnmarshalExact(bundle.Files[path], &decoded); err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range decoded.Builds[0].Graph.Nodes {
		if decoded.Builds[0].Graph.Nodes[i].Kind == ir.KindBuild {
			decoded.Builds[0].Graph.Nodes[i].Requirements = testExecutionRequirements()
			found = true
			break
		}
	}
	if !found {
		t.Fatal("generated Plan has no build instruction")
	}
	raw, err := canonical.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	mutated := *bundle
	mutated.Files = make(map[string][]byte, len(bundle.Files))
	for name, member := range bundle.Files {
		mutated.Files[name] = append([]byte(nil), member...)
	}
	mutated.Files[path] = raw

	assertFactoryErrorCode(t,
		ValidateGitHubBundle(context.Background(), plan, &mutated),
		e.CodePipelineDrift)
}

func syncTestCatalogComponents(t *testing.T, plan *Plan) {
	t.Helper()
	for i := range plan.Builds {
		build := &plan.Builds[i]
		for _, component := range build.ResolvedComponents {
			build.CatalogSnapshot.Components[component.Name] = component.Component
		}
		digest, err := build.CatalogSnapshot.SnapshotDigest()
		if err != nil {
			t.Fatal(err)
		}
		build.CatalogDigest = digest
		build.AppliedOverlays = nil
		for _, output := range plan.Definition.Outputs {
			if output.Name != build.Name || output.Catalog != nil {
				continue
			}
			base, err := catalog.ApplyStack(build.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			plan.CatalogSnapshot, plan.CatalogDigest = base.Catalog, base.Digest
			plan.AppliedOverlays = nil
		}
	}
}
