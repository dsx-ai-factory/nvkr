// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// loadLocalQualificationMaterials retains exact canonical descriptions. The
// caller must independently capture payloads and match them to this same Run;
// neither this loader nor the saved records authenticate producer execution.
func loadLocalQualificationMaterials(ctx context.Context, workspace string, inputs *localQualificationInputs) (string, error) {
	if ctx == nil || inputs == nil || inputs.plan == nil || workspace == "" {
		return "", e.New(e.CodeUsage, "local materials require context, workspace and replayed inputs")
	}
	if err := ctx.Err(); err != nil {
		return "", e.Wrap(e.CodeTimeout, err, "local material loading canceled")
	}
	var slots []factory.LifecycleOutputPlan
	for _, slot := range inputs.plan.LifecycleOutputs {
		if slot.Kind == OutputDiskImage {
			slots = append(slots, slot)
		}
	}
	if len(slots) == 0 {
		return "", nil
	}
	root, err := openRunInspectionDirectory(ctx, workspace)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := requireLocalQualificationCommit(root); err != nil {
		return "", err
	}
	raw, err := readLocalQualificationMetadata(ctx, root, localDiskMaterialsFile, 4<<20)
	if err != nil {
		return "", err
	}
	if err := validateLocalQualificationMaterials(ctx, raw, inputs); err != nil {
		return "", err
	}
	if err := requireLocalQualificationCommit(root); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", e.Wrap(e.CodeTimeout, err, "local material loading canceled")
	}
	return string(raw), nil
}

// Reused immediately before signing; retained bytes must still describe the
// exact original disk set. Validation alone does not authenticate their origin.
func validateLocalQualificationMaterials(ctx context.Context, raw []byte, inputs *localQualificationInputs) error {
	if ctx == nil || inputs == nil || inputs.plan == nil {
		return e.New(e.CodeUsage, "material validation requires context and replayed inputs")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "material validation canceled")
	}
	if len(raw) == 0 || len(raw) > 4<<20 {
		return e.New(e.CodeVerifyFailed, "retained material records are missing or oversized")
	}
	var slots []factory.LifecycleOutputPlan
	for _, slot := range inputs.plan.LifecycleOutputs {
		if slot.Kind == OutputDiskImage {
			slots = append(slots, slot)
		}
	}
	var envelope localDiskMaterials
	if err := canonical.UnmarshalExact(raw, &envelope); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding exact local material records")
	}
	identity, err := inputs.plan.IdentityDigest()
	if err != nil {
		return err
	}
	if envelope.SchemaVersion != "nvkr.dev/local-run-materials/v1" || envelope.Attempt != inputs.run.Attempt ||
		envelope.PlanDigest != identity || inputs.run.PlanDigest != identity || len(envelope.Disks) != len(slots) {
		return e.New(e.CodeVerifyFailed, "local material envelope changes its attempt, Plan or complete disk set")
	}
	observed := make(map[string]Artifact)
	for _, artifact := range inputs.run.Observed {
		if _, duplicate := observed[artifact.Name]; duplicate {
			return e.New(e.CodeVerifyFailed, "local material Run repeats an output")
		}
		observed[artifact.Name] = artifact
	}
	for i, slot := range slots {
		artifact, exists := observed[slot.Name]
		if !exists || artifact.Kind != OutputDiskImage || artifact.Disk == nil || artifact.Digest != artifact.Disk.PayloadDigest {
			return e.New(e.CodeVerifyFailed, "local material record has no exact observed disk")
		}
		disk := artifact.Disk
		subject := factory.DiskPayloadObservation{Digest: disk.PayloadDigest, Size: disk.Size,
			BuildDigest: artifact.BuildDigest, Components: artifact.Components, Producer: disk.Producer,
			Component: disk.Component, Format: disk.Format, Encryption: disk.Encryption, PlanDigest: disk.PlanDigest}
		if err := envelope.Disks[i].ValidateDescription(ctx, inputs.plan, slot, subject); err != nil {
			return err
		}
	}
	return nil
}
