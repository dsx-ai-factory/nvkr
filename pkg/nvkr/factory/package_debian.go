// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const PackageDebian = definition.OutputDebianPackage

func (plan *Plan) debianPublicationTarget(name string) bool {
	if plan.Environment.Publication == nil {
		return false
	}
	for _, output := range plan.Definition.Outputs {
		if output.Name == name && output.Format == PackageDebian {
			return true
		}
	}
	return false
}

func executeDebianPackage(ctx context.Context, plan *Plan, node Node, output Artifact, inputs []Artifact, artifacts map[string]Artifact) error {
	if !plan.debianPublicationTarget(output.Name) || len(inputs) != 1 || inputs[0].Kind != "host-package" {
		return e.New(e.CodeVerifyFailed, "Debian carrier requires its declared original raw producer")
	}
	var expected LifecycleOutputPlan
	for _, candidate := range plan.LifecycleOutputs {
		if candidate.Name == output.Name && candidate.Kind == PackageDebian {
			expected = candidate
		}
	}
	if expected.ArtifactID != inputs[0].ID {
		return e.New(e.CodeVerifyFailed, "Debian carrier changes original lifecycle subject")
	}
	record := "result/" + inputs[0].BuildTarget + "/" + inputs[0].Component
	if !containsString(node.Inputs, record) {
		return e.New(e.CodeProvenanceMissing, "Debian carrier omits original producer result input")
	}
	observed, err := ObserveLifecyclePayload(ctx, plan, expected, artifacts)
	if err != nil {
		return err
	}
	_, err = delivery.BuildDebianArtifactWithRepresentation(ctx, output.Path, output.Name, inputs[0].Path, observed.Digest, publicationOCIRepresentation(plan))
	return err
}
