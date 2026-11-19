// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/helmarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
)

func derivedRequirement(plan *Plan, name string, binding *derivedoutput.Binding) (LifecycleOutputPlan, error) {
	for _, required := range plan.LifecycleOutputs {
		if required.Name == name && required.Derived != nil && reflect.DeepEqual(required.Derived, binding) {
			if err := binding.ValidateOutput(name, required.Kind, required.BuildDigest, required.Components); err != nil {
				return LifecycleOutputPlan{}, err
			}
			return required, nil
		}
	}
	return LifecycleOutputPlan{}, e.New(e.CodePipelineDrift, "inventory derivation lacks its exact compiled output requirement")
}

func observeDerivedMaterials(ctx context.Context, plan *Plan, binding *derivedoutput.Binding, artifacts map[string]Artifact) ([]derivedoutput.Material, error) {
	materials := make([]derivedoutput.Material, 0, len(binding.Inputs))
	for _, input := range binding.Inputs {
		found := false
		for _, required := range plan.LifecycleOutputs {
			if required.Name != input.Name || required.Kind != input.Kind || required.BuildDigest != input.BuildDigest || required.Derived != nil {
				continue
			}
			observed, err := ObserveLifecyclePayload(ctx, plan, required, artifacts)
			if err != nil {
				return nil, err
			}
			materials = append(materials, derivedoutput.Material{Name: input.Name, Digest: observed.Digest})
			found = true
		}
		if !found {
			return nil, e.New(e.CodeReleaseIncomplete, "derived inventory lacks its exact sibling material")
		}
	}
	return materials, binding.ValidateMaterials(materials)
}

// projectPackagedInventory authenticates sibling results through their ordinary
// observer before projecting. Locations are compiled data; no registry is read.
func projectPackagedInventory(ctx context.Context, plan *Plan, binding *derivedoutput.Binding, artifacts map[string]Artifact) ([]byte, error) {
	var description releaseview.Description
	for _, input := range binding.Inputs {
		var required *LifecycleOutputPlan
		for i := range plan.LifecycleOutputs {
			candidate := &plan.LifecycleOutputs[i]
			if candidate.Name == input.Name && candidate.Kind == input.Kind && candidate.BuildDigest == input.BuildDigest && candidate.Derived == nil {
				required = candidate
			}
		}
		if required == nil {
			return nil, e.New(e.CodeVerifyFailed, "inventory sibling differs from its exact compiled requirement")
		}
		observed, err := ObserveLifecyclePayload(ctx, plan, *required, artifacts)
		if err != nil {
			return nil, err
		}
		output := releaseview.Output{Name: input.Name, Kind: input.Kind, Digest: observed.Digest, BuildDigest: input.BuildDigest,
			Reference: binding.RepositoryPrefix + "/" + input.Name + "@" + observed.Digest}
		if input.Kind == "helm-oci" {
			descriptor, payload, err := delivery.ReadArtifact(ctx, artifacts[required.ArtifactID].Path)
			if err != nil {
				return nil, err
			}
			if descriptor.Digest != observed.Digest {
				return nil, e.New(e.CodeVerifyFailed, "chart changed during inventory projection")
			}
			identity, err := helmarchive.Inspect(ctx, payload)
			if err != nil {
				return nil, err
			}
			chart, _, err := delivery.ReadHelmChart(ctx, artifacts[required.ArtifactID].Path, observed.Digest, identity.Name, identity.Version)
			if err != nil {
				return nil, err
			}
			output.ChartVersion, output.Helm = chart.Version, &releaseview.Helm{DefaultDigest: chart.ArchiveDigest}
		} else if required.ImageIndex != nil {
			output.ImageIndex = &releaseview.ImageIndex{RequiredPlatforms: append([]string(nil), required.ImageIndex.RequiredPlatforms...)}
		} else {
			for _, declared := range plan.Definition.Outputs {
				if declared.Name == input.Name {
					output.OS, output.Arch, _ = strings.Cut(declared.Platform, "/")
				}
			}
		}
		description.Outputs = append(description.Outputs, output)
	}
	return releaseview.ArtifactInventory(description)
}

