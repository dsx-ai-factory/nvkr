// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// bindExecutor borrows the original Run operation, validates the exact named
// output's complete graph and uninvoked producer, and freezes a concrete executor
// facade before future work. The local scheduler composes this binding with
// producer/content/terminal operations; binding alone issues no final evidence.
func (state *debianRunState) bindExecutor(ctx context.Context, result *LocalRunResult, plan *Plan,
	output LifecycleOutputPlan, executor *pipeline.DebianProducerExecutor,
) error {
	owned, finish, err := state.begin(ctx, result, plan, output)
	if err != nil {
		return err
	}
	defer finish()
	if output.Kind != authored.OutputDebianPackage || executor == nil {
		return e.New(e.CodeProvenanceMissing, "named Debian output requires its original executor")
	}
	// Freeze the opaque facade before comparisons and read the originally owned
	// Plan rather than following caller mutations after begin's byte comparison.
	frozenExecutor := *executor
	var originalPlan Plan
	if err := canonical.UnmarshalExact(state.plan, &originalPlan); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading original Debian Run Plan")
	}
	var selected *Artifact
	for i := range originalPlan.Graph.Artifacts {
		if originalPlan.Graph.Artifacts[i].ID == output.ArtifactID {
			selected = &originalPlan.Graph.Artifacts[i]
			break
		}
	}
	if selected == nil {
		return e.New(e.CodeVerifyFailed, "named Debian output has no original artifact")
	}
	for _, node := range originalPlan.Graph.Nodes {
		if node.ID != selected.Producer {
			continue
		}
		build, instruction, err := originalPlan.producerInstruction(node)
		if err != nil {
			return err
		}
		if err := frozenExecutor.RequireBuildContext(owned, &state.session, build.Graph, output.BuildDigest); err != nil {
			return err
		}
		var installed *pipeline.DebianProducerBinding
		err = frozenExecutor.BindSelectionContext(owned, output.ArtifactID, instruction,
			func(bindingContext context.Context, binding *pipeline.DebianProducerBinding) error {
				state.mu.Lock()
				defer state.mu.Unlock()
				if state.closed {
					return e.New(e.CodeEngineCapability, "original Debian Run closed before executor binding")
				}
				if err := bindingContext.Err(); err != nil {
					return e.Wrap(e.CodeTimeout, err, "binding original Debian executor")
				}
				if _, exists := state.executors[output.ArtifactID]; exists {
					return e.New(e.CodeVerifyFailed, "named Debian output already has its original executor")
				}
				if state.executors == nil {
					state.executors = make(map[string]debianRunExecutorBinding)
				}
				state.executors[output.ArtifactID] = debianRunExecutorBinding{executor: frozenExecutor, invocation: *binding}
				installed = binding
				return nil
			})
		if err != nil && installed != nil {
			state.mu.Lock()
			if entry, ok := state.executors[output.ArtifactID]; ok && entry.invocation == *installed {
				delete(state.executors, output.ArtifactID)
			}
			state.mu.Unlock()
		}
		return err
	}
	return e.New(e.CodeVerifyFailed, "named Debian output has no original producer")
}

type debianRunExecutorBinding struct {
	executor   pipeline.DebianProducerExecutor
	invocation pipeline.DebianProducerBinding
}
