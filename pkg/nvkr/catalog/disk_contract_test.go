// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"slices"
	"strings"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
)

func TestDiskImageCatalogSchemaAndIdentity(t *testing.T) {
	const vmToolchain = "example.invalid/nvkr-vm-builder@sha256:59b8183301af6dc358c9258d7b2ab0ee1a9363618552334fb3b160d454cbda72"
	registry := `apiVersion: nvkr.dev/catalog/v3
kind: SourceBuildRegistry
sources:
  image-source:
    repo: https://github.com/example/node-image
    toolchainImage: ` + vmToolchain + `
    toolchainImagesByArch:
      amd64: ` + vmToolchain + `
      arm64: ` + vmToolchain + `
components:
  node-fde:
    platforms: [linux/amd64, linux/arm64]
    fromSource: true
    source: image-source
    builderType: vm-image
    outputType: disk-image
    disk:
      format: qcow2
      encryption: luks2
    buildRequirements:
      # Schema-fixture values only, not an NVKE production runner profile.
      resources:
        cpuMillis: 4000
        memoryMiB: 8192
        ephemeralStorageMiB: 32768
      allowedHostPlatforms:
        - linux/amd64
        - linux/arm64
      capabilities:
        - name: kvm
          policy: preferred
      timeoutSeconds: 10800
    buildCommand:
      - make
      - "-C"
      - image
      - image-fde
      - "ARCH={{.Arch}}"
    artifactPath: "image/output-fde/node-{{.Arch}}.qcow2"
  node-plain:
    platforms: [linux/amd64, linux/arm64]
    fromSource: true
    source: image-source
    builderType: vm-image
    outputType: disk-image
    disk:
      format: qcow2
      encryption: none
    buildRequirements:
      # Schema-fixture values only, not an NVKE production runner profile.
      resources:
        cpuMillis: 4000
        memoryMiB: 8192
        ephemeralStorageMiB: 32768
      allowedHostPlatforms:
        - linux/amd64
        - linux/arm64
      capabilities:
        - name: kvm
          policy: preferred
      timeoutSeconds: 18000
    buildCommand:
      - make
      - "-C"
      - image
      - image-plain
      - "ARCH={{.Arch}}"
    artifactPath: "image/output-plain/node-{{.Arch}}.qcow2"
`
	lock := `apiVersion: nvkr.dev/lock/v1
kind: PinLock
pins:
  image-source:
    tag: v1.0.0
    commit: ` + goodCommit + `
    treeHash: ` + goodTree + `
    sourceDateEpoch: 1733939955
`
	load := func(t *testing.T) *Catalog {
		t.Helper()
		cat, err := LoadContext(context.Background(), bytesProvider{
			RegistryFile: []byte(registry), LockFile: []byte(lock),
		})
		if err != nil {
			t.Fatal(err)
		}
		return cat
	}

	cat := load(t)
	plain := cat.Components["node-plain"]
	wantCommand := []string{"make", "-C", "image", "image-plain", "ARCH={{.Arch}}"}
	if plain.BuilderType != BuilderVMImage || plain.Disk == nil || plain.Disk.Format != "qcow2" ||
		plain.Disk.Encryption != "none" || !slices.Equal(plain.BuildCommand, wantCommand) ||
		plain.ArtifactPath != "image/output-plain/node-{{.Arch}}.qcow2" || plain.BuildRequirements == nil {
		t.Fatalf("loaded disk component lost its authored contract: %#v", plain)
	}

	before, err := cat.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	changed := load(t)
	component := changed.Components["node-plain"]
	disk := *component.Disk
	disk.Encryption = "luks2"
	component.Disk = &disk
	changed.Components["node-plain"] = component
	after, err := changed.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("disk encryption metadata did not affect catalog identity")
	}
}

