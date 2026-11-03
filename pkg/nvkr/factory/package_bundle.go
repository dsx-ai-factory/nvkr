// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Bundle packaging preserves the exact observed compressed archive. Delivery
// holds and rechecks those bytes before producing the OCI envelope.
func executeBundlePackage(ctx context.Context, plan *Plan, node Node, output Artifact, inputs []Artifact, artifacts map[string]Artifact) error {
	if len(inputs) != 1 || inputs[0].Kind != "tarball" || inputs[0].BuildTarget != output.Name ||
		inputs[0].Component == "" || planNode(plan, inputs[0].Producer).Kind != NodeBuild || node.PackageKind != PackageBundle {
		return e.New(e.CodeInvalidCatalog, "bundle package %q requires its exact source-built archive", node.ID)
	}
	result, err := observeProducerPayload(ctx, plan, inputs[0], artifacts)
	if err != nil {
		return err
	}
	_, err = delivery.BuildBundleArtifact(ctx, output.Path, output.Name, inputs[0].Path, result.SHA256)
	return err
}

func (plan *Plan) hasBundleTarget(name string) bool {
	for _, output := range plan.Definition.Outputs {
		if output.Name == name && output.Format == PackageBundle {
			return true
		}
	}
	return false
}
