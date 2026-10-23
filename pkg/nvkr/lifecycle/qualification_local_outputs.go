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
	return checkLocalQualificationPayloadsWith(ctx, workspace, inputs, factory.ObserveRetainedDiskSubject,
		func(ctx context.Context, slot requiredOutput, want Artifact, artifacts map[string]factory.Artifact) error {
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
			return nil
		})
}

type localDiskObserver func(context.Context, *factory.Plan, factory.LifecycleOutputPlan, string, factory.DiskPayloadObservation) (factory.DiskPayloadObservation, error)

// Ordinary checks return no observations or authority. Standalone checking
// observes payloads; complete capture retains lifecycle's bindings here and
// delegates producer/package verification to the factory publication owner.
type localOrdinaryCheck func(context.Context, requiredOutput, Artifact, map[string]factory.Artifact) error

// Both paths share admission, original slot order and kind dispatch. The disk
// callback either observes paths or supplies an owned snapshot; the ordinary
// callback applies the caller's checks to that same original named slot.
func checkLocalQualificationPayloadsWith(ctx context.Context, workspace string, inputs *localQualificationInputs,
	observeDisk localDiskObserver, checkOrdinary localOrdinaryCheck,
) error {
	artifacts, err := localQualificationOutputArtifacts(ctx, workspace, inputs)
	if err != nil {
		return err
	}
	for index, slot := range inputs.run.Expected {
		want := inputs.run.Observed[index]
		if want.Name != slot.Name {
			return e.New(e.CodeVerifyFailed, "local output observations are not in canonical slot order")
		}
		if slot.Kind == OutputDiskImage {
			if err := checkLocalQualificationDisk(ctx, workspace, inputs.plan, slot, want, observeDisk); err != nil {
				return err
			}
			continue
		}
		if err := checkOrdinary(ctx, slot, want, artifacts); err != nil {
			return err
		}
	}
	return nil
}

// localQualificationOutputArtifacts admits the original complete Run and resolves
// its compiled artifact paths. These descriptions establish no payload authority.
func localQualificationOutputArtifacts(ctx context.Context, workspace string, inputs *localQualificationInputs) (map[string]factory.Artifact, error) {
	if ctx == nil || workspace == "" || inputs == nil || inputs.plan == nil {
		return nil, e.New(e.CodeUsage, "local payload checking requires replayed compiler inputs")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "local payload checking canceled")
	}
	if err := validateInspectedRun(&inputs.run); err != nil {
		return nil, err
	}
	if inputs.run.Status != RunComplete {
		return nil, e.New(e.CodeGateUnmet, "local payload checking requires complete Run metadata")
	}
	artifacts := make(map[string]factory.Artifact, len(inputs.plan.Graph.Artifacts))
	for _, artifact := range inputs.plan.Graph.Artifacts {
		if artifact.Path != "" {
			path, err := lifecycleArtifactPath(artifact.Path, workspace)
			if err != nil {
				return nil, err
			}
			artifact.Path = path
		}
		artifacts[artifact.ID] = artifact
	}
	return artifacts, nil
}

func checkLocalQualificationDisk(ctx context.Context, workspace string, plan *factory.Plan, slot requiredOutput,
	want Artifact, observeDisk localDiskObserver,
) error {
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
	got, err := observeDisk(ctx, plan,
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
	return nil
}
