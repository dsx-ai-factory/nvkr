// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

// observeHostedOutputs accepts disk transport projections only in the existing
// pipeline-attested route. No LocalRunResult or evidence receipt is reconstructed;
// full hosted outcome/provenance authentication still precedes Run completion.
func observeHostedOutputs(ctx context.Context, workspace string, slots []requiredOutput,
	artifacts map[string]factory.Artifact, plan *factory.Plan,
) ([]Artifact, error) {
	if ctx == nil || plan == nil {
		return nil, e.New(e.CodeUsage, "hosted observation requires a context and its original compiled Plan")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "hosted observation canceled")
	}
	for _, slot := range slots {
		if slot.Kind == OutputDiskImage && (plan.Qualification != factory.TierPipelineAttested || plan.Environment.Publication == nil) {
			return nil, e.New(e.CodeEngineCapability, "restored disk output requires the authenticated production publication route")
		}
	}
	localized := make(map[string]factory.Artifact, len(artifacts))
	for id, artifact := range artifacts {
		path, err := lifecycleArtifactPath(artifact.Path, workspace)
		if err != nil {
			return nil, err
		}
		artifact.Path = path
		localized[id] = artifact
	}
	observed := make([]Artifact, 0, len(slots))
	for _, slot := range slots {
		if slot.Kind != OutputDiskImage {
			artifact, err := observeOutput(ctx, slot, localized, plan, nil, workspace)
			if err != nil {
				return nil, err
			}
			observed = append(observed, artifact)
			continue
		}
		expected := factory.LifecycleOutputPlan{Name: slot.Name, Kind: slot.Kind, ArtifactID: slot.MemberArtifactID,
			BuildDigest: slot.BuildDigest, Components: append([]string(nil), slot.Components...)}
		artifact, err := factory.ObserveDiskPublicationPayload(ctx, plan, expected, localized)
		if err != nil {
			return nil, err
		}
		outer := localized[slot.ArtifactID]
		if outer.Kind != factory.PackageOCI || slot.ArtifactID == slot.MemberArtifactID {
			return nil, e.New(e.CodeReleaseIncomplete, "hosted disk output lacks its exact publication envelope")
		}
		inspection, err := delivery.Inspect(ctx, outer.Path)
		if err != nil {
			return nil, err
		}
		if err := delivery.RequireAttestedEvidence(inspection); err != nil {
			return nil, err
		}
		matches := 0
		for _, object := range inspection.Root.Objects {
			if object.Name != slot.Name || object.Kind != factory.PackageDiskImage {
				continue
			}
			matches++
			if object.Output == nil || object.BlobDigest != artifact.Disk.PayloadDigest {
				return nil, e.New(e.CodeVerifyFailed, "publication substitutes its exact observed disk member")
			}
			member := delivery.Output{Name: object.Name, Kind: object.Kind, Digest: object.DescriptorDigest,
				BuildDigest: object.Output.BuildDigest, Components: object.Output.Components, Disk: object.Output.Disk}
			if err := requireHostedOutputMembership(artifact, member); err != nil {
				return nil, err
			}
		}
		if matches != 1 {
			return nil, e.New(e.CodeReleaseIncomplete, "publication does not include its unique exact disk member")
		}
		observed = append(observed, artifact)
	}
	return observed, nil
}
