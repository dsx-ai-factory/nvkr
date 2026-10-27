// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// RestoredSourceDescription is a point-in-time projection of checked source
// bytes. It grants no release, qualification or future pathname authority.
type RestoredSourceDescription struct {
	Node          string `json:"node"`
	Path          string `json:"path"`
	Commit        string `json:"commit"`
	TreeHash      string `json:"treeHash"`
	ClosureDigest string `json:"closureDigest"`
}

// InspectRestoredSource selects one exact fetch operation from a complete Plan
// and delegates unsigned receipt/checkout consistency checks to the source owner.
// Its closure digest does not authenticate vendor bytes against published evidence.
// It performs no acquisition, vendoring, producer execution or output writes.
func InspectRestoredSource(ctx context.Context, plan *Plan, nodeID string) (RestoredSourceDescription, error) {
	if ctx == nil || plan == nil {
		return RestoredSourceDescription{}, e.New(e.CodeUsage, "source inspection requires a context and complete Plan")
	}
	if err := ctx.Err(); err != nil {
		return RestoredSourceDescription{}, e.Wrap(e.CodeTimeout, err, "source inspection canceled")
	}
	if err := plan.Validate(); err != nil {
		return RestoredSourceDescription{}, err
	}
	nodes, artifacts := planIndex(plan)
	node, exists := nodes[nodeID]
	if !exists || node.Kind != NodeFetch {
		return RestoredSourceDescription{}, e.New(e.CodeInvalidCatalog, "source inspection requires an exact planned fetch node %q", nodeID)
	}
	_, instruction, err := plan.producerInstruction(node)
	if err != nil {
		return RestoredSourceDescription{}, err
	}
	source := artifacts["source/"+node.BuildTarget+"/"+node.IRNode]
	suffix := "/" + instruction.Dest
	if source.Path == "" || !strings.HasSuffix(source.Path, suffix) {
		return RestoredSourceDescription{}, e.New(e.CodePipelineDrift, "source output for %q does not bind its destination", nodeID)
	}
	if err := admitSourceFetchPath(ctx, source.Path, true); err != nil {
		return RestoredSourceDescription{}, err
	}
	receipt := artifacts["source-receipt/"+node.BuildTarget+"/"+node.IRNode]
	if receipt.Path == "" {
		return RestoredSourceDescription{}, e.New(e.CodePipelineDrift, "source output for %q has no receipt", nodeID)
	}
	sourceRoot := strings.TrimSuffix(source.Path, suffix)
	derivedReceipt, err := pipeline.SourceReceiptPath(sourceRoot, instruction.Dest)
	if err != nil {
		return RestoredSourceDescription{}, err
	}
	if filepath.Clean(receipt.Path) != filepath.Clean(derivedReceipt) {
		return RestoredSourceDescription{}, e.New(e.CodePipelineDrift, "planned source receipt differs from the source owner's derived path")
	}
	if err := admitSourceFetchPath(ctx, receipt.Path, false); err != nil {
		return RestoredSourceDescription{}, err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
	}
	digest, err := pipeline.InspectRestoredSource(ctx, instruction, sourceRoot)
	if err != nil {
		return RestoredSourceDescription{}, err
	}
	path, err := filepath.Abs(source.Path)
	if err != nil {
		return RestoredSourceDescription{}, e.Wrap(e.CodeFetchFailed, err, "resolving checked source path")
	}
	return RestoredSourceDescription{Node: nodeID, Path: path, Commit: instruction.Commit, TreeHash: instruction.TreeHash, ClosureDigest: digest}, nil
}
