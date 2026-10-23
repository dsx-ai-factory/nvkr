// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
)

// observeOutputs resolves ordinary payload paths once and independently observes
// each named output. Disk outputs consume the local execution's held capability.
func observeOutputs(ctx context.Context, workspace string, slots []requiredOutput, artifacts map[string]factory.Artifact,
	plan *factory.Plan, execution *factory.LocalRunResult,
) ([]Artifact, error) {
	if execution == nil {
		for _, slot := range slots {
			if slot.Kind == OutputDiskImage {
				return nil, e.New(e.CodeEngineCapability,
					"disk output %q requires an in-process held producer result", slot.Name)
			}
		}
	}
	return observeOutputsWithDiskStaging(ctx, workspace, slots, artifacts, plan, execution, nil)
}

type heldDiskStager func(context.Context, requiredOutput, *factory.Plan, diskPayloadExporter) (Artifact, error)

type diskPayloadObserver interface {
	diskPayloadExporter
	ObserveDiskLifecyclePayload(context.Context, *factory.Plan, factory.LifecycleOutputPlan) (factory.DiskPayloadObservation, error)
}

func observeOutputsWithDiskStaging(ctx context.Context, workspace string, slots []requiredOutput,
	artifacts map[string]factory.Artifact, plan *factory.Plan, execution diskPayloadObserver,
	stage heldDiskStager, monitors ...*progress.Monitor,
) ([]Artifact, error) {
	var monitor *progress.Monitor
	if len(monitors) != 0 {
		monitor = monitors[0]
	}
	var localized map[string]factory.Artifact
	observed := make([]Artifact, 0, len(slots))
	for _, slot := range slots {
		monitor.SetOutput(slot.Name, "observing", "observation")
		// Held disk observation must not depend on reopenable path descriptions.
		if slot.Kind != OutputDiskImage && localized == nil {
			if plan == nil {
				return nil, e.New(e.CodeInternal, "typed lifecycle package observation requires its owning plan")
			}
			localized = make(map[string]factory.Artifact, len(artifacts))
			for id, artifact := range artifacts {
				if artifact.Path != "" {
					resolved, err := lifecycleArtifactPath(artifact.Path, workspace)
					if err != nil {
						return nil, err
					}
					artifact.Path = resolved
				}
				localized[id] = artifact
			}
		}
		var artifact Artifact
		var err error
		if slot.Kind == OutputDiskImage && stage != nil {
			artifact, err = stage(ctx, slot, plan, execution)
		} else {
			artifact, err = observeOutput(ctx, slot, localized, plan, execution, workspace)
		}
		if err != nil {
			monitor.SetOutput(slot.Name, "failed", "observation")
			return nil, err
		}
		monitor.SetOutput(slot.Name, "observed", "observation")
		observed = append(observed, artifact)
	}
	return observed, nil
}

func observeOutput(ctx context.Context, slot requiredOutput, artifacts map[string]factory.Artifact,
	plan *factory.Plan, execution diskPayloadObserver, workspace string,
) (Artifact, error) {
	if slot.Kind == OutputDiskImage {
		if execution == nil {
			return Artifact{}, e.New(e.CodeEngineCapability,
				"disk output %q requires an in-process held producer result", slot.Name)
		}
		return observeHeldDiskOutput(ctx, slot, plan, execution.ObserveDiskLifecyclePayload)
	}
	member, err := ordinaryOutputMember(slot, artifacts)
	if err != nil {
		return Artifact{}, err
	}

	expected := factory.LifecycleOutputPlan{Name: slot.Name, Kind: slot.Kind, ArtifactID: slot.MemberArtifactID,
		BuildDigest: slot.BuildDigest, Components: append([]string(nil), slot.Components...), ImageIndex: slot.ImageIndex.Clone()}
	observation, err := factory.ObserveWorkspaceLifecyclePayload(ctx, plan, expected, workspace, artifacts)
	if err != nil {
		return Artifact{}, err
	}
	components, digest, semantic := observation.Components, observation.Digest, observation.SemanticDigest
	if !equalStrings(components, slot.Components) {
		return Artifact{}, e.New(e.CodeVerifyFailed, "output %q bundle members differ from its frozen component mapping", slot.Name)
	}
	if err := checkOutputPublicationMember(ctx, slot, member, artifacts, digest, semantic); err != nil {
		return Artifact{}, err
	}
	return Artifact{Name: slot.Name, Kind: slot.Kind, Digest: digest, BuildDigest: slot.BuildDigest,
		Components: components, ImageIndex: observation.ImageIndex.Clone()}, nil
}

