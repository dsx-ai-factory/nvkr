package lifecycle

import (
	"context"
	"slices"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
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
	var installed []factory.DiskInstalledInventoryDescription
	for _, output := range plan.LifecycleOutputs {
		if output.Kind != OutputDiskImage {
			continue
		}
		receipt, err := owner.ObserveDiskInstalledInventory(ctx, plan, output, runtime)
		if err != nil {
			return nil, err
		}
		if err := owner.ValidateDiskInstalledInventory(ctx, plan, output, runtime, receipt); err != nil {
			return nil, err
		}
		installed = append(installed, receipt.Description())
	}
	if len(installed) == 0 {
		return nil, e.New(e.CodeEngineCapability, "installed inventory was requested without a disk output")
	}
	return installed, nil
}
