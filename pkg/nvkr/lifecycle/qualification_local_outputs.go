// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// checkLocalQualificationPayloads compares current payloads and original
// producer records against every named output. It issues no receipt: publication
// must capture the checked closure and the local signer must authenticate that
// held closure, not reuse these reopenable paths as authority.
func checkLocalQualificationPayloads(ctx context.Context, workspace string, inputs *localQualificationInputs) error {
	return checkLocalQualificationPayloadsWith(ctx, workspace, inputs, factory.ObserveRetainedDiskSubject)
}

type localDiskObserver func(context.Context, *factory.Plan, factory.LifecycleOutputPlan, string, factory.DiskPayloadObservation) (factory.DiskPayloadObservation, error)

// The disk callback either observes paths or supplies an owned snapshot. Exact
// Run/output matching remains here, shared by both callers.
func checkLocalQualificationPayloadsWith(ctx context.Context, workspace string, inputs *localQualificationInputs, observeDisk localDiskObserver) error {
	if ctx == nil || workspace == "" || inputs == nil || inputs.plan == nil {
		return e.New(e.CodeUsage, "local payload checking requires replayed compiler inputs")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "local payload checking canceled")
	}
	if err := validateInspectedRun(&inputs.run); err != nil {
		return err
	}
	if inputs.run.Status != RunComplete {
		return e.New(e.CodeGateUnmet, "local payload checking requires complete Run metadata")
	}
	artifacts := make(map[string]factory.Artifact, len(inputs.plan.Graph.Artifacts))
	for _, artifact := range inputs.plan.Graph.Artifacts {
		if artifact.Path != "" {
			path, err := lifecycleArtifactPath(artifact.Path, workspace)
			if err != nil {
				return err
			}
			artifact.Path = path
		}
		artifacts[artifact.ID] = artifact
	}
	for index, slot := range inputs.run.Expected {
		want := inputs.run.Observed[index]
		if want.Name != slot.Name {
			return e.New(e.CodeVerifyFailed, "local output observations are not in canonical slot order")
		}
		if slot.Kind == OutputDiskImage {
			path, err := LocalDiskOutputPath(workspace, slot.Name)
			if err != nil {
				return err
			}
			if want.Disk == nil {
				return e.New(e.CodeReleaseIncomplete, "local disk has no recorded observation")
			}
			disk := want.Disk
			subject := factory.DiskPayloadObservation{Digest: disk.PayloadDigest, Size: disk.Size,
				BuildDigest: want.BuildDigest, Components: want.Components, Producer: disk.Producer,
				Component: disk.Component, Format: disk.Format, Encryption: disk.Encryption, PlanDigest: disk.PlanDigest}
			got, err := observeDisk(ctx, inputs.plan,
				factory.LifecycleOutputPlan{Name: slot.Name, Kind: slot.Kind, ArtifactID: slot.MemberArtifactID,
					BuildDigest: slot.BuildDigest, Components: slot.Components}, path, subject)
			if err != nil {
				return err
			}
			if want.Disk == nil || got.Digest != want.Digest || got.Digest != want.Disk.PayloadDigest ||
				got.Size != want.Disk.Size || got.BuildDigest != want.BuildDigest || got.PlanDigest != want.Disk.PlanDigest ||
				got.Producer != want.Disk.Producer || got.Component != want.Disk.Component ||
				got.Format != want.Disk.Format || got.Encryption != want.Disk.Encryption || !equalStrings(got.Components, want.Components) {
				return e.New(e.CodeVerifyFailed, "retained local disk %q differs from its complete Run", slot.Name)
			}
			continue
		}
		got, err := observeOutput(ctx, slot, artifacts, inputs.plan, nil, workspace)
		if err != nil {
			return err
		}
		wantBytes, err := canonical.Marshal(want)
		if err != nil {
			return err
		}
		gotBytes, err := canonical.Marshal(got)
		if err != nil {
			return err
		}
		if !bytes.Equal(wantBytes, gotBytes) {
			return e.New(e.CodeVerifyFailed, "local output %q differs from its complete Run", slot.Name)
		}
	}
	return nil
}