// ordinaryOutputMember checks lifecycle's frozen member and producer-result
// mappings without opening payload bytes. Factory owns producer verification.
func ordinaryOutputMember(slot requiredOutput, artifacts map[string]factory.Artifact) (factory.Artifact, error) {
	member, ok := artifacts[slot.MemberArtifactID]
	if !ok {
		return factory.Artifact{}, e.New(e.CodeReleaseIncomplete, "run plan has no typed member for frozen output %q", slot.Name)
	}
	// Lifecycle owns the frozen result slots; factory owns re-observing their
	// exact instruction, record and payload bytes.

	bindings, err := requiredProducerBindings(slot)
	if err != nil {
		return factory.Artifact{}, err
	}
	if len(slot.ComponentResults) != len(bindings) {
		return factory.Artifact{}, e.New(e.CodeReleaseIncomplete, "output %q has incomplete frozen producer mappings", slot.Name)
	}
	for index, id := range slot.ComponentResults {
		binding := bindings[index]
		result, exists := artifacts[id]
		if !exists {
			return factory.Artifact{}, e.New(e.CodeReleaseIncomplete, "output %q has no frozen producer result %q", slot.Name, id)
		}
		if id != binding.id || result.Kind != "producer-result" || result.BuildTarget != binding.target || result.Component != binding.component {
			return factory.Artifact{}, e.New(e.CodeVerifyFailed, "output %q substitutes its frozen producer mapping %q", slot.Name, id)
		}
	}
	return member, nil
}

// checkOutputPublicationMember retains lifecycle's outer-publication join.
// The caller must independently verify the member bytes against these subjects.
func checkOutputPublicationMember(ctx context.Context, slot requiredOutput, member factory.Artifact,
	artifacts map[string]factory.Artifact, digest, semantic string,
) error {
	if slot.ArtifactID != slot.MemberArtifactID {
		outer, found := artifacts[slot.ArtifactID]
		if !found || outer.Kind != factory.PackageOCI {
			return e.New(e.CodeReleaseIncomplete, "run plan has no publication root for frozen output %q", slot.Name)
		}
		inspection, inspectErr := delivery.Inspect(ctx, outer.Path)
		if inspectErr != nil {
			return inspectErr
		}
		matches := 0
		for _, object := range inspection.Root.Objects {
			if object.Name == member.Name && object.Kind == member.Kind {
				matches++
				exact := object.DescriptorDigest == digest
				if member.Kind == factory.PackageAPT {
					exact = object.SemanticDigest == semantic
				}
				if object.ArtifactType != member.MediaType || !exact {
					return e.New(e.CodeVerifyFailed, "output %q publication root substitutes its planned typed member", slot.Name)
				}
			}
		}
		if matches != 1 {
			return e.New(e.CodeReleaseIncomplete, "output %q publication root does not contain its exact planned binary member", slot.Name)
		}
	}
	return nil
}

type heldDiskObserver func(context.Context, *factory.Plan, factory.LifecycleOutputPlan) (factory.DiskPayloadObservation, error)

// observeHeldDiskOutput is reached in production only after observeOutput has
// required factory's concrete in-process LocalRunResult. Keeping the callback
// private makes the recording projection testable without exposing a
// caller-mintable lifecycle authority boundary.
func observeHeldDiskOutput(ctx context.Context, slot requiredOutput, plan *factory.Plan,
	observe heldDiskObserver,
) (Artifact, error) {
	expected := factory.LifecycleOutputPlan{Name: slot.Name, Kind: slot.Kind,
		ArtifactID: slot.MemberArtifactID, BuildDigest: slot.BuildDigest,
		Components: append([]string(nil), slot.Components...)}
	observation, err := observe(ctx, plan, expected)
	if err != nil {
		return Artifact{}, err
	}
	return artifactFromDiskObservation(slot, observation), nil
}

func artifactFromDiskObservation(slot requiredOutput, observation factory.DiskPayloadObservation) Artifact {
	return Artifact{Name: slot.Name, Kind: slot.Kind, Digest: observation.Digest,
		BuildDigest: observation.BuildDigest, Components: append([]string(nil), observation.Components...),
		Disk: &delivery.DiskOutput{PayloadDigest: observation.Digest, Producer: observation.Producer, Component: observation.Component,
			Format: observation.Format, Encryption: observation.Encryption, Size: observation.Size,
			PlanDigest: observation.PlanDigest, Outcome: observation.Outcome}}
}

func lifecycleArtifactPath(path, workspace string) (string, error) {
	prefix := filepath.ToSlash(filepath.Join(".nvkr", "factory"))
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == prefix || len(clean) <= len(prefix)+1 || clean[:len(prefix)+1] != prefix+"/" {
		return "", e.New(e.CodePipelineDrift, "factory artifact path %q is outside %s", path, prefix)
	}
	return filepath.Join(workspace, filepath.FromSlash(clean[len(prefix)+1:])), nil
}
