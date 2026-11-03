// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"runtime"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// operationSession is the target-neutral executor for ordinary operations of one
// complete Plan. The execute-node helper, hosted GitHub steps and local
// scheduling admit their Plan, then open a session whose placement says only
// where work runs. Routing, per-operation admission and kind dispatch live here
// once, so the targets cannot drift apart. It issues no new authority: grouped
// disk work, signing identity and Run completion keep their existing owners.
type operationSession struct {
	plan      *Plan
	placement operationPlacement
	stdout    io.Writer
	stderr    io.Writer
}

// operationPlacement projects ordinary operations onto one target. It cannot
// select, add or reorder work.
type operationPlacement struct {
	// workspace relocates compiled artifact paths before in-process and
	// producer work; empty executes the compiled layout.
	workspace string
	runtime   string
	jobs      int
	producer  func(ir.Node, pipeline.RunOptions) (*pipeline.ArtifactResult, error)
	// owner holds disk material for a local VM Run; nil elsewhere.
	owner *LocalRunResult
	// contained selects utilities launched in their pinned container, whose
	// execute-node child admits the operation again.
	contained func(*Plan, Node) bool
	launch    func(context.Context, Node) error
	// owned selects operations the opening Run already admitted with its whole
	// Plan; every other in-process operation passes admitOperation first.
	owned func(*Plan, Node) bool
}

type operationRoute string

const (
	routeAdmitted  operationRoute = "admitted"
	routeOwned     operationRoute = "owned"
	routeContained operationRoute = "contained"
)

func newOperationSession(plan *Plan, placement operationPlacement, stdout, stderr io.Writer) *operationSession {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	return &operationSession{plan: plan, placement: placement, stdout: stdout, stderr: stderr}
}

// compiledPlacement runs producers once with the Environment runtime in the
// compiled layout, as the execute-node helper and hosted steps do.
func compiledPlacement(plan *Plan) operationPlacement {
	return operationPlacement{runtime: plan.Environment.RuntimeBinary(), jobs: 1, producer: (pipeline.Local{}).ExecNode}
}

func (session *operationSession) route(node Node) operationRoute {
	placement := session.placement
	if node.IRNode == "" && placement.contained != nil && placement.contained(session.plan, node) {
		return routeContained
	}
	if placement.owned != nil && placement.owned(session.plan, node) {
		return routeOwned
	}
	return routeAdmitted
}

// execute is the context-first executor for one exact operation.
func (session *operationSession) execute(ctx context.Context, node Node) error {
	route := session.route(node)
	if route == routeContained {
		return session.placement.launch(ctx, node)
	}
	plan := session.plan
	if session.placement.workspace != "" {
		var err error
		if plan, err = relocateFactoryPlan(plan, session.placement.workspace); err != nil {
			return err
		}
	}
	if route == routeAdmitted {
		var err error
		if node, err = admitOperation(ctx, plan, node.ID); err != nil {
			return err
		}
	}
	return session.dispatch(ctx, plan, node)
}

// admitOperation validates the complete Plan and one exact operation for
// execution in this process without an owning Run.
func admitOperation(ctx context.Context, plan *Plan, nodeID string) (Node, error) {
	node, err := admitExecutionNode(ctx, plan, nodeID)
	if err != nil {
		return Node{}, err
	}
	if githubDiskOutput(plan, node) != "" {
		return Node{}, e.New(e.CodeEngineCapability, "ARC disk operations require the original grouped executor")
	}
	if err := rejectUnsupportedRuntimeStorage(plan, "factory execute-node"); err != nil {
		return Node{}, err
	}
	if len(plan.Environment.GitHubRunners) > 0 {
		if err := admitGitHubNodePlatform(plan, node, runtime.GOOS+"/"+runtime.GOARCH); err != nil {
			return Node{}, err
		}
		// Producer admission runs here; hosted utility admission runs on the host
		// in executeGitHubNode before entering a container without daemon access.
		if node.IRNode != "" {
			if err := checkNodeContainerPlatform(ctx, plan, node, pipeline.CheckContainerPlatform); err != nil {
				return Node{}, err
			}
		}
	}
	return node, nil
}

// dispatch runs an admitted operation in this process. Producer instructions
// remain owned by the embedded build IR.
func (session *operationSession) dispatch(ctx context.Context, plan *Plan, node Node) error {
	if node.IRNode != "" {
		var owner *LocalRunResult
		for _, output := range plan.LifecycleOutputs {
			if output.Name == node.BuildTarget && output.Kind == authored.OutputDiskImage {
				owner = session.placement.owner
			}
		}
		return executeProducer(ctx, plan, node, session.placement.runtime, session.placement.jobs,
			session.stdout, session.stderr, session.placement.producer, owner)
	}
	_, artifacts := planIndex(plan)
	switch node.Kind {
	case NodeAcquire:
		return executeToolAcquire(ctx, node, artifacts[node.Outputs[0]], session.stdout)
	case NodeVerify:
		return executeObservation(ctx, plan, node, artifacts)
	case NodeSBOM:
		return executeSBOM(ctx, plan, node, artifacts, session.stdout)
	case NodeAssembleRelease:
		return executeReferenceRelease(ctx, plan, node, artifacts, productionOCISigner)
	case NodePackage:
		return executePackage(ctx, plan, node, artifacts, session.stdout)
	case NodePublish:
		return executePublish(ctx, plan, node, artifacts, session.stdout)
	case NodeAttestEvidence:
		return executeAttestEvidence(ctx, plan, node, artifacts, productionOCISigner, session.stdout)
	case NodeSign:
		return executeSign(ctx, plan, node, artifacts, productionOCISigner, session.stdout)
	case NodeVerifyPublication:
		return executeVerifyPublication(ctx, plan, node, artifacts, productionOCISigner, session.stdout)
	default:
		return e.New(e.CodeEngineCapability, "factory node kind %q has no executable adapter", node.Kind)
	}
}
