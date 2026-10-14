// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"io"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// DebianProducerRun provides source routing and diagnostic writers, never a
// result, archive path, selected instruction or evidence assertion.
type DebianProducerRun struct {
	SourceRoot     string
	Stdout, Stderr io.Writer
}

// AdmitDebianProducer prepares one exact standalone producer through the complete
// graph and native held-runtime owners. It performs no source, container or output
// work and issues no factory Run, inventory, qualification or release authority.
// The local factory scheduler supplies separate original Run/content/terminal joins.
func (session *NodeSession) AdmitDebianProducer(ctx context.Context, graph *ir.Graph, buildDigest,
	subject, nodeID, runtimeName string,
) (*DebianProducerExecutor, error) {
	executor, err := session.admitDebianProducers(ctx, graph, buildDigest, []debianProducerSelection{{subject, nodeID}})
	if err != nil {
		return nil, err
	}
	if err := executor.admitRuntime(ctx, runtimeName); err != nil {
		return nil, err
	}
	return executor, nil
}

// RunBoundContext executes only the original exclusive binding, using the admitted
// native executable and selected frozen instruction. Successful unqualified byte
// retention follows authenticated source materialization and checked cleanup; it
// does not validate package format or establish final evidence/install acceptance.
func (executor *DebianProducerExecutor) RunBoundContext(ctx context.Context, binding *DebianProducerBinding,
	request DebianProducerRun,
) (*DebianProducerArchive, *ArtifactResult, error) {
	if err := requireRunContext(ctx, "bound Debian producer"); err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "bound Debian producer")
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, nil, e.New(e.CodeEngineCapability, "bound Debian producer requires a bounded context")
	}
	if executor == nil || executor.state == nil || binding == nil || binding.state == nil {
		return nil, nil, e.New(e.CodeProvenanceMissing, "original Debian executor and exclusive binding are required")
	}
	if binding.state.executor != executor.state {
		return nil, nil, e.New(e.CodeVerifyFailed, "Debian binding belongs to another original executor")
	}
	executor.state.mu.Lock()
	admitted := executor.state.runtime
	executor.state.mu.Unlock()
	if admitted == nil {
		return nil, nil, e.New(e.CodeEngineCapability, "bound Debian producer requires native held-runtime admission")
	}
	return executor.run(ctx, binding.state.subject, RunOptions{Ctx: ctx, Launch: true, SourceRoot: request.SourceRoot,
		Runtime: admitted.name, Stdout: request.Stdout, Stderr: request.Stderr, debianBinding: binding.state})
}