func TestDiskImageSchemaValidation(t *testing.T) {
	valid := goodComponent()
	valid.OutputType = "disk-image"
	valid.Disk = &DiskSpec{Format: "qcow2", Encryption: "none"}
	valid.BuildRequirements = &execution.Requirements{
		Resources: &execution.ResourceRequest{
			CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192,
		},
		AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
		Capabilities: []execution.CapabilityRequirement{{
			Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred,
		}},
		TimeoutSeconds: 7200,
	}
	valid.ToolchainImages = map[string]string{"amd64": valid.ToolchainImage}
	validate := func(component Component) error {
		cat := base(component)
		cat.Components["x"] = component
		return cat.Validate()
	}
	tests := []struct {
		name     string
		edit     func(*Component)
		wantText string
	}{
		{"missing disk", func(c *Component) { c.Disk = nil }, ""},
		{"unsupported format", func(c *Component) { c.Disk.Format = "raw" }, ""},
		{"unsupported encryption", func(c *Component) { c.Disk.Encryption = "aes" }, ""},
		{"disk on another output", func(c *Component) { c.OutputType = "raw" }, ""},
		{"vm builder on another output", func(c *Component) { c.BuilderType = BuilderVMImage; c.OutputType = "raw"; c.Disk = nil }, ""},
		{"vm builder without build requirements", func(c *Component) { c.BuilderType = BuilderVMImage; c.BuildRequirements = nil }, "generic buildRequirements"},
		{"vm builder without resource request", func(c *Component) { c.BuilderType = BuilderVMImage; c.BuildRequirements.Resources = nil }, "resource request"},
		{"vm builder without CPU", func(c *Component) { c.BuilderType = BuilderVMImage; c.BuildRequirements.Resources.CPUMillis = 0 }, "positive CPU"},
		{"vm builder without memory", func(c *Component) { c.BuilderType = BuilderVMImage; c.BuildRequirements.Resources.MemoryMiB = 0 }, "positive memory"},
		{"vm builder without ephemeral storage", func(c *Component) {
			c.BuilderType = BuilderVMImage
			c.BuildRequirements.Resources.EphemeralStorageMiB = 0
		}, "positive ephemeral storage"},
		{"vm builder without host platform", func(c *Component) { c.BuilderType = BuilderVMImage; c.BuildRequirements.AllowedHostPlatforms = nil }, "allowed host platform"},
		{"vm builder without virtualization policy", func(c *Component) { c.BuilderType = BuilderVMImage; c.BuildRequirements.Capabilities = nil }, "KVM or HVF capability policy"},
		{"vm builder without timeout", func(c *Component) { c.BuilderType = BuilderVMImage; c.BuildRequirements.TimeoutSeconds = 0 }, "positive executor timeout"},
		{"vm builder with CGO", func(c *Component) { c.BuilderType = BuilderVMImage; c.CGO = true }, ""},
		{"vm builder with container write directory", func(c *Component) { c.BuilderType = BuilderVMImage; c.WriteDir = "out" }, ""},
		{"vm builder not from source", func(c *Component) {
			c.BuilderType = BuilderVMImage
			c.FromSource = false
			c.BuildRequirements = nil
			c.ExclusionReason = "not built"
			c.OutputType = "host-package"
			c.Disk = nil
		}, "vm-image builder requires a from-source component"},
		{"excluded disk", func(c *Component) {
			c.FromSource = false
			c.ExclusionReason = "not built"
		}, ""},
		{"prebuilt disk", func(c *Component) {
			c.FromSource = false
			c.Prebuilt = &Prebuilt{URL: "https://example.test/image.qcow2", Digests: map[string]string{
				"amd64": "sha256:59b8183301af6dc358c9258d7b2ab0ee1a9363618552334fb3b160d454cbda72",
			}, Evidence: &ExternalEvidence{None: true}}
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			component := valid
			disk := *valid.Disk
			component.Disk = &disk
			component.BuildRequirements = execution.Clone(valid.BuildRequirements)
			tt.edit(&component)
			err := validate(component)
			if coded, ok := err.(*nvkrerr.Error); !ok || coded.Code != nvkrerr.CodeInvalidCatalog {
				t.Fatalf("error = %v, want %s", err, nvkrerr.CodeInvalidCatalog)
			}
			if tt.wantText != "" && !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("error = %v, want text %q", err, tt.wantText)
			}
		})
	}
	if err := validate(valid); err != nil {
		t.Fatalf("disk-image must not require the vm-image builder: %v", err)
	}
}

