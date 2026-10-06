package factory

import (
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// producerInstruction resolves an exact scheduler reference to its sole executable
// definition. Callers validate the complete Plan before rendering or executing it.
func (plan Plan) producerInstruction(node Node) (BuildPlan, ir.Node, error) {
	for _, build := range plan.Builds {
		if build.Name != node.BuildTarget || build.Graph == nil {
			continue
		}
		for _, instruction := range build.Graph.Nodes {
			if instruction.ID != node.IRNode {
				continue
			}
			if node.ID != build.Name+"/"+instruction.ID {
				return BuildPlan{}, ir.Node{}, e.New(e.CodePipelineDrift, "operation %q changes its producer reference", node.ID)
			}
			kind := map[string]string{ir.KindFetch: NodeFetch, ir.KindPull: NodeAcquire, ir.KindBuild: NodeBuild}[instruction.Kind]
			if kind == "" || node.Kind != kind {
				return BuildPlan{}, ir.Node{}, e.New(e.CodePipelineDrift, "operation %q changes producer kind", node.ID)
			}
			return build, instruction, nil
		}
	}
	return BuildPlan{}, ir.Node{}, e.New(e.CodePipelineDrift, "operation %q references an absent producer %s/%s", node.ID, node.BuildTarget, node.IRNode)
}

// ExecutionFor resolves a scheduler operation's producer reference. The returned
// value is an ephemeral execution view; it is never written back into the Plan.
// Utility operations already own their instruction and are returned unchanged.
func (plan Plan) ExecutionFor(node Node) (Node, error) {
	resolved, err := plan.executionForProducer(node)
	if err != nil {
		return Node{}, err
	}
	return projectARCNode(&plan, resolved), nil
}

func (plan Plan) executionForProducer(node Node) (Node, error) {
	if node.IRNode == "" {
		return node, nil
	}
	build, instruction, err := plan.producerInstruction(node)
	if err != nil {
		return Node{}, err
	}
	node.RunsOn = plan.Environment.RunsOn
	if node.RunsOn == "" {
		node.RunsOn = DefaultRunsOn
	}
	node.Platform = build.Graph.Platform
	switch instruction.Kind {
	case ir.KindFetch:
		node.Network = ir.NetworkDefault
		node.Container = instruction.VendorImage
	case ir.KindPull:
		node.Network = ir.NetworkDefault
	case ir.KindBuild:
		node.Network = instruction.Network
		node.Container = instruction.Toolchain
		node.Platform = instruction.Platform
		node.Requirements = execution.Clone(instruction.Requirements)
		node.ExecutionBinding = execution.CloneBinding(instruction.ExecutionBinding)
		if instruction.ExecutionBinding != nil {
			node.RunsOn = instruction.ExecutionBinding.Class.RunsOn
		}
	}
	return node, nil
}
