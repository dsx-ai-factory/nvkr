// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"fmt"
	"io"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
)

// NativeResources describes downstream guest sizing and additional host/QEMU
// headroom. It changes neither selected artifact bytes nor their build identity.
// Zero selects the existing shared defaults; an override must be complete.
type NativeResources struct {
	SiteMemoryGiB   int `yaml:"siteMemoryGiB"`
	TenantMemoryGiB int `yaml:"tenantMemoryGiB"`
	HeadroomMiB     int `yaml:"headroomMiB"`
}

func selectedNativeResources(value NativeResources) (NativeResources, error) {
	if value == (NativeResources{}) {
		value = NativeResources{SiteMemoryGiB: 12, TenantMemoryGiB: tenantDemoMemoryGiB, HeadroomMiB: 4096}
	}
	// Bound descriptive arithmetic before converting to unsigned capacity units.
	if value.SiteMemoryGiB < 1 || value.SiteMemoryGiB > 1<<20 || value.TenantMemoryGiB < 1 || value.TenantMemoryGiB > 1<<20 || value.HeadroomMiB < 1 || value.HeadroomMiB > 1<<30 {
		return NativeResources{}, configUsage("native resources require positive bounded siteMemoryGiB, tenantMemoryGiB and headroomMiB")
	}
	return value, nil
}

type nativeResourceAdmission struct {
	resources     NativeResources
	tenantWorkers int
	conformance   bool
	observe       func(context.Context, string) (execution.ResourceCapacity, error)
}

func observeNativeResources(ctx context.Context, work string) (execution.ResourceCapacity, error) {
	if err := requireContext(ctx); err != nil {
		return execution.ResourceCapacity{}, err
	}
	directory, _, err := securepath.OpenDirectory(work)
	if err != nil {
		return execution.ResourceCapacity{}, err
	}
	defer directory.Close()
	facts, err := execution.ObserveLocalHost(ctx, directory)
	return facts.Resources, err
}

func (admission nativeResourceAdmission) check(ctx context.Context, work string, tenantOnly bool, output io.Writer) error {
	if err := requireContext(ctx); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "native guest capacity observation")
	}
	tenantNodes, err := selectedNativeTenantNodes(admission.tenantWorkers, admission.conformance)
	if err != nil {
		return err
	}
	resources, err := selectedNativeResources(admission.resources)
	if err != nil {
		return err
	}
	observe := admission.observe
	if observe == nil {
		observe = observeNativeResources
	}
	actual, err := observe(ctx, filepath.Clean(work))
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "observe native guest capacity")
	}
	tenantMiB := uint64(tenantNodes) * uint64(resources.TenantMemoryGiB) * 1024
	needed := tenantMiB
	if !tenantOnly {
		// The temporary 6-GiB bootstrap backend is joined before tenants start.
		// Admit the larger phase, never the sum of two non-overlapping phases.
		needed = uint64(resources.SiteMemoryGiB)*1024 + max(tenantMiB, 6*1024)
	}
	needed += uint64(resources.HeadroomMiB)
	// vCPUs are time-shared: admit the largest VM, not the sum of all vCPUs.
	if actual.CPUMillis < tenantDemoVCPUs*1000 || actual.MemoryMiB < needed {
		return nvkrerr.New(nvkrerr.CodeEngineCapability, "native guests require %d available MiB including host/QEMU headroom and %d host CPUs; observed %d MiB and %d CPU milliunits; observation reserves nothing", needed, tenantDemoVCPUs, actual.MemoryMiB, actual.CPUMillis)
	}
	if output != nil {
		_, err = fmt.Fprintf(output, "Native guest capacity: available memory %d/%d MiB; largest VM %d CPUs. Not reserved.\n", actual.MemoryMiB, needed, tenantDemoVCPUs)
	}
	return err
}
