// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// prepareImageIndex validates each independent producer and SBOM handoff before
// importing their exact archive bytes. The returned held closure outlives staging.
func prepareImageIndex(ctx context.Context, plan *Plan, required *ImageIndexPlan, artifacts map[string]Artifact) (*delivery.ImageIndex, *delivery.ImageIndexBinding, error) {
	if ctx == nil {
		return nil, nil, e.New(e.CodeUsage, "image index packaging requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "image index packaging canceled")
	}
	if _, err := required.buildDigest(); err != nil {
		return nil, nil, err
	}
	results := make([]pipeline.ArtifactResult, len(required.Children))
	for i, child := range required.Children {
		input, ok := artifacts[child.ArtifactID]
		if !ok || input.ID != child.ArtifactID || input.Kind != "oci-image" || input.BuildTarget != child.BuildTarget || input.Component != child.Component {
			return nil, nil, e.New(e.CodeReleaseIncomplete, "image index omits exact native payload %q", child.ArtifactID)
		}
		record, ok := artifacts[child.ResultArtifactID]
		if !ok || record.ID != child.ResultArtifactID || record.Kind != "producer-result" || record.BuildTarget != child.BuildTarget || record.Component != child.Component {
			return nil, nil, e.New(e.CodeReleaseIncomplete, "image index omits exact native producer result %q", child.ResultArtifactID)
		}
		evidence, ok := artifacts[child.SBOMArtifactID]
		if !ok || evidence.ID != child.SBOMArtifactID || evidence.Kind != "sbom-set" || evidence.BuildTarget != child.BuildTarget {
			return nil, nil, e.New(e.CodeProvenanceMissing, "image index omits exact native inventory %q", child.SBOMArtifactID)
		}
		result, err := observeProducerPayload(ctx, plan, input, artifacts)
		if err != nil {
			return nil, nil, err
		}
		results[i] = result
		if _, err := sbomPublicationObjects(ctx, plan, evidence, artifacts, func(path string) (string, error) { return path, nil }); err != nil {
			return nil, nil, err
		}
	}
	stage, err := os.MkdirTemp("", "nvkr-image-index-*")
	if err != nil {
		return nil, nil, e.Wrap(e.CodeInternal, err, "creating native image import stage")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	inputs := make([]delivery.ImageIndexInput, 0, len(required.Children))
	binding := &delivery.ImageIndexBinding{RequiredPlatforms: slices.Clone(required.RequiredPlatforms)}
	for i, child := range required.Children {
		layout := filepath.Join(stage, fmt.Sprintf("child-%d", i))
		image, err := delivery.ImportContainerImage(ctx, artifacts[child.ArtifactID].Path, results[i].SHA256, child.Platform, layout)
		if err != nil {
			return nil, nil, err
		}
		inputs = append(inputs, delivery.ImageIndexInput{Platform: child.Platform, LayoutPath: layout, ManifestDigest: image.ManifestDigest})
		binding.Children = append(binding.Children, delivery.ImageIndexChildBinding{
			Platform: child.Platform, ManifestDigest: image.ManifestDigest, BuildDigest: child.BuildDigest, Component: child.Component, BuildSubject: results[i].SHA256,
		})
	}
	index, err := delivery.AssembleImageIndex(ctx, required.RequiredPlatforms, inputs)
	if err != nil {
		return nil, nil, err
	}
	description, err := index.Description()
	if err != nil {
		_ = index.Close()
		return nil, nil, err
	}
	for i, child := range description.Children {
		binding.Children[i].Size = child.Size
	}
	if err := binding.Validate(); err != nil {
		_ = index.Close()
		return nil, nil, err
	}
	return index, binding, nil
}

func executeImageIndexPackage(ctx context.Context, plan *Plan, node Node, output Artifact, artifacts map[string]Artifact, stdout io.Writer) error {
	if err := node.ImageIndex.ValidateFor(output.Name, "", nil); err != nil {
		return err
	}
	index, _, err := prepareImageIndex(ctx, plan, node.ImageIndex, artifacts)
	if err != nil {
		return err
	}
	defer func() { _ = index.Close() }()
	if err := os.MkdirAll(filepath.Dir(output.Path), 0o755); err != nil {
		return e.Wrap(e.CodeInternal, err, "creating image index output parent")
	}
	if err := index.WriteLayout(ctx, output.Path); err != nil {
		return err
	}
	description, err := index.Description()
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Packaged container image %q: index %s (%d platforms)\n", output.Name, description.Digest, len(description.Children))
	return nil
}

func observeImageIndex(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, artifact Artifact, artifacts map[string]Artifact) (LifecyclePayloadObservation, error) {
	if err := expected.ImageIndex.ValidateFor(expected.Name, expected.BuildDigest, expected.Components); err != nil {
		return LifecyclePayloadObservation{}, err
	}
	node := planNode(plan, artifact.Producer)
	if !reflect.DeepEqual(node.ImageIndex, expected.ImageIndex) {
		return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "image index requirements differ from their package producer")
	}
	reproduced, binding, err := prepareImageIndex(ctx, plan, expected.ImageIndex, artifacts)
	if err != nil {
		return LifecyclePayloadObservation{}, err
	}
	defer func() { _ = reproduced.Close() }()
	description, err := reproduced.Description()
	if err != nil {
		return LifecyclePayloadObservation{}, err
	}
	actual, err := delivery.OpenImageIndex(ctx, artifact.Path, description.Digest, expected.ImageIndex.RequiredPlatforms)
	if err != nil {
		return LifecyclePayloadObservation{}, err
	}
	defer func() { _ = actual.Close() }()
	return LifecyclePayloadObservation{Digest: description.Digest, SemanticDigest: description.Digest, Components: slices.Clone(expected.Components), ImageIndex: binding}, nil
}

// inspectFactoryImageIndex observes the index under compiler-frozen membership.
// Producer/evidence authentication is a separate exact join in observeImageIndex.
func inspectFactoryImageIndex(ctx context.Context, plan *Plan, artifact Artifact) (delivery.ImageIndexDescription, error) {
	required := planNode(plan, artifact.Producer).ImageIndex
	if err := required.ValidateFor(artifact.Name, "", nil); err != nil {
		return delivery.ImageIndexDescription{}, err
	}
	raw, err := readSmallFactoryFile(ctx, filepath.Join(artifact.Path, "index.json"), "image index")
	if err != nil {
		return delivery.ImageIndexDescription{}, err
	}
	index, err := delivery.OpenImageIndex(ctx, artifact.Path, pipeline.FileDigest(raw), required.RequiredPlatforms)
	if err != nil {
		return delivery.ImageIndexDescription{}, err
	}
	defer func() { _ = index.Close() }()
	return index.Description()
}
