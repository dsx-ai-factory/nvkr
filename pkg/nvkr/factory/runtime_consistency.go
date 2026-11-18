// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type runtimeContent struct {
	manifest string
	layout   string
	size     int64
}

// validateRuntimeConsistency compares independent productions of the same
// selected runtime at Run completion. Hosted callers must first authenticate
// same-Run transport. These observations cannot restore execution capabilities.
func validateRuntimeConsistency(ctx context.Context, plan *Plan, artifacts map[string]Artifact, local func(string) (string, error)) error {
	type producer struct{ build, component, platform string }
	seen := make(map[producer]runtimeContent)
	for _, tool := range plan.InternalTools {
		if tool.Format != definition.OutputOCI {
			continue
		}
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "comparing independent runtime productions")
		}
		// Each named requirement must retain its own complete indexed inventory,
		// even when another requirement has an equal Build or subject digest.
		if _, err := sbomPublicationObjects(ctx, plan, artifacts[tool.SBOMArtifactID], artifacts, local); err != nil {
			return err
		}
		content, err := observedRuntimeContent(ctx, plan, tool, artifacts, local)
		if err != nil {
			return err
		}
		key := producer{tool.BuildDigest, tool.Component, tool.Platform}
		if previous, ok := seen[key]; ok && previous != content {
			return e.New(e.CodeVerifyFailed, "independent runtime production %q differs from the same selected producer's other disk groups", tool.Name)
		}
		seen[key] = content
	}
	return nil
}

// This reads descriptions emitted by the original owner. The full OCI layout
// stays in its disk group; its terminal digest and manifest subject are compared
// here only after the provider has authenticated the group's transported bytes.
func observedRuntimeContent(ctx context.Context, plan *Plan, tool InternalToolPlan, artifacts map[string]Artifact, local func(string) (string, error)) (runtimeContent, error) {
	path, err := local(artifacts[tool.ResultArtifactID].Path)
	if err != nil {
		return runtimeContent{}, err
	}
	result, err := readProducerResult(ctx, path)
	if err != nil {
		return runtimeContent{}, err
	}
	if result.Component != tool.Component || !canonical.IsDigest(result.SHA256) || result.Size <= 0 {
		return runtimeContent{}, e.New(e.CodeVerifyFailed, "runtime consistency result changes its selected subject")
	}
	path, err = local(artifacts[tool.ObservationArtifactID].Path)
	if err != nil {
		return runtimeContent{}, err
	}
	raw, err := readSmallFactoryFile(ctx, path, "runtime consistency terminal observation")
	if err != nil {
		return runtimeContent{}, err
	}
	var observed factoryObservation
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &observed); err != nil {
		return runtimeContent{}, e.Wrap(e.CodeVerifyFailed, err, "decoding runtime consistency observation")
	}
	node := planNode(plan, artifacts[tool.ObservationArtifactID].Producer)
	if node.Kind != NodeVerify || node.BuildTarget != tool.BuildTarget ||
		observed.SchemaVersion != "nvkr.dev/factory-observation/v2" || observed.Qualification != TierStaticUnqualified ||
		observed.BuildDigest != tool.BuildDigest || observed.BuildTarget != tool.BuildTarget || len(observed.Inputs) != len(node.Inputs) {
		return runtimeContent{}, e.New(e.CodeVerifyFailed, "runtime consistency observation changes its exact producer closure")
	}
	content := runtimeContent{manifest: result.SHA256, size: result.Size}
	resultBound := false
	for i, input := range observed.Inputs {
		planned := artifacts[node.Inputs[i]]
		if input.ArtifactID != planned.ID || input.Kind != planned.Kind || !canonical.IsDigest(input.Digest) {
			return runtimeContent{}, e.New(e.CodeVerifyFailed, "runtime consistency observation has an unbound input")
		}
		if input.ArtifactID == tool.ArtifactID {
			content.layout = input.Digest
		}
		if planned.Kind != "producer-result" {
			continue
		}
		path, err := local(planned.Path)
		if err != nil {
			return runtimeContent{}, err
		}
		digest, err := digestFactoryPath(ctx, path)
		if err != nil {
			return runtimeContent{}, err
		}
		if input.Digest != digest {
			return runtimeContent{}, e.New(e.CodeVerifyFailed, "runtime consistency observation changed its producer result")
		}
		resultBound = resultBound || input.ArtifactID == tool.ResultArtifactID
	}
	if content.layout == "" || !resultBound {
		return runtimeContent{}, e.New(e.CodeProvenanceMissing, "runtime consistency observation omits its exact layout or result")
	}
	return content, nil
}
