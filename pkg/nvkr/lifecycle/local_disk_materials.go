// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

const localDiskMaterialsFile = "disk-materials.json"

// localDiskMaterials binds descriptive live-handoff exports to the attempt
// retaining them. Neither this envelope nor its decoded records recreate the
// factory's process-local material observations or authorize signing.
type localDiskMaterials struct {
	SchemaVersion string                       `json:"schemaVersion"`
	Attempt       string                       `json:"attempt"`
	PlanDigest    string                       `json:"planDigest"`
	Disks         []factory.DiskMaterialRecord `json:"disks"`
}

func retainLocalDiskMaterials(ctx context.Context, workspace string, prepared *PreparedRun, result localExecutionResult) error {
	hasDisk := false
	for _, output := range prepared.plan.LifecycleOutputs {
		hasDisk = hasDisk || output.Kind == OutputDiskImage
	}
	if !hasDisk {
		return nil
	}
	owner, ok := result.(*factory.LocalRunResult)
	if !ok || owner == nil {
		return e.New(e.CodeEngineCapability, "material retention requires the original live factory Run")
	}
	raw, err := collectLocalDiskMaterials(ctx, prepared.plan, prepared.run.Attempt, owner.ObserveDiskMaterialRecord)
	if err != nil {
		return err
	}
	_, err = writeLifecycleRecordState(ctx, filepath.Join(workspace, localDiskMaterialsFile), raw, func(directory *os.File) error {
		return directory.Sync()
	})
	return err
}

func collectLocalDiskMaterials(ctx context.Context, plan *factory.Plan, attempt string,
	observe func(context.Context, *factory.Plan, factory.LifecycleOutputPlan) (factory.DiskMaterialRecord, error),
) ([]byte, error) {
	if ctx == nil || plan == nil || observe == nil || attempt == "" {
		return nil, e.New(e.CodeUsage, "material retention requires an attempt, Plan and live observer")
	}
	identity, err := plan.IdentityDigest()
	if err != nil {
		return nil, err
	}
	record := localDiskMaterials{SchemaVersion: "nvkr.dev/local-run-materials/v1", Attempt: attempt, PlanDigest: identity,
		Disks: []factory.DiskMaterialRecord{}}
	for _, output := range plan.LifecycleOutputs {
		if output.Kind != OutputDiskImage {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "material retention canceled")
		}
		observed, err := observe(ctx, plan, output)
		if err != nil {
			return nil, err
		}
		if observed.Output != output.Name || observed.PlanDigest != identity || observed.BuildDigest != output.BuildDigest ||
			!equalStrings(observed.Components, output.Components) {
			return nil, e.New(e.CodeVerifyFailed, "live material record differs from its named Plan output")
		}
		record.Disks = append(record.Disks, observed)
	}
	raw, err := canonical.Marshal(record)
	if err != nil {
		return nil, err
	}
	if len(raw) > 4<<20 {
		return nil, e.New(e.CodeProvenanceMissing, "local material records exceed their aggregate byte bound")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "material retention canceled")
	}
	return raw, nil
}
