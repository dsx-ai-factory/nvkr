package factory

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
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

func bindTestPlanExecution(t *testing.T, plan *Plan, recipeIndex, irIndex, nodeIndex int,
	requirements *execution.Requirements,
) {
	t.Helper()
	plan.ExecutionSettings.ExecutionClasses = testExecutionClasses()
	digest, err := plan.ExecutionSettings.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.ExecutionSettingsDigest = digest

	irNode := &plan.Recipes[recipeIndex].Graph.Nodes[irIndex]
	irNode.Requirements = execution.Clone(requirements)
	binding, err := execution.Match(
		irNode.Requirements, plan.ExecutionSettings.ExecutionClasses, matchOptionsForBuildNode(*irNode))
	if err != nil {
		t.Fatal(err)
	}
	irNode.ExecutionBinding = binding
	plan.Graph.Nodes[nodeIndex].Requirements = execution.Clone(requirements)
	plan.Graph.Nodes[nodeIndex].ExecutionBinding = execution.CloneBinding(binding)
	plan.Graph.Nodes[nodeIndex].RunsOn = binding.Class.RunsOn
}

func bindFirstTestPlanExecution(t *testing.T, plan *Plan, requirements *execution.Requirements) int {
	t.Helper()
	for recipeIndex := range plan.Recipes {
		recipe := &plan.Recipes[recipeIndex]
		for irIndex := range recipe.Graph.Nodes {
			irNode := &recipe.Graph.Nodes[irIndex]
			if irNode.Kind != ir.KindBuild {
				continue
			}
			for nodeIndex := range plan.Graph.Nodes {
				node := &plan.Graph.Nodes[nodeIndex]
				if node.RecipeTarget != recipe.Name || node.IRNode != irNode.ID {
					continue
				}
				bindTestPlanExecution(t, plan, recipeIndex, irIndex, nodeIndex, requirements)
				for componentIndex := range recipe.ResolvedComponents {
					component := &recipe.ResolvedComponents[componentIndex]
					if component.Name == irNode.Component {
						component.BuildRequirements = execution.Clone(requirements)
						if err := plan.Validate(); err != nil {
							t.Fatalf("bound test Plan is invalid: %v", err)
						}
						return nodeIndex
					}
				}
				t.Fatalf("recipe %q has no resolved component %q", recipe.Name, irNode.Component)
			}
		}
	}
	t.Fatal("test plan has no projected build node")
	return -1
}

func TestCompileProjectsProducerRequirementsOntoGenericPlanNode(t *testing.T) {
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
	definition := CompileSpec{APIVersion: CompileSpecAPIVersion, Kind: CompileSpecKind,
		Metadata: Metadata{Name: "requirements"}, Catalog: CatalogStack{Base: "testdata/catalogs/kubernetes-layered"},
		UtilityContainer: "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161",
		UtilityPlatform:  "linux/amd64",
		Recipes: []RecipeTarget{{Name: "worker", Criteria: criteria.Criteria{
			Category: "kubernetes", Role: "worker", K8sVersion: "1.34.0", OS: "ubuntu", Arch: "amd64",
		}}},
		Packages: []PackageSpec{{Name: "tools", Kind: PackageBinaryBundle, Inputs: []string{"worker/*"}}},
	}
	delivery := ExecutionSettings{APIVersion: ExecutionSettingsAPIVersion, Kind: ExecutionSettingsKind,
		Metadata: Metadata{Name: "requirements"}, ExecutionClasses: testExecutionClasses()}
	plan, err := Compile(context.Background(), definition, delivery, stack, "v0.0.0 (test)")
	if err != nil {
		t.Fatal(err)
	}

	var recipeRequest, planRequest *execution.Requirements
	var recipeBinding, planBinding *execution.Binding
	var planRunsOn string
	for _, node := range plan.Recipes[0].Graph.Nodes {
		if node.Kind == ir.KindBuild && node.Component == "kubelet" {
			recipeRequest = node.Requirements
			recipeBinding = node.ExecutionBinding
		}
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeBuild && node.RecipeTarget == "worker" && node.IRNode == "build-kubelet" {
			planRequest = node.Requirements
			planBinding = node.ExecutionBinding
			planRunsOn = node.RunsOn
		}
	}
	if !execution.Equal(recipeRequest, component.BuildRequirements) || !execution.Equal(planRequest, component.BuildRequirements) {
		t.Fatalf("requirements were not preserved catalog -> IR -> Plan: IR=%#v Plan=%#v", recipeRequest, planRequest)
	}
	if !execution.EqualBinding(recipeBinding, planBinding) || recipeBinding == nil ||
		recipeBinding.Class.Name != "beta-kvm" || planRunsOn != recipeBinding.Class.RunsOn {
		t.Fatalf("execution binding was not preserved: IR=%#v Plan=%#v runsOn=%q", recipeBinding, planBinding, planRunsOn)
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
	unsatisfied := plan.ExecutionSettings
	unsatisfied.ExecutionClasses = execution.CloneClasses(plan.ExecutionSettings.ExecutionClasses)
	unsatisfied.ExecutionClasses[0].Resources.MemoryMiB = 1
	unsatisfied.ExecutionClasses[1].Resources.MemoryMiB = 1
	if failed, compileErr := Compile(context.Background(), definition, unsatisfied, stack, "v0.0.0 (test)"); failed != nil {
		t.Fatalf("unsatisfied execution requirements returned a partial Plan: %#v", failed)
	} else {
		assertFactoryErrorCode(t, compileErr, e.CodeEngineCapability)
	}
}

func TestExecutionClassFieldsChangeExecutionSettingsIdentity(t *testing.T) {
	base := ExecutionSettings{
		APIVersion: ExecutionSettingsAPIVersion, Kind: ExecutionSettingsKind,
		Metadata: Metadata{Name: "identity"}, ExecutionClasses: testExecutionClasses(),
	}
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
				t.Fatalf("changing execution class %s did not change ExecutionSettings identity", test.name)
			}
		})
	}
}

