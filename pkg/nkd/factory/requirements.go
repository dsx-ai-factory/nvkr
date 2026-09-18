package factory

import (
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func rejectUnsupportedNodeRequirements(node Node, adapter string) error {
	if node.Requirements == nil {
		return nil
	}
	return e.New(e.CodeEngineCapability,
		"%s cannot enforce execution requirements for node %q", adapter, node.ID)
}

func rejectUnsupportedPlanRequirements(plan *Plan, adapter string) error {
	selected := make(map[string]map[string]struct{})
	for _, node := range plan.Graph.Nodes {
		if err := rejectUnsupportedNodeRequirements(node, adapter); err != nil {
			return err
		}
		if node.Kind == NodeBuild {
			if selected[node.BuildTarget] == nil {
				selected[node.BuildTarget] = make(map[string]struct{})
			}
			selected[node.BuildTarget][node.IRNode] = struct{}{}
		}
	}
	for _, build := range plan.Builds {
		for _, component := range build.ResolvedComponents {
			if component.BuildRequirements != nil {
				return e.New(e.CodeEngineCapability,
					"%s cannot enforce execution requirements for component %q", adapter, component.Name)
			}
		}
		selectedNodes := selected[build.Name]
		if len(selectedNodes) == 0 || build.Graph == nil {
			continue
		}
		for _, node := range build.Graph.Nodes {
			if node.Kind != ir.KindBuild {
				continue
			}
			if _, ok := selectedNodes[node.ID]; !ok {
				continue
			}
			if node.Requirements != nil {
				return e.New(e.CodeEngineCapability,
					"%s cannot enforce execution requirements for node %q", adapter, node.ID)
			}
		}
	}
	return nil
}
