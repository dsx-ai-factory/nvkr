// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"runtime"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// ComputeInternalToolSBOM computes descriptions in the selected offline utility
// container. It cannot restore production, retain completion, or consume a tool.
// The original grouped Run admits the launch and records validated completion
// only after this child returns successfully.
func ComputeInternalToolSBOM(ctx context.Context, plan *Plan, nodeID string, stdout io.Writer) error {
	node, err := admitExecutionNode(ctx, plan, nodeID)
	if err != nil {
		return err
	}
	if err := validateInternalToolSBOMNode(plan, node); err != nil {
		return err
	}
	if err := admitGitHubNodePlatform(plan, node, runtime.GOOS+"/"+runtime.GOARCH); err != nil {
		return err
	}
	_, artifacts := planIndex(plan)
	return executeSBOM(ctx, plan, node, artifacts, stdout)
}

func validateInternalToolSBOMNode(plan *Plan, node Node) error {
	tool, internal := plan.internalToolForTarget(node.BuildTarget)
	if !internal || node.Kind != NodeSBOM || node.IRNode != "" || node.ExecutionBinding != nil ||
		node.Container == "" || node.Network != ir.NetworkNone || len(node.Outputs) != 1 || node.Outputs[0] != tool.SBOMArtifactID {
		return e.New(e.CodeEngineCapability, "internal tool SBOM requires its exact selected offline utility")
	}
	return nil
}