func executeDerivedInventory(ctx context.Context, plan *Plan, node Node, output Artifact, artifacts map[string]Artifact) error {
	if node.Kind != NodePackage || node.PackageKind != PackageBundle || output.Kind != PackageBundle {
		return e.New(e.CodePipelineDrift, "inventory derivation requires its bundle package operation")
	}
	if _, err := derivedRequirement(plan, output.Name, node.Derived); err != nil {
		return err
	}
	inventory, err := projectPackagedInventory(ctx, plan, node.Derived, artifacts)
	if err != nil {
		return err
	}
	archive, err := derivedoutput.InventoryArchive(ctx, inventory)
	if err != nil {
		return err
	}
	return writeDerivedInventoryArtifact(ctx, plan, output, archive)
}

func writeDerivedInventoryArtifact(ctx context.Context, plan *Plan, output Artifact, archive []byte) error {
	stage, err := os.MkdirTemp(filepath.Dir(output.Path), ".derived-inventory-*")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "staging derived inventory")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	path := filepath.Join(stage, "inventory.tar.gz")
	if err := writeFactoryFile(ctx, path, archive, 0o600); err != nil {
		return err
	}
	_, err = delivery.BuildBundleArtifactWithRepresentation(ctx, output.Path, output.Name, path, pipeline.FileDigest(archive), publicationOCIRepresentation(plan))
	return err
}

func observeDerivedInventory(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, artifact Artifact, artifacts map[string]Artifact) (LifecyclePayloadObservation, error) {
	required, err := derivedRequirement(plan, expected.Name, expected.Derived)
	if err != nil {
		return LifecyclePayloadObservation{}, err
	}
	if !reflect.DeepEqual(required, expected) {
		return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "derived observation changes compiled membership")
	}
	_, planned := planIndex(plan)
	compiled, found := planned[expected.ArtifactID]
	compiled.Path = artifact.Path // Runtime relocation does not change ownership.
	if !found || compiled != artifact {
		return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "derived observation substitutes its compiled artifact description")
	}
	descriptor, payload, err := delivery.ReadArtifact(ctx, artifact.Path)
	if err != nil {
		return LifecyclePayloadObservation{}, err
	}
	if descriptor.ArtifactType != delivery.MediaBundle {
		return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "derived inventory has a foreign wrapper")
	}
	inventory, err := projectPackagedInventory(ctx, plan, expected.Derived, artifacts)
	if err != nil {
		return LifecyclePayloadObservation{}, err
	}
	if err := derivedoutput.ValidateInventoryArchive(ctx, payload, inventory); err != nil {
		return LifecyclePayloadObservation{}, err
	}
	// Reconstruct only the OCI envelope around the observed compressed bytes.
	// Compressor changes cannot invalidate an otherwise exact inventory payload.
	stage, err := os.MkdirTemp("", "nvkr-observe-inventory-*")
	if err != nil {
		return LifecyclePayloadObservation{}, e.Wrap(e.CodeInternal, err, "staging inventory wrapper observation")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	reproduced := artifact
	reproduced.Path = filepath.Join(stage, "wrapper")
	if err := writeDerivedInventoryArtifact(ctx, plan, reproduced, payload); err != nil {
		return LifecyclePayloadObservation{}, err
	}
	wrapper, err := delivery.PayloadDescriptor(ctx, reproduced.Path)
	if err != nil {
		return LifecyclePayloadObservation{}, err
	}
	if wrapper.Digest != descriptor.Digest {
		return LifecyclePayloadObservation{}, e.New(e.CodeVerifyFailed, "derived inventory wrapper differs from its compiled output")
	}
	return LifecyclePayloadObservation{Digest: descriptor.Digest, SemanticDigest: pipeline.FileDigest(payload)}, nil
}
