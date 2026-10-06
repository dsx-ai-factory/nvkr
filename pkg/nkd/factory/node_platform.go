package factory

import (
	"context"
	"runtime"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// CheckNodePlatform is a standalone diagnostic facade that revalidates the Plan
// and selected operation and checks its native helper, daemon and pinned container.
// Generated CI uses executeGitHubNode, which calls checkNodeContainerPlatform on
// the host before launching a utility container without daemon access. These are
// point-in-time capability checks, not qualified runtime receipts or substitutes
// for ExecuteNode validation.
func CheckNodePlatform(ctx context.Context, plan *Plan, nodeID string) error {
	return checkNodePlatform(ctx, plan, nodeID, runtime.GOOS+"/"+runtime.GOARCH, pipeline.CheckContainerPlatform)
}

type containerPlatformCheck func(context.Context, string, string, string) error

func checkNodePlatform(ctx context.Context, plan *Plan, nodeID, observed string, check containerPlatformCheck) error {
	node, err := admitExecutionNode(ctx, plan, nodeID)
	if err != nil {
		return err
	}
	if len(plan.Environment.GitHubRunners) == 0 {
		return e.New(e.CodeEngineCapability, "node platform admission requires explicit native GitHub routes")
	}
	if err := admitGitHubNodePlatform(plan, node, observed); err != nil {
		return err
	}
	return checkNodeContainerPlatform(ctx, plan, node, check)
}

func checkNodeContainerPlatform(ctx context.Context, plan *Plan, node Node, check containerPlatformCheck) error {
	execution, err := plan.GitHubExecutionFor(node)
	if err != nil {
		return err
	}
	if execution.Container == "" {
		return nil
	}
	return check(ctx, plan.Environment.RuntimeBinary(), execution.Container, execution.Platform)
}

func admitExecutionNode(ctx context.Context, plan *Plan, nodeID string) (Node, error) {
	if ctx == nil {
		return Node{}, e.New(e.CodeUsage, "factory node execution requires a context")
	}
	if err := ctx.Err(); err != nil {
		return Node{}, e.Wrap(e.CodeTimeout, err, "factory node execution canceled")
	}
	if plan == nil {
		return Node{}, e.New(e.CodeUsage, "factory node execution requires a plan")
	}
	if err := plan.Validate(); err != nil {
		return Node{}, err
	}
	if err := rejectUnsupportedRuntimeStorage(plan, "factory execute-node"); err != nil {
		return Node{}, err
	}
	if err := rejectUnsupportedPlanRequirements(plan, "factory execute-node"); err != nil {
		return Node{}, err
	}
	for _, node := range plan.Graph.Nodes {
		if node.ID == nodeID {
			return node, nil
		}
	}
	return Node{}, e.New(e.CodeInvalidCatalog, "factory plan has no node %q", nodeID)
}
