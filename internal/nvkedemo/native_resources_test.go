// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"go.yaml.in/yaml/v3"
)

func TestNativeResourceProfilesRequireCompleteBoundedDescriptions(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile NativeResources
		fail    bool
	}{
		{"defaults", NativeResources{}, false},
		{"explicit shared sizing", NativeResources{12, 8, 4096}, false},
		{"partial", NativeResources{TenantMemoryGiB: 8}, true},
		{"negative", NativeResources{12, -8, 4096}, true},
		{"missing headroom", NativeResources{12, 8, 0}, true},
		{"arithmetic bound", NativeResources{1 << 21, 8, 4096}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := selectedNativeResources(tc.profile)
			if (err != nil) != tc.fail {
				t.Fatalf("profile: %v", err)
			}
			if err != nil {
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeUsage {
					t.Fatalf("uncoded profile rejection: %v", err)
				}
			}
		})
	}
}

func TestNativeCapacityChecksAggregatePhasesAndIncrementalTenants(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		profile                       NativeResources
		conformance, tenantOnly, fail bool
		memory, cpu                   uint64
		workers                       int
	}{
		{"ordinary forty GiB budget", NativeResources{}, false, false, false, 40 * 1024, 14000, 0},
		{"four guests cannot fill a 48 GiB host", NativeResources{}, true, false, true, 48 * 1024, 14000, 0},
		{"conformance full budget", NativeResources{}, true, false, false, 52 * 1024, 14000, 0},
		{"explicit shared 8 GiB tenants", NativeResources{12, 8, 4096}, true, false, false, 40 * 1024, 14000, 0},
		{"headroom included", NativeResources{12, 8, 4096}, true, false, true, 36 * 1024, 14000, 0},
		{"site already consumes memory", NativeResources{12, 8, 4096}, true, true, false, 28 * 1024, 14000, 0},
		{"incremental budget fell", NativeResources{12, 8, 4096}, true, true, true, 27 * 1024, 14000, 0},
		{"time shared guest CPUs", NativeResources{}, true, false, false, 52 * 1024, 6000, 0},
		{"largest guest CPU unavailable", NativeResources{}, true, false, true, 52 * 1024, 5999, 0},
		{"memory unobserved", NativeResources{}, true, false, true, 0, 14000, 0},
		{"ordinary two workers full budget", NativeResources{12, 6, 4096}, false, false, false, 34 * 1024, 6000, 2},
		{"ordinary second worker needs memory", NativeResources{12, 6, 4096}, false, false, true, 33 * 1024, 6000, 2},
		{"ordinary two workers incremental budget", NativeResources{12, 6, 4096}, false, true, false, 22 * 1024, 6000, 2},
		{"ordinary incremental budget fell", NativeResources{12, 6, 4096}, false, true, true, 21 * 1024, 6000, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			admission := nativeResourceAdmission{resources: tc.profile, tenantWorkers: tc.workers, conformance: tc.conformance, observe: func(context.Context, string) (execution.ResourceCapacity, error) {
				return execution.ResourceCapacity{CPUMillis: tc.cpu, MemoryMiB: tc.memory}, nil
			}}
			err := admission.check(t.Context(), t.TempDir(), tc.tenantOnly, nil)
			if (err != nil) != tc.fail {
				t.Fatalf("capacity result: %v", err)
			}
			if err != nil {
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability {
					t.Fatalf("uncoded capacity rejection: %v", err)
				}
			}
		})
	}
}

func TestNativeCapacityCancellationDoesNotObserve(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	admission := nativeResourceAdmission{observe: func(context.Context, string) (execution.ResourceCapacity, error) {
		t.Fatal("canceled request reached host observation")
		return execution.ResourceCapacity{}, nil
	}}
	err := admission.check(ctx, t.TempDir(), false, nil)
	var coded *nvkrerr.Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &coded) || coded.Code != nvkrerr.CodeTimeout {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestNativeTenantRechecksCapacityBeforeCreateOnReusedSite(t *testing.T) {
	attempt, runner, _ := nativeSessionFixture(t, true, 0)
	observations := 0
	admission := &nativeResourceAdmission{resources: NativeResources{12, 8, 4096}, conformance: true, observe: func(context.Context, string) (execution.ResourceCapacity, error) {
		observations++
		memory := uint64(64 * 1024)
		if observations > 1 {
			memory = 20 * 1024
		}
		return execution.ResourceCapacity{CPUMillis: 14000, MemoryMiB: memory}, nil
	}}
	if err := admission.check(t.Context(), runner.Work, false, nil); err != nil {
		t.Fatal(err)
	}
	attempt.capacity = admission
	err := withNativeSiteScope(t.Context(), &attempt, runner, runner.Work, runner.Work, func(ctx context.Context, site *NativeSite) error {
		_, err := site.CreateTenant(ctx, "no-capacity")
		return err
	})
	var coded *nvkrerr.Error
	if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability || observations != 2 || attempt.sequence != 0 || attempt.tenantOwner != nil {
		t.Fatalf("tenant was created without current capacity: err=%v observations=%d sequence=%d", err, observations, attempt.sequence)
	}
}

func TestNativeResourceConfigurationReachesSeedWithoutChangingSource(t *testing.T) {
	root := t.TempDir()
	configuration := filepath.Join(root, "demo.yaml")
	if err := os.WriteFile(configuration, []byte("version: 1\nrun:\n  out: runtime\n  resources:\n    siteMemoryGiB: 12\n    tenantMemoryGiB: 8\n    headroomMiB: 4096\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := LoadDemoFile(t.Context(), configuration)
	if err != nil {
		t.Fatal(err)
	}
	configured, err := file.runDescription(nil)
	if err != nil {
		t.Fatal(err)
	}
	if configured.Resources != (NativeResources{12, 8, 4096}) {
		t.Fatalf("configured resources lost: %+v", configured.Resources)
	}
	seed := []byte("instance_types:\n  - {id: a1b2c3d4-0005-4000-8000-000000000004, name: cpu6-mem32gb-disk60gb, site_id: a1b2c3d4-0001-4000-8000-000000000001, gpu_count: 0, cpu_count: 6, memory_gib: 32, disk_gib: 60}\n  - {id: a1b2c3d4-0005-4000-8000-000000000003, name: cpu4-mem6gb-disk60gb, site_id: a1b2c3d4-0001-4000-8000-000000000001, gpu_count: 0, cpu_count: 4, memory_gib: 6, disk_gib: 60}\n")
	source := filepath.Join(root, "seed.yaml")
	if err := os.WriteFile(source, seed, 0600); err != nil {
		t.Fatal(err)
	}
	path, err := nativeSiteSeed(t.Context(), source, root, configured.Resources)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Instances []struct {
			Memory int `yaml:"memory_gib"`
		} `yaml:"instance_types"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Instances) != 2 || doc.Instances[0].Memory != 12 || doc.Instances[1].Memory != 8 {
		t.Fatalf("profile not projected: %s; %v", raw, err)
	}
	original, err := os.ReadFile(source)
	if err != nil || !bytes.Equal(original, seed) {
		t.Fatal("source seed changed")
	}
}
