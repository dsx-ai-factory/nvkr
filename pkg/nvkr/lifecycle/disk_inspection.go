// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// InstalledDiskInventories returns historical descriptions from this process's
// optional live inspection. Reloaded Run JSON has none. These are installed
// metadata, not full-disk SBOMs or restored receipt/qualification authority.
func (run *Run) InstalledDiskInventories() []factory.DiskInstalledInventoryDescription {
	if run == nil {
		return nil
	}
	views := slices.Clone(run.diskInventories)
	for index := range views {
		views[index].Subject.Components = slices.Clone(views[index].Subject.Components)
		views[index].Packages = slices.Clone(views[index].Packages)
	}
	return views
}

// This joins diagnostic views only; it does not reconstruct inspection authority.
func validateInstalledInventorySubjects(run *Run) error {
	byOutput := make(map[string]factory.DiskInstalledInventoryDescription, len(run.diskInventories))
	for _, inventory := range run.diskInventories {
		if _, duplicate := byOutput[inventory.OutputName]; duplicate {
			return e.New(e.CodeVerifyFailed, "duplicate installed inventory for output %q", inventory.OutputName)
		}
		byOutput[inventory.OutputName] = inventory
	}
	for _, output := range run.Observed {
		if output.Kind != OutputDiskImage {
			continue
		}
		inventory, found := byOutput[output.Name]
		if !found || output.Disk == nil || !canonical.IsDigest(output.Disk.PayloadDigest) || inventory.Subject.Digest != output.Disk.PayloadDigest ||
			inventory.Subject.Size != output.Disk.Size || inventory.Subject.PlanDigest != run.PlanDigest ||
			inventory.Subject.BuildDigest != output.BuildDigest || inventory.Subject.Format != output.Disk.Format ||
			inventory.Subject.Encryption != output.Disk.Encryption {
			return e.New(e.CodeVerifyFailed, "installed inventory does not bind observed disk output %q", output.Name)
		}
		delete(byOutput, output.Name)
	}
	if len(byOutput) != 0 {
		return e.New(e.CodeVerifyFailed, "installed inventory names an unobserved disk output")
	}
	return nil
}

func admitDiskInspection(ctx context.Context, plan *factory.Plan, runtime *sbom.DiskInspectionRuntime) error {
	if ctx == nil || plan == nil || runtime == nil {
		return e.New(e.CodeEngineCapability, "installed inventory requires a Plan and admitted runtime")
	}
	for _, output := range plan.LifecycleOutputs {
		if output.Kind == OutputDiskImage {
			_, _, _, err := runtime.BindingContext(ctx)
			return err
		}
	}
	return e.New(e.CodeEngineCapability, "installed inventory was requested without a disk output")
}

func inspectLocalDisks(ctx context.Context, plan *factory.Plan, result localExecutionResult,
	runtime *sbom.DiskInspectionRuntime,
) ([]factory.DiskInstalledInventoryDescription, error) {
	owner, ok := result.(*factory.LocalRunResult)
	if !ok || owner == nil || plan == nil || runtime == nil {
		return nil, e.New(e.CodeEngineCapability, "installed inventory requires the original live factory Run and admitted inspector")
	}
	return inspectLocalDiskOutputs(ctx, plan.LifecycleOutputs,
		func(ctx context.Context, output factory.LifecycleOutputPlan) (factory.DiskInstalledInventoryDescription, error) {
			receipt, err := owner.ObserveDiskInstalledInventory(ctx, plan, output, runtime)
			if err != nil {
				return factory.DiskInstalledInventoryDescription{}, err
			}
			if err := owner.ValidateDiskInstalledInventory(ctx, plan, output, runtime, receipt); err != nil {
				return factory.DiskInstalledInventoryDescription{}, err
			}
			return receipt.Description(), nil
		})
}

// The production wrapper binds this private loop to the original live owner and
// its Observe/Validate operations. The seam returns diagnostic descriptions only;
// it cannot issue or restore an inventory receipt.
func inspectLocalDiskOutputs(ctx context.Context, outputs []factory.LifecycleOutputPlan,
	inspect func(context.Context, factory.LifecycleOutputPlan) (factory.DiskInstalledInventoryDescription, error),
) ([]factory.DiskInstalledInventoryDescription, error) {
	var installed []factory.DiskInstalledInventoryDescription
	for _, output := range outputs {
		if output.Kind != OutputDiskImage {
			continue
		}
		inventory, err := inspect(ctx, output)
		if err != nil {
			return nil, err
		}
		installed = append(installed, inventory)
	}
	if len(installed) == 0 {
		return nil, e.New(e.CodeEngineCapability, "installed inventory was requested without a disk output")
	}
	return installed, nil
}
