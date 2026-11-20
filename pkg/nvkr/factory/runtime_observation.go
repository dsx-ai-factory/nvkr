// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/ociclosure"
)

// runtimeObservation is the input to the private observation renderer, not a
// production capability. Only the original owner invokes it for live execution.
type runtimeObservation struct{ artifact, layout string }

func (owner *LocalRunResult) executeRuntimeObservation(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, name string) error {
	runtime, selected, err := owner.internalRuntimeProduction(ctx, plan, name)
	if err != nil {
		return err
	}
	if node.Kind != NodeVerify || node.BuildTarget != selected.BuildTarget || len(node.Outputs) != 1 || node.Outputs[0] != selected.ObservationArtifactID {
		return e.New(e.CodePipelineDrift, "runtime observation changes its exact named requirement")
	}
	binding, err := runtime.BindingContext(ctx)
	if err != nil {
		return err
	}
	return executeObservationInputs(ctx, plan, node, artifacts, nil, &runtimeObservation{artifact: selected.ArtifactID, layout: binding.LayoutDigest})
}

// Runtime layout observations use the production owner's terminal-tree codec.
// Ordinary directories and disk payloads retain their existing digest families.
// Recomputing this description never creates a live production receipt.
func digestObservationInput(ctx context.Context, plan *Plan, artifact Artifact, path string) (string, error) {
	if selected, internal := plan.internalToolForTarget(artifact.BuildTarget); internal && selected.Format == definition.OutputOCI && artifact.ID == selected.ArtifactID {
		tree, err := ociclosure.Snapshot(ctx, path)
		if err != nil {
			return "", e.Wrap(e.CodeVerifyFailed, err, "observing internal runtime layout")
		}
		return tree.Digest(), nil
	}
	return digestFactoryPath(ctx, path)
}
