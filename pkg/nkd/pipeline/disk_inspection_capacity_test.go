package pipeline

import (
	"context"
	"os"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

func TestDiskContentStorageAdmissionFollowsActualOwner(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, before := range []bool{false, true} {
			for _, storage := range []string{"private-volume", "memory"} {
				t.Run(platform+"/"+storage+map[bool]string{false: "/scan", true: "/snapshot"}[before], func(t *testing.T) {
					selection := testDiskInspectionSelection(t)
					selection.Platform, selection.ScratchStorage = platform, storage
					selection.MaxFilesystemBytes = 16 << 30
					selection.Resources.MemoryMiB = 2*1024 + 2048 + 12*16384 + 2048 + diskContentReportMemoryMiB
					selection.Resources.EphemeralStorageMiB = 12*16384 + 2*1024 + 2048
					workspace := selection.Resources.EphemeralStorageMiB
					if storage == "private-volume" {
						selection.Resources.MemoryMiB = 2*1024 + 2048 + 6144 + 2*1024 + diskContentReportMemoryMiB
						// Production Linux held snapshots use memfd; helper scratch
						// is owned by the separately admitted daemon volume.
						workspace = 0
					}
					facts := execution.ObservedHostFacts{Platform: execution.HostPlatform(platform), Resources: execution.ResourceCapacity{
						CPUMillis: selection.Resources.CPUMillis, MemoryMiB: selection.Resources.MemoryMiB, EphemeralStorageMiB: workspace}}
					if before {
						facts.Resources.MemoryMiB += 1024
					}
					if err := validateDiskInspectionResources(selection, facts, 1<<30, before); err != nil {
						t.Fatalf("actual storage route rejected: %v", err)
					}
					if storage == "memory" {
						facts.Resources.EphemeralStorageMiB--
						assertDiskCapacityError(t, validateDiskInspectionResources(selection, facts, 1<<30, before), "workspace", "required=200704MiB", "observed=200703MiB")
					} else {
						selection.Resources.EphemeralStorageMiB--
						assertDiskCapacityError(t, validateDiskInspectionResources(selection, facts, 1<<30, before), "scratch", "required=200704MiB", "declared=200703MiB")
					}
				})
			}
		}
	}
}

func TestDiskInspectionCapacityDiagnosticsAndAdmissionBeforeSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, dimension, required, observed string
		edit                                func(*DiskInspectionSelection, *execution.ObservedHostFacts)
	}{
		{"declared RAM", "memory", "required=4096MiB", "declared=4095MiB", func(s *DiskInspectionSelection, _ *execution.ObservedHostFacts) { s.Resources.MemoryMiB-- }},
		{"host RAM", "memory", "required=5120MiB", "observed=5119MiB", func(_ *DiskInspectionSelection, f *execution.ObservedHostFacts) { f.Resources.MemoryMiB-- }},
		{"host CPU", "CPU", "required=1000m", "observed=999m", func(_ *DiskInspectionSelection, f *execution.ObservedHostFacts) { f.Resources.CPUMillis-- }},
		{"workspace", "workspace", "required=1024MiB", "observed=1023MiB", func(_ *DiskInspectionSelection, f *execution.ObservedHostFacts) { f.Resources.EphemeralStorageMiB-- }},
		{"platform", "platform", "required=linux/amd64", "observed=linux/arm64", func(_ *DiskInspectionSelection, f *execution.ObservedHostFacts) {
			f.Platform = execution.HostPlatformLinuxARM64
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selection := testDiskInspectionSelection(t)
			selection.Resources.MemoryMiB = 4096
			facts := execution.ObservedHostFacts{Platform: execution.HostPlatformLinuxAMD64, Resources: execution.ResourceCapacity{CPUMillis: 1000, MemoryMiB: 5120, EphemeralStorageMiB: 1024}}
			tc.edit(&selection, &facts)
			observed := 0
			container := &DiskInspectionContainer{selection: selection, runtime: &heldRuntimeExecutable{}, observeHost: func(context.Context, *os.File) (execution.ObservedHostFacts, error) {
				observed++
				return facts, nil
			}}
			// No held artifact file exists: reaching snapshot creation would
			// return VerifyFailed instead of the admission error asserted here.
			result, err := container.InspectPackerArtifact(t.Context(), &PackerArtifact{size: 1 << 30})
			assertDiskCapacityError(t, err, tc.dimension, tc.required, tc.observed)
			if result != nil || observed != 1 {
				t.Fatalf("failed admission escaped: result=%v observations=%d", result, observed)
			}
			if !diskInspectionCapacity.TryLock() {
				t.Fatal("failed admission retained the capacity permit")
			}
			diskInspectionCapacity.Unlock()
		})
	}
}

func TestDiskInspectionScratchCapacityDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, raw, required, observed string }{
		{"short", "1023 1048576", "required=1024MiB", "observed=1023MiB"},
		{"malformed", "unknown", "required=1024MiB", "unknown"},
		{"overflow", "18446744073709551615 4096", "required=1024MiB", "18446744073709551615"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertDiskCapacityError(t, validateDiskInspectionScratchCapacity([]byte(tc.raw), 1024), "scratch", tc.required, tc.observed)
		})
	}
}

func assertDiskCapacityError(t *testing.T, err error, values ...string) {
	t.Helper()
	if code(err) != e.CodeEngineCapability {
		t.Fatalf("error=%v want EngineCapability", err)
	}
	for _, value := range values {
		if !strings.Contains(err.Error(), value) {
			t.Fatalf("error %q omits %q", err, value)
		}
	}
}
