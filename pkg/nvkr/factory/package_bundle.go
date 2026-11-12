// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Bundle packaging preserves the exact observed compressed archive. Delivery
// holds and rechecks those bytes before producing the OCI envelope.
func executeBundlePackage(ctx context.Context, plan *Plan, node Node, output Artifact, inputs []Artifact, artifacts map[string]Artifact) error {
	if node.PackageKind != PackageBundle {
		return e.New(e.CodeInvalidCatalog, "invalid bundle package kind")
	}
	var root string
	for _, build := range plan.Builds {
		if build.Name == output.Name {
			var err error
			root, err = buildgraph.BundleRootName(build.ResolvedComponents)
			if err != nil {
				return err
			}
			if len(inputs) != len(build.ResolvedComponents) {
				return e.New(e.CodeVerifyFailed, "bundle package omits selected material closure")
			}
		}
	}
	var selected Artifact
	for _, input := range inputs {
		if input.BuildTarget != output.Name || input.Component == "" || (planNode(plan, input.Producer).Kind != NodeBuild && (input.Component == root || planNode(plan, input.Producer).Kind != NodeAcquire)) {
			return e.New(e.CodeInvalidCatalog, "bundle input has a foreign producer")
		}
		if _, err := observeProducerPayload(ctx, plan, input, artifacts); err != nil {
			return err
		}
		if input.Component == root {
			selected = input
		}
	}
	if selected.ID == "" {
		return e.New(e.CodeInvalidCatalog, "bundle package omits its exact root")
	}
	result, err := observeProducerPayload(ctx, plan, selected, artifacts)
	if err != nil {
		return err
	}
	if selected.Kind == "oci-layout" {
		clone := *plan
		clone.Graph.Artifacts = nil
		for _, id := range sortedKeys(artifacts) {
			clone.Graph.Artifacts = append(clone.Graph.Artifacts, artifacts[id])
		}
		for _, input := range inputs {
			instruction, err := producerInstructionFor(plan, input)
			if err != nil {
				return err
			}
			if len(instruction.Materials) == 0 {
				continue
			}
			materials, err := observeBuildMaterials(ctx, &clone, planNode(plan, input.Producer))
			if err != nil {
				return err
			}
			observed, err := observeProducerPayload(ctx, plan, input, artifacts)
			matches := err == nil && materials.manifest.Digest() == observed.MaterialManifestDigest
			closeErr := materials.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return e.Wrap(e.CodeInternal, closeErr, "closing bundle materials")
			}
			if !matches {
				return e.New(e.CodeVerifyFailed, "bundle producer material binding differs from observed closure")
			}
		}
	}
	if selected.Kind == "oci-layout" {
		_, err := delivery.ImportObservedBundleLayout(ctx, output.Path, selected.Path, result.SHA256)
		return err
	}
	_, err = delivery.BuildBundleArtifactWithRepresentation(ctx, output.Path, output.Name, selected.Path, result.SHA256, publicationOCIRepresentation(plan))
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
