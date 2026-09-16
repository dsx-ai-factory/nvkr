package execution

import (
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestRequirementsValidation(t *testing.T) {
	tests := []struct {
		name         string
		requirements *Requirements
		wantCode     nkderr.Code
	}{
		{name: "absent"},
		{name: "missing constraint", requirements: &Requirements{}, wantCode: nkderr.CodeInvalidCatalog},
		{name: "empty resource request", requirements: &Requirements{Resources: &ResourceRequest{}}, wantCode: nkderr.CodeInvalidCatalog},
		{name: "CPU only", requirements: &Requirements{Resources: &ResourceRequest{CPUMillis: 1000}}},
		{name: "memory only", requirements: &Requirements{Resources: &ResourceRequest{MemoryMiB: 4096}}},
		{name: "ephemeral storage only", requirements: &Requirements{Resources: &ResourceRequest{EphemeralStorageMiB: 8192}}},
		{name: "empty host platforms", requirements: &Requirements{AllowedHostPlatforms: []HostPlatform{}}, wantCode: nkderr.CodeInvalidCatalog},
		{name: "host platform only", requirements: &Requirements{AllowedHostPlatforms: []HostPlatform{HostPlatformLinuxAMD64}}},
		{name: "unknown host platform", requirements: &Requirements{AllowedHostPlatforms: []HostPlatform{"linux/riscv64"}}, wantCode: nkderr.CodeInvalidCatalog},
		{name: "unsorted host platforms", requirements: &Requirements{AllowedHostPlatforms: []HostPlatform{HostPlatformLinuxARM64, HostPlatformLinuxAMD64}}, wantCode: nkderr.CodeInvalidCatalog},
		{name: "duplicate host platform", requirements: &Requirements{AllowedHostPlatforms: []HostPlatform{HostPlatformLinuxAMD64, HostPlatformLinuxAMD64}}, wantCode: nkderr.CodeInvalidCatalog},
		{name: "empty capabilities", requirements: &Requirements{Capabilities: []CapabilityRequirement{}}, wantCode: nkderr.CodeInvalidCatalog},
		{name: "capability only", requirements: &Requirements{Capabilities: []CapabilityRequirement{{Name: CapabilityKVM, Policy: CapabilityRequired}}}},
		{name: "forbidden capability", requirements: &Requirements{Capabilities: []CapabilityRequirement{{Name: CapabilityKVM, Policy: CapabilityForbidden}}}},
		{name: "unknown capability", requirements: &Requirements{Capabilities: []CapabilityRequirement{{Name: "gpu", Policy: CapabilityRequired}}}, wantCode: nkderr.CodeInvalidCatalog},
		{name: "unknown capability policy", requirements: &Requirements{Capabilities: []CapabilityRequirement{{Name: CapabilityKVM, Policy: "maybe"}}}, wantCode: nkderr.CodeInvalidCatalog},
		{name: "duplicate capability", requirements: &Requirements{Capabilities: []CapabilityRequirement{{Name: CapabilityKVM, Policy: CapabilityPreferred}, {Name: CapabilityKVM, Policy: CapabilityRequired}}}, wantCode: nkderr.CodeInvalidCatalog},
		{name: "timeout only", requirements: &Requirements{TimeoutSeconds: 7200}},
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
			coded, ok := err.(*nkderr.Error)
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