func TestPlanValidationRejectsAlternativeSatisfyingExecutionClass(t *testing.T) {
	plan := buildOnlyPlan(t, "generic")
	recipeIndex, irIndex, nodeIndex := -1, -1, -1
	for i := range plan.Recipes {
		for j := range plan.Recipes[i].Graph.Nodes {
			candidate := &plan.Recipes[i].Graph.Nodes[j]
			if candidate.Kind != ir.KindBuild {
				continue
			}
			recipeIndex, irIndex = i, j
			for k := range plan.Graph.Nodes {
				if plan.Graph.Nodes[k].RecipeTarget == plan.Recipes[i].Name && plan.Graph.Nodes[k].IRNode == candidate.ID {
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
	if recipeIndex < 0 || irIndex < 0 || nodeIndex < 0 {
		t.Fatal("test plan has no projected build node")
	}

	requirements := testExecutionRequirements()
	classes := testExecutionClasses()
	classes[0].Capabilities = []execution.CapabilityName{execution.CapabilityKVM}
	plan.ExecutionSettings.ExecutionClasses = classes
	settingsDigest, err := plan.ExecutionSettings.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.ExecutionSettingsDigest = settingsDigest
	irNode := &plan.Recipes[recipeIndex].Graph.Nodes[irIndex]
	irNode.Requirements = execution.Clone(requirements)
	for i := range plan.Recipes[recipeIndex].ResolvedComponents {
		if plan.Recipes[recipeIndex].ResolvedComponents[i].Name == irNode.Component {
			plan.Recipes[recipeIndex].ResolvedComponents[i].BuildRequirements = execution.Clone(requirements)
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
	plan.Graph.Nodes[nodeIndex].Requirements = execution.Clone(requirements)
	plan.Graph.Nodes[nodeIndex].ExecutionBinding = execution.CloneBinding(expected)
	plan.Graph.Nodes[nodeIndex].RunsOn = expected.Class.RunsOn
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}

	alternative, err := execution.Match(requirements, classes[1:], execution.MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	irNode.ExecutionBinding = alternative
	plan.Graph.Nodes[nodeIndex].ExecutionBinding = execution.CloneBinding(alternative)
	plan.Graph.Nodes[nodeIndex].RunsOn = alternative.Class.RunsOn
	assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
}

func TestPlanValidationRejectsExecutionBindingOnUnselectedBuild(t *testing.T) {
	plan := buildOnlyPlan(t, "generic")
	selected := make(map[string]struct{}, len(plan.Recipes[0].Components))
	for _, component := range plan.Recipes[0].Components {
		selected[component] = struct{}{}
	}

	var unselected *ir.Node
	for i := range plan.Recipes[0].Graph.Nodes {
		node := &plan.Recipes[0].Graph.Nodes[i]
		if node.Kind != ir.KindBuild {
			continue
		}
		if _, ok := selected[node.Component]; !ok {
			unselected = node
			break
		}
	}
	if unselected == nil {
		t.Fatal("test plan has no unselected build node")
	}

	plan.ExecutionSettings.ExecutionClasses = testExecutionClasses()
	settingsDigest, err := plan.ExecutionSettings.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.ExecutionSettingsDigest = settingsDigest
	unselected.Requirements = testExecutionRequirements()
	unselected.ExecutionBinding, err = execution.Match(
		unselected.Requirements, plan.ExecutionSettings.ExecutionClasses, execution.MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}

	assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
}

func TestPlanValidationRejectsExecutionRouteDrift(t *testing.T) {
	plan := buildOnlyPlan(t, "generic")
	nodeIndex := bindFirstTestPlanExecution(t, plan, testExecutionRequirements())

	plan.Graph.Nodes[nodeIndex].RunsOn = "substituted-route"
	assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
}

func TestPlanValidationRejectsUnsatisfiedExecutionClassInventory(t *testing.T) {
	plan := buildOnlyPlan(t, "generic")
	bindFirstTestPlanExecution(t, plan, testExecutionRequirements())

	for index := range plan.ExecutionSettings.ExecutionClasses {
		plan.ExecutionSettings.ExecutionClasses[index].Resources.MemoryMiB = 1
	}
	digest, err := plan.ExecutionSettings.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.ExecutionSettingsDigest = digest

	assertFactoryErrorCode(t, plan.Validate(), e.CodeEngineCapability)
}

func TestPlanBindsGenericNodeRequirementsAndChangesPlanIdentity(t *testing.T) {
	plan := buildOnlyPlan(t, "generic")
	recipeDigest := plan.Recipes[0].RecipeDigest

	recipeIndex, irIndex, nodeIndex := -1, -1, -1
	for i := range plan.Recipes {
		for j := range plan.Recipes[i].Graph.Nodes {
			if plan.Recipes[i].Graph.Nodes[j].Kind == ir.KindBuild {
				recipeIndex, irIndex = i, j
				break
			}
		}
		if irIndex >= 0 {
			break
		}
	}
	if recipeIndex < 0 || irIndex < 0 {
		t.Fatal("test plan has no build node")
	}
	irNode := &plan.Recipes[recipeIndex].Graph.Nodes[irIndex]
	for i := range plan.Graph.Nodes {
		if plan.Graph.Nodes[i].RecipeTarget == plan.Recipes[recipeIndex].Name && plan.Graph.Nodes[i].IRNode == irNode.ID {
			nodeIndex = i
			break
		}
	}
	if nodeIndex < 0 {
		t.Fatal("factory plan does not project its IR build node")
	}
	componentIndex := -1
	for i := range plan.Recipes[recipeIndex].ResolvedComponents {
		if plan.Recipes[recipeIndex].ResolvedComponents[i].Name == irNode.Component {
			componentIndex = i
			break
		}
	}
	if componentIndex < 0 {
		t.Fatal("recipe plan does not retain its resolved build component")
	}

	plan.ExecutionSettings.ExecutionClasses = testExecutionClasses()
	settingsDigest, err := plan.ExecutionSettings.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.ExecutionSettingsDigest = settingsDigest
	baselineDigest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}

	bindTestPlanExecution(t, plan, recipeIndex, irIndex, nodeIndex, testExecutionRequirements())
	plan.Recipes[recipeIndex].ResolvedComponents[componentIndex].BuildRequirements = execution.Clone(irNode.Requirements)
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
	if plan.Recipes[recipeIndex].RecipeDigest != recipeDigest {
		t.Fatal("execution binding changed the frozen Recipe identity")
	}
	bind := func(requirements *execution.Requirements) {
		bindTestPlanExecution(t, plan, recipeIndex, irIndex, nodeIndex, requirements)
		plan.Recipes[recipeIndex].ResolvedComponents[componentIndex].BuildRequirements = execution.Clone(requirements)
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
	savedComponents := plan.Recipes[recipeIndex].ResolvedComponents
	missingComponents := append([]catalog.NamedComponent(nil), savedComponents[:componentIndex]...)
	missingComponents = append(missingComponents, savedComponents[componentIndex+1:]...)
	plan.Recipes[recipeIndex].ResolvedComponents = missingComponents
	assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
	plan.Recipes[recipeIndex].ResolvedComponents = savedComponents

	plan.Recipes[recipeIndex].ResolvedComponents = append(
		append([]catalog.NamedComponent(nil), savedComponents...), savedComponents[componentIndex])
	assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
	plan.Recipes[recipeIndex].ResolvedComponents = savedComponents

	plan.Graph.Nodes[nodeIndex].Requirements.Resources.MemoryMiB++
	assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)

	plan.Graph.Nodes[nodeIndex].Requirements = execution.Clone(irNode.Requirements)
	plan.Recipes[recipeIndex].ResolvedComponents[componentIndex].BuildRequirements.Resources.MemoryMiB++
	assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
}

func TestCurrentFactoryAdaptersRejectExecutionRequirementsBeforeOutput(t *testing.T) {
	plan := buildOnlyPlan(t, "generic")
	baselineBundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}

	var recipeNode *ir.Node
	for i := range plan.Recipes[0].Graph.Nodes {
		if plan.Recipes[0].Graph.Nodes[i].Kind == ir.KindBuild {
			recipeNode = &plan.Recipes[0].Graph.Nodes[i]
			break
		}
	}
	if recipeNode == nil {
		t.Fatal("test plan has no build node")
	}
	recipeNode.Requirements = testExecutionRequirements()
	plan.ExecutionSettings.ExecutionClasses = testExecutionClasses()
	settingsDigest, err := plan.ExecutionSettings.Digest()
	if err != nil {
		t.Fatal(err)
	}
	plan.ExecutionSettingsDigest = settingsDigest
	recipeNode.ExecutionBinding, err = execution.Match(
		recipeNode.Requirements, plan.ExecutionSettings.ExecutionClasses, execution.MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	componentBound := false
	for i := range plan.Recipes[0].ResolvedComponents {
		component := &plan.Recipes[0].ResolvedComponents[i]
		if component.Name == recipeNode.Component {
			component.BuildRequirements = execution.Clone(recipeNode.Requirements)
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
		if node.RecipeTarget == plan.Recipes[0].Name && node.IRNode == recipeNode.ID {
			node.Requirements = execution.Clone(recipeNode.Requirements)
			node.ExecutionBinding = execution.CloneBinding(recipeNode.ExecutionBinding)
			node.RunsOn = recipeNode.ExecutionBinding.Class.RunsOn
			factoryNodeID = node.ID
			break
		}
	}
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
	if bundle, err := RenderGitLab(context.Background(), plan); bundle != nil {
		t.Fatalf("GitLab rejection returned partial output: %#v", bundle)
	} else {
		assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	}
	assertFactoryErrorCode(t,
		AdmitLocal(context.Background(), plan, LocalRunOptions{DryRun: true, Workspace: t.TempDir()}),
		e.CodeEngineCapability)
	assertFactoryErrorCode(t,
		ValidateGeneratedBundle(context.Background(), plan, baselineBundle),
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
			plan.Graph.Nodes[i].Requirements = nil
			plan.Graph.Nodes[i].ExecutionBinding = nil
			break
		}
	}
	assertFactoryErrorCode(t,
		ValidateHostedRestores(context.Background(), t.TempDir(), plan),
		e.CodePipelineDrift)
	assertFactoryErrorCode(t,
		ValidateHostedOutcomes(context.Background(), t.TempDir(), plan),
		e.CodePipelineDrift)

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

func TestGeneratedBundleRejectsRequirementsInjectedIntoGraphMember(t *testing.T) {
	plan := buildOnlyPlan(t, "generic")
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.ToSlash(filepath.Join(bundle.GraphDir, plan.Recipes[0].Name+".json"))
	var graph ir.Graph
	if err := canonical.UnmarshalExact(bundle.Files[path], &graph); err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range graph.Nodes {
		if graph.Nodes[i].Kind == ir.KindBuild {
			graph.Nodes[i].Requirements = testExecutionRequirements()
			found = true
			break
		}
	}
	if !found {
		t.Fatal("generated graph has no build node")
	}
	raw, err := graph.CanonicalJSON()
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
		ValidateGeneratedBundle(context.Background(), plan, &mutated),
		e.CodePipelineDrift)
}
