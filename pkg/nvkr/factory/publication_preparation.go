// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// completePublicationInput joins the original payload with independently
// attested control documents. The publisher verifies existing bundles only; it
// neither requests a signing identity nor creates signatures.
func completePublicationInput(ctx context.Context, plan *Plan, node Node, artifacts map[string]Artifact, newSigner signerFor, expectedRoot string) (string, error) {
	route, prepared := preparedEvidenceRoute(plan, node)
	if !prepared {
		if len(node.Inputs) != 1 {
			return "", e.New(e.CodePipelineDrift, "publication requires one routed input")
		}
		return artifacts[node.Inputs[0]].Path, nil
	}
	if len(node.Inputs) != 3 || !containsString(node.Inputs, route.Input) || !containsString(node.Inputs, "tool/"+plan.Definition.CosignTool) {
		return "", e.New(e.CodePipelineDrift, "publication completion requires its preparation, attested controls and pinned verifier")
	}
	payload, err := signNodeInput(node, artifacts, PackageOCI)
	if err != nil {
		return "", err
	}
	if payload.ID != route.Input {
		return "", e.New(e.CodePipelineDrift, "publication preparation differs from its route")
	}
	controls, err := signNodeInput(node, artifacts, ArtifactAttestedEvidenceMetadata)
	if err != nil {
		return "", err
	}
	inspection, err := delivery.InspectMetadata(ctx, controls.Path)
	if err != nil {
		return "", err
	}
	if expectedRoot != "" && inspection.RootDigest != expectedRoot {
		return "", e.New(e.CodeVerifyFailed, "attested controls changed after publication privacy admission")
	}
	if err := admitEvidenceMetadata(plan, route, inspection); err != nil {
		return "", err
	}
	preparation, err := delivery.Inspect(ctx, payload.Path)
	if err != nil {
		return "", err
	}
	if err := admitEvidenceMetadata(plan, route, preparation); err != nil {
		return "", err
	}
	adapter, err := newSigner(ctx, plan, node, artifacts)
	if err != nil {
		return "", err
	}
	verifier, ok := adapter.(sign.SBOMAttester)
	if !ok {
		return "", e.New(e.CodeEngineCapability, "publication completion requires signed SBOM verification")
	}
	workspace := plan.workspace
	if workspace == "" {
		workspace = filepath.Join(".nvkr", "factory")
	}
	out := filepath.ToSlash(filepath.Join(workspace, "packages", payload.Name))
	result, err := delivery.CompleteAttestedPreparation(ctx, payload.Path, controls.Path, out, preparation.RootDigest, inspection.RootDigest, verifier.VerifySBOM)
	if err != nil {
		return "", err
	}
	if result.RootDigest != inspection.RootDigest {
		return "", e.New(e.CodeVerifyFailed, "completed publication differs from attested controls")
	}
	return result.LayoutPath, nil
}
