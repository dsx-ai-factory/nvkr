package factory

import (
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// matchOptionsForBuildNode is the single owner for compiler-derived hard
// constraints passed to execution.Match. Current IR lowering admits only
// ordinary builders, which add none. Future VM lowering must derive its
// RequiredHostPlatform here so initial binding and Plan revalidation cannot
// diverge.
func matchOptionsForBuildNode(_ ir.Node) execution.MatchOptions {
	return execution.MatchOptions{}
}

// bindBuildExecution deterministically selects an Environment execution class
// for every selected build that declares requirements. Ordinary builders do not
// infer a host platform from their target/toolchain platform; VM-specific hard
// constraints belong to the later VM lowering boundary.
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