func TestVMImageHostRouteAndToolchainValidation(t *testing.T) {
	valid := goodComponent()
	valid.BuilderType = BuilderVMImage
	valid.OutputType = "disk-image"
	valid.Disk = &DiskSpec{Format: "qcow2", Encryption: "none"}
	valid.BuildRequirements = &execution.Requirements{
		Resources: &execution.ResourceRequest{
			CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192,
		},
		AllowedHostPlatforms: []execution.HostPlatform{
			execution.HostPlatformLinuxAMD64, execution.HostPlatformLinuxARM64,
		},
		Capabilities: []execution.CapabilityRequirement{{
			Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred,
		}},
		TimeoutSeconds: 7200,
	}
	valid.ToolchainImages = map[string]string{"amd64": goodImage, "arm64": goodImage}
	validate := func(component Component, guestArch string) error {
		cat := base(component)
		component.Platforms = []string{"linux/" + guestArch}
		cat.Components["x"] = component
		return cat.Validate()
	}

	missingHostToolchain := valid
	missingHostToolchain.ToolchainImages = map[string]string{"amd64": goodImage}
	if err := validate(missingHostToolchain, "amd64"); catalogErrorCode(err) != nvkrerr.CodeUnpinnedSource ||
		!strings.Contains(err.Error(), `host platform "linux/arm64"`) {
		t.Fatalf("uncovered VM host toolchain route returned %v", err)
	}
	completeHostToolchains := missingHostToolchain
	completeHostToolchains.ToolchainImages = map[string]string{"amd64": goodImage, "arm64": goodImage}
	if err := validate(completeHostToolchains, "amd64"); err != nil {
		t.Fatalf("complete per-host VM toolchain routes rejected: %v", err)
	}

	requiredCrossArch := valid
	requiredCrossArch.BuildRequirements = execution.Clone(valid.BuildRequirements)
	requiredCrossArch.BuildRequirements.AllowedHostPlatforms = []execution.HostPlatform{execution.HostPlatformLinuxAMD64}
	requiredCrossArch.BuildRequirements.Capabilities[0].Policy = execution.CapabilityRequired
	if err := validate(requiredCrossArch, "arm64"); catalogErrorCode(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("required KVM without a same-architecture host route returned %v", err)
	}
	requiredNative := requiredCrossArch
	requiredNative.BuildRequirements = execution.Clone(requiredCrossArch.BuildRequirements)
	requiredNative.BuildRequirements.AllowedHostPlatforms = []execution.HostPlatform{
		execution.HostPlatformLinuxAMD64,
		execution.HostPlatformLinuxARM64,
	}
	if err := validate(requiredNative, "arm64"); err != nil {
		t.Fatalf("required KVM with a same-architecture host route rejected: %v", err)
	}
	requiredCrossArch.BuildRequirements.Capabilities[0].Policy = execution.CapabilityPreferred
	if err := validate(requiredCrossArch, "arm64"); err != nil {
		t.Fatalf("preferred KVM cross-architecture route rejected: %v", err)
	}
}

func TestNonVMRequiredKVMHostRouteRemainsDeferred(t *testing.T) {
	component := goodComponent()
	component.BuildRequirements = &execution.Requirements{
		AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
		Capabilities: []execution.CapabilityRequirement{{
			Name: execution.CapabilityKVM, Policy: execution.CapabilityRequired,
		}},
	}
	cat := base(component)
	component.Platforms = []string{"linux/arm64"}
	cat.Components["x"] = component

	// ADR-18 defines the target-architecture/runner-host relationship only for
	// vm-image. Preserve the authored requirement for future Environment matching
	// without inventing guest semantics for an ordinary builder. The pipeline and
	// factory adapter suites independently prove that every current adapter rejects
	// this non-nil request before execution or output.
	if err := cat.Validate(); err != nil {
		t.Fatalf("ordinary builder requirements were given VM guest semantics: %v", err)
	}
	if got := cat.Components["x"].BuildRequirements; !execution.Equal(got, component.BuildRequirements) {
		t.Fatalf("ordinary builder requirements were not preserved for adapter admission: %#v", got)
	}
}
