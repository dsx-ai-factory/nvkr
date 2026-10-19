// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
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

// validateExecutionBindings recomputes selection from exact catalog requirements
// and the admitted Environment before scheduler nodes consume producer references.
func (plan Plan) validateExecutionBindings() (map[string]ir.Node, error) {
	builds := map[string]ir.Node{}
	for _, build := range plan.Builds {
		selected := make(map[string]struct{}, len(build.Components))
		for _, name := range build.Components {
			selected[name] = struct{}{}
		}
		components := make(map[string]*execution.Requirements, len(build.ResolvedComponents))
		for _, component := range build.ResolvedComponents {
			if _, selectedComponent := selected[component.Name]; !selectedComponent {
				return nil, e.New(e.CodePipelineDrift,
					"build %q retains unselected resolved component %q", build.Name, component.Name)
			}
			if _, duplicate := components[component.Name]; duplicate {
				return nil, e.New(e.CodePipelineDrift,
					"build %q repeats resolved component %q", build.Name, component.Name)
			}
			components[component.Name] = component.BuildRequirements
		}
		for name := range selected {
			if _, ok := components[name]; !ok {
				return nil, e.New(e.CodePipelineDrift,
					"build %q has no resolved component %q", build.Name, name)
			}
		}
		for _, node := range build.Graph.Nodes {
			if node.Kind == ir.KindBuild {
				if _, selectedComponent := selected[node.Component]; !selectedComponent {
					if node.ExecutionBinding != nil {
						return nil, e.New(e.CodePipelineDrift,
							"build %q unselected build node %q has an execution-class binding", build.Name, node.ID)
					}
					continue
				}
				requirements := components[node.Component]
				if !execution.Equal(node.Requirements, requirements) {
					return nil, e.New(e.CodePipelineDrift,
						"build %q build node %q changes its catalog execution requirements", build.Name, node.ID)
				}
				expectedBinding, matchErr := execution.Match(
					node.Requirements, plan.Environment.ExecutionClasses, matchOptionsForBuildNode(node))
				if matchErr != nil {
					return nil, e.Wrap(e.CodeEngineCapability, matchErr,
						"build %q build node %q has no admitted execution class", build.Name, node.ID)
				}
				if !execution.EqualBinding(node.ExecutionBinding, expectedBinding) {
					return nil, e.New(e.CodePipelineDrift,
						"build %q build node %q changes its deterministic execution-class binding", build.Name, node.ID)
				}
				builds[build.Name+"/"+node.ID] = node
			}
		}
	}
	return builds, nil
}
