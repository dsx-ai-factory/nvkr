// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
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
	if len(seen) == 0 {
		return nil
	}
	return validateRuntimeConsumerEvidence(ctx, plan, artifacts, local)
}

func validateRuntimeConsumerEvidence(ctx context.Context, plan *Plan, artifacts map[string]Artifact, local func(string) (string, error)) error {
	identity, err := plan.IdentityDigest()
	if err != nil {
		return err
	}
	for _, consumer := range plan.Graph.Nodes {
		if consumer.Kind != NodeBuild || len(consumer.InternalTools) == 0 {
			continue
		}
		_, instruction, err := plan.producerInstruction(consumer)
		if err != nil {
			return err
		}
		if instruction.Runtime == nil {
			continue
		}
		var tool InternalToolPlan
		for _, candidate := range plan.InternalTools {
			if candidate.Name == instruction.Runtime.Tool {
				tool = candidate
			}
		}
		if tool.Name == "" || tool.Format != definition.OutputOCI {
			return e.New(e.CodeProvenanceMissing, "runtime consumer has no named production")
		}
		var resultArtifact Artifact
		for _, id := range consumer.Outputs {
			if artifacts[id].Kind == "producer-result" {
				resultArtifact = artifacts[id]
			}
		}
		if resultArtifact.ID == "" {
			return e.New(e.CodeProvenanceMissing, "runtime consumer has no result")
		}
		path, err := local(resultArtifact.Path)
		if err != nil {
			return err
		}
		result, err := readProducerResult(ctx, path)
		if err != nil {
			return err
		}
		if err := pipeline.ValidateArtifactRuntimeUse(instruction, result); err != nil {
			return err
		}
		use := result.RuntimeUse
		subject := strings.SplitN(use.Subject, "/", 3)
		if use.PlanDigest != identity || len(subject) != 3 || subject[2] != consumer.ID {
			return e.New(e.CodeVerifyFailed, "runtime use changes its exact Plan or consumer")
		}
		for _, evidence := range []struct{ id, digest string }{{tool.ObservationArtifactID, use.Observation}, {tool.SBOMArtifactID, use.SBOM}} {
			path, err := local(artifacts[evidence.id].Path)
			if err != nil {
				return err
			}
			digest, err := digestFactoryPath(ctx, path)
			if err != nil {
				return err
			}
			if digest != evidence.digest {
				return e.New(e.CodeVerifyFailed, "runtime use changes its named producer's completed evidence")
			}
		}
		content, err := observedRuntimeContent(ctx, plan, tool, artifacts, local)
		if err != nil {
			return err
		}
		path, err = local(artifacts[tool.SBOMArtifactID].Path)
		if err != nil {
			return err
		}
		raw, err := readFactorySBOMFile(ctx, filepath.Join(path, tool.Component+".cdx.json"), "runtime contents binding")
		if err != nil {
			return err
		}
		var bom sbom.BOM
		if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &bom); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "decoding runtime contents binding")
		}
		layer := ""
		for _, property := range bom.Metadata.Props {
			if property.Name == "nvkr:runtimeLayerDigest" {
				layer = property.Value
			}
		}
		path, err = local(artifacts[tool.ResultArtifactID].Path)
		if err != nil {
			return err
		}
		produced, err := readProducerResult(ctx, path)
		if err != nil {
			return err
		}
		_, producer, err := plan.producerInstruction(planNode(plan, artifacts[tool.ResultArtifactID].Producer))
		if err != nil {
			return err
		}
		if err := pipeline.ValidateRuntimeProductionDescription(use.Production, producer, produced, content.layout, layer); err != nil {
			return err
		}
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
