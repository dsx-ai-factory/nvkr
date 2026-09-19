package factory

import (
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// matchOptionsForBuildNode is the single owner for compiler-derived hard
// constraints passed to execution.Match. VM lowering derives required-KVM host
// affinity here so initial binding and Plan revalidation cannot diverge.
func matchOptionsForBuildNode(node ir.Node) execution.MatchOptions {
	return matchOptionsForVM(node.BuilderType, node.Platform, node.Requirements)
}

func matchOptionsForVM(builderType, guestPlatform string, requirements *execution.Requirements) execution.MatchOptions {
	if builderType != ir.BuilderVMImage || requirements == nil {
		return execution.MatchOptions{}
	}
	for _, capability := range requirements.Capabilities {
		if capability.Name == execution.CapabilityKVM && capability.Policy == execution.CapabilityRequired {
			return execution.MatchOptions{RequiredHostPlatform: execution.HostPlatform(guestPlatform)}
		}
	}
	return execution.MatchOptions{}
}

// selectVMHostPlatforms performs deterministic Environment matching before IR
// construction because a VM toolchain is pinned by execution-host architecture,
// not by the guest/output architecture. The resulting platform map is a compiler
// input only; buildgraph carries both the selected host and toolchain bytes into
// logical identity.
func selectVMHostPlatforms(components []catalog.NamedComponent, guestPlatform string,
	classes []execution.Class,
) (map[string]execution.HostPlatform, error) {
	hosts := make(map[string]execution.HostPlatform)
	for _, component := range components {
		if component.BuilderType != catalog.BuilderVMImage {
			continue
		}
		binding, err := execution.Match(component.BuildRequirements, classes,
			matchOptionsForVM(ir.BuilderVMImage, guestPlatform, component.BuildRequirements))
		if err != nil {
			return nil, err
		}
		if binding == nil {
			return nil, e.New(e.CodeEngineCapability,
				"VM component %q has no selected execution class", component.Name)
		}
		hosts[component.Name] = binding.Class.HostPlatform
	}
	return hosts, nil
}

// bindBuildExecution deterministically selects an Environment execution class
// for every selected build that declares requirements. Ordinary builders do not
// infer a host platform from their target/toolchain platform; VM-specific hard
// constraints are derived by matchOptionsForBuildNode.
func bindBuildExecution(graph *ir.Graph, selected map[string]bool, classes []execution.Class) error {
	for index := range graph.Nodes {
		node := &graph.Nodes[index]
		if node.Kind != ir.KindBuild || !selected[node.Component] {
			continue
		}
		binding, err := execution.Match(node.Requirements, classes, matchOptionsForBuildNode(*node))
		if err != nil {
			return err
		}
		node.ExecutionBinding = binding
	}
	return nil
}
