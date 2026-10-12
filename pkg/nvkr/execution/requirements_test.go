// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package execution

import (
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestRequirementsValidation(t *testing.T) {
	tests := []struct {
		name         string
		requirements *Requirements
		wantCode     nvkrerr.Code
	}{
		{name: "absent"},
		{name: "missing constraint", requirements: &Requirements{}, wantCode: nvkrerr.CodeInvalidCatalog},
		{name: "empty resource request", requirements: &Requirements{Resources: &ResourceRequest{}}, wantCode: nvkrerr.CodeInvalidCatalog},
		{name: "CPU only", requirements: &Requirements{Resources: &ResourceRequest{CPUMillis: 1000}}},
		{name: "memory only", requirements: &Requirements{Resources: &ResourceRequest{MemoryMiB: 4096}}},
		{name: "ephemeral storage only", requirements: &Requirements{Resources: &ResourceRequest{EphemeralStorageMiB: 8192}}},
		{name: "empty host platforms", requirements: &Requirements{AllowedHostPlatforms: []HostPlatform{}}, wantCode: nvkrerr.CodeInvalidCatalog},
		{name: "host platform only", requirements: &Requirements{AllowedHostPlatforms: []HostPlatform{HostPlatformLinuxAMD64}}},
		{name: "unknown host platform", requirements: &Requirements{AllowedHostPlatforms: []HostPlatform{"linux/riscv64"}}, wantCode: nvkrerr.CodeInvalidCatalog},
		{name: "unsorted host platforms", requirements: &Requirements{AllowedHostPlatforms: []HostPlatform{HostPlatformLinuxARM64, HostPlatformLinuxAMD64}}, wantCode: nvkrerr.CodeInvalidCatalog},
		{name: "duplicate host platform", requirements: &Requirements{AllowedHostPlatforms: []HostPlatform{HostPlatformLinuxAMD64, HostPlatformLinuxAMD64}}, wantCode: nvkrerr.CodeInvalidCatalog},
		{name: "empty capabilities", requirements: &Requirements{Capabilities: []CapabilityRequirement{}}, wantCode: nvkrerr.CodeInvalidCatalog},
		{name: "capability only", requirements: &Requirements{Capabilities: []CapabilityRequirement{{Name: CapabilityKVM, Policy: CapabilityRequired}}}},
		{name: "forbidden capability", requirements: &Requirements{Capabilities: []CapabilityRequirement{{Name: CapabilityKVM, Policy: CapabilityForbidden}}}},
		{name: "unknown capability", requirements: &Requirements{Capabilities: []CapabilityRequirement{{Name: "gpu", Policy: CapabilityRequired}}}, wantCode: nvkrerr.CodeInvalidCatalog},
		{name: "unknown capability policy", requirements: &Requirements{Capabilities: []CapabilityRequirement{{Name: CapabilityKVM, Policy: "maybe"}}}, wantCode: nvkrerr.CodeInvalidCatalog},
		{name: "duplicate capability", requirements: &Requirements{Capabilities: []CapabilityRequirement{{Name: CapabilityKVM, Policy: CapabilityPreferred}, {Name: CapabilityKVM, Policy: CapabilityRequired}}}, wantCode: nvkrerr.CodeInvalidCatalog},
		{name: "timeout only", requirements: &Requirements{TimeoutSeconds: 7200}},
		{name: "timeout overflow", requirements: &Requirements{TimeoutSeconds: maxTimeoutSeconds + 1}, wantCode: nvkrerr.CodeInvalidCatalog},
		{name: "complete", requirements: &Requirements{Resources: &ResourceRequest{
			CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 32768,
		}, AllowedHostPlatforms: []HostPlatform{HostPlatformLinuxAMD64, HostPlatformLinuxARM64},
			Capabilities: []CapabilityRequirement{{Name: CapabilityKVM, Policy: CapabilityPreferred}}, TimeoutSeconds: 7200}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.requirements.Validate()
			if test.wantCode == "" {
				if err != nil {
					t.Fatalf("valid requirements rejected: %v", err)
				}
				return
			}
			coded, ok := err.(*nvkrerr.Error)
			if !ok || coded.Code != test.wantCode {
				t.Fatalf("error = %v, want %s", err, test.wantCode)
			}
		})
	}
}

func TestCloneIsIndependentAndEqual(t *testing.T) {
	original := &Requirements{
		Resources:            &ResourceRequest{CPUMillis: 2000, MemoryMiB: 4096},
		AllowedHostPlatforms: []HostPlatform{HostPlatformLinuxAMD64, HostPlatformLinuxARM64},
		Capabilities:         []CapabilityRequirement{{Name: CapabilityKVM, Policy: CapabilityPreferred}},
		TimeoutSeconds:       7200,
	}
	cloned := Clone(original)
	if !Equal(original, cloned) {
		t.Fatal("cloned requirements differ from their source")
	}
	cloned.Resources.MemoryMiB++
	cloned.AllowedHostPlatforms[0] = HostPlatformLinuxARM64
	cloned.Capabilities[0].Policy = CapabilityRequired
	if original.Resources.MemoryMiB != 4096 ||
		original.AllowedHostPlatforms[0] != HostPlatformLinuxAMD64 ||
		original.Capabilities[0].Policy != CapabilityPreferred || Equal(original, cloned) {
		t.Fatal("cloned requirements alias their source")
	}
}

func TestEqualDistinguishesEveryRequirementField(t *testing.T) {
	original := &Requirements{
		Resources: &ResourceRequest{
			CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192,
		},
		AllowedHostPlatforms: []HostPlatform{HostPlatformLinuxAMD64, HostPlatformLinuxARM64},
		Capabilities:         []CapabilityRequirement{{Name: CapabilityKVM, Policy: CapabilityPreferred}},
		TimeoutSeconds:       7200,
	}

	if !Equal(nil, nil) {
		t.Fatal("two absent requirements must compare equal")
	}
	if Equal(nil, original) || Equal(original, nil) {
		t.Fatal("absent and present requirements must compare unequal")
	}
	if !Equal(original, Clone(original)) {
		t.Fatal("identical requirements must compare equal")
	}

	tests := []struct {
		name   string
		mutate func(*Requirements)
	}{
		{name: "resources presence", mutate: func(requirements *Requirements) {
			requirements.Resources = nil
		}},
		{name: "CPU", mutate: func(requirements *Requirements) {
			requirements.Resources.CPUMillis++
		}},
		{name: "memory", mutate: func(requirements *Requirements) {
			requirements.Resources.MemoryMiB++
		}},
		{name: "ephemeral storage", mutate: func(requirements *Requirements) {
			requirements.Resources.EphemeralStorageMiB++
		}},
		{name: "allowed host platforms", mutate: func(requirements *Requirements) {
			requirements.AllowedHostPlatforms = requirements.AllowedHostPlatforms[:1]
		}},
		{name: "capability name", mutate: func(requirements *Requirements) {
			requirements.Capabilities[0].Name = "future-capability"
		}},
		{name: "capability policy", mutate: func(requirements *Requirements) {
			requirements.Capabilities[0].Policy = CapabilityRequired
		}},
		{name: "timeout", mutate: func(requirements *Requirements) {
			requirements.TimeoutSeconds++
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := Clone(original)
			test.mutate(changed)
			if Equal(original, changed) || Equal(changed, original) {
				t.Fatalf("requirements differing only in %s compared equal", test.name)
			}
		})
	}
}
