package factory

import (
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// bindRecipeExecution deterministically selects an Environment execution class
// for every selected build that declares requirements. Ordinary builders do not
// infer a host platform from their target/toolchain platform; VM-specific hard
// constraints belong to the later VM lowering boundary.
func bindRecipeExecution(graph *ir.Graph, selected map[string]bool, classes []execution.Class) error {
	for index := range graph.Nodes {
		node := &graph.Nodes[index]
		if node.Kind != ir.KindBuild || !selected[node.Component] {
			continue
		}
		binding, err := execution.Match(node.Requirements, classes, execution.MatchOptions{})
		if err != nil {
			return err
		}
		node.ExecutionBinding = binding
	}
	return nil
}
