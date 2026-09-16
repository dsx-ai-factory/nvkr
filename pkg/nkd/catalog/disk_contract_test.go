package catalog

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/compose"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestDiskImageCatalogSchemaAndIdentity(t *testing.T) {
	const vmToolchain = "example.invalid/nkd-vm-builder@sha256:59b8183301af6dc358c9258d7b2ab0ee1a9363618552334fb3b160d454cbda72"
	registry := `apiVersion: nkd.dev/catalog/v1
kind: SourceBuildRegistry
sources:
  image-source: {repo: https://github.com/example/node-image, toolchainImage: ` + vmToolchain + `}
recipes:
  - id: node-amd64
    criteria: {category: os, os: ubuntu, arch: amd64}
    components: [node-fde, node-plain]
  - id: node-arm64
    criteria: {category: os, os: ubuntu, arch: arm64}
    components: [node-plain]
components:
  node-fde:
    fromSource: true
    source: image-source
    builderType: vm-image
    outputType: disk-image
    disk: {format: qcow2, encryption: luks2}
    vmImage:
      # Schema-fixture values only; NKE host-capacity minima require measured evidence.
      allowedHostArchs: [amd64, arm64]
      minHostCPUs: 4
      minHostMemoryMiB: 8192
      minScratchMiB: 32768
      minOutputMiB: 12288
      kvm: optional
      network: default
      timeoutSeconds: 10800
    buildCommand: [make, -C, os-image/packer, os-image-fde, "ARCH={{.Arch}}"]
    artifactPath: "os-image/packer/output-fde/nke-node-fde-ubuntu-26.04.1-{{.Arch}}.qcow2"
  node-plain:
    fromSource: true
    source: image-source
    builderType: vm-image
    outputType: disk-image
    disk: {format: qcow2, encryption: none}
    vmImage:
      # Schema-fixture values only; NKE host-capacity minima require measured evidence.
      allowedHostArchs: [amd64, arm64]
      minHostCPUs: 4
      minHostMemoryMiB: 8192
      minScratchMiB: 32768
      minOutputMiB: 12288
      kvm: optional
      network: default
      timeoutSeconds: 18000
    buildCommand: [make, -C, os-image/packer, os-image-non-fde, "ARCH={{.Arch}}"]
    artifactPath: "os-image/packer/output-non-fde/nke-node-non-fde-ubuntu-26.04.1-{{.Arch}}.qcow2"
`
	lock := `apiVersion: nkd.dev/lock/v1
kind: PinLock
pins:
  image-source: {tag: v1.0.0, commit: ` + goodCommit + `, treeHash: ` + goodTree + `, sourceDateEpoch: 1733939955}
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
	unknownVMField := strings.Replace(registry, "      timeoutSeconds: 18000", "      timeoutSeconds: 18000\n      futureCapability: true", 1)
	if _, err := LoadContext(context.Background(), bytesProvider{
		RegistryFile: []byte(unknownVMField), LockFile: []byte(lock),
	}); catalogErrorCode(err) != nkderr.CodeInvalidCatalog || !strings.Contains(err.Error(), "futureCapability") {
		t.Fatalf("unknown nested vmImage field was not rejected strictly: %v", err)
	}
	plain := cat.Components["node-plain"]
	wantCommand := []string{"make", "-C", "os-image/packer", "os-image-non-fde", "ARCH={{.Arch}}"}
	if plain.BuilderType != BuilderVMImage || plain.Disk == nil || plain.Disk.Format != "qcow2" ||
		plain.Disk.Encryption != "none" || !slices.Equal(plain.BuildCommand, wantCommand) ||
		plain.ArtifactPath != "os-image/packer/output-non-fde/nke-node-non-fde-ubuntu-26.04.1-{{.Arch}}.qcow2" ||
		plain.VMImage == nil || !slices.Equal(plain.VMImage.AllowedHostArchs, []string{"amd64", "arm64"}) ||
		plain.VMImage.KVM != VMKVMOptional || plain.VMImage.Network != VMNetworkDefault {
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
	identityChanges := []struct {
		name string
		edit func(*VMImageRequirements)
	}{
		{"allowed host architectures", func(vm *VMImageRequirements) { vm.AllowedHostArchs = []string{"amd64"} }},
		{"minimum host CPUs", func(vm *VMImageRequirements) { vm.MinHostCPUs++ }},
		{"minimum host memory", func(vm *VMImageRequirements) { vm.MinHostMemoryMiB++ }},
		{"minimum scratch", func(vm *VMImageRequirements) { vm.MinScratchMiB++ }},
		{"minimum output", func(vm *VMImageRequirements) { vm.MinOutputMiB++ }},
		{"KVM policy", func(vm *VMImageRequirements) { vm.KVM = VMKVMForbidden }},
		{"network policy", func(vm *VMImageRequirements) { vm.Network = VMNetworkNone }},
		{"timeout", func(vm *VMImageRequirements) { vm.TimeoutSeconds++ }},
	}
	for _, test := range identityChanges {
		t.Run(test.name+" affects catalog identity", func(t *testing.T) {
			changedVM := load(t)
			component := changedVM.Components["node-plain"]
			vmImage := *component.VMImage
			vmImage.AllowedHostArchs = append([]string(nil), component.VMImage.AllowedHostArchs...)
			test.edit(&vmImage)
			component.VMImage = &vmImage
			changedVM.Components["node-plain"] = component
			vmDigest, err := changedVM.SnapshotDigest()
			if err != nil {
				t.Fatal(err)
			}
			if before == vmDigest {
				t.Fatalf("%s did not affect catalog identity", test.name)
			}
		})
	}
}

func TestVMImageRequirementsValidation(t *testing.T) {
	validRequirements := func() *VMImageRequirements {
		return &VMImageRequirements{
			AllowedHostArchs: []string{"amd64", "arm64"},
			MinHostCPUs:      4,
			MinHostMemoryMiB: 8192,
			MinScratchMiB:    32768,
			MinOutputMiB:     12288,
			KVM:              VMKVMOptional,
			Network:          VMNetworkDefault,
			TimeoutSeconds:   18000,
		}
	}
	validComponent := func() Component {
		component := goodComponent()
		component.BuilderType = BuilderVMImage
		component.OutputType = "disk-image"
		component.Disk = &DiskSpec{Format: "qcow2", Encryption: "none"}
		component.VMImage = validRequirements()
		return component
	}
	validate := func(component Component) error {
		cat := base(component)
		cat.Recipes[0].Criteria.Category = "os"
		cat.Recipes[0].Criteria.OS = "ubuntu"
		cat.Recipes[0].Criteria.Arch = "amd64"
		return cat.Validate()
	}
	tests := []struct {
		name string
		edit func(*Component)
	}{
		{"missing requirements", func(c *Component) { c.VMImage = nil }},
		{"requirements on OCI builder", func(c *Component) { c.BuilderType = BuilderOCIHermetic }},
		{"empty host architectures", func(c *Component) { c.VMImage.AllowedHostArchs = nil }},
		{"unsorted host architectures", func(c *Component) { c.VMImage.AllowedHostArchs = []string{"arm64", "amd64"} }},
		{"duplicate host architecture", func(c *Component) { c.VMImage.AllowedHostArchs = []string{"amd64", "amd64"} }},
		{"unknown host architecture", func(c *Component) { c.VMImage.AllowedHostArchs = []string{"riscv64"} }},
		{"zero host CPUs", func(c *Component) { c.VMImage.MinHostCPUs = 0 }},
		{"zero host memory", func(c *Component) { c.VMImage.MinHostMemoryMiB = 0 }},
		{"zero scratch", func(c *Component) { c.VMImage.MinScratchMiB = 0 }},
		{"zero output", func(c *Component) { c.VMImage.MinOutputMiB = 0 }},
		{"zero timeout", func(c *Component) { c.VMImage.TimeoutSeconds = 0 }},
		{"unknown network", func(c *Component) { c.VMImage.Network = "sometimes" }},
		{"unknown KVM mode", func(c *Component) { c.VMImage.KVM = "sometimes" }},
		{"CGO", func(c *Component) { c.CGO = true }},
		{"container write directory", func(c *Component) { c.WriteDir = "out" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			component := validComponent()
			test.edit(&component)
			err := validate(component)
			if coded, ok := err.(*nkderr.Error); !ok || coded.Code != nkderr.CodeInvalidCatalog {
				t.Fatalf("error = %v, want %s", err, nkderr.CodeInvalidCatalog)
			}
		})
	}
	if err := validate(validComponent()); err != nil {
		t.Fatalf("valid VM image requirements rejected: %v", err)
	}
	missingHostToolchain := validComponent()
	missingHostToolchain.ToolchainImage = ""
	missingHostToolchain.ToolchainImages = map[string]string{"amd64": goodImage}
	if err := validate(missingHostToolchain); catalogErrorCode(err) != nkderr.CodeUnpinnedSource ||
		!strings.Contains(err.Error(), `host architecture "arm64"`) {
		t.Fatalf("uncovered VM host toolchain route returned %v", err)
	}
	completeHostToolchains := validComponent()
	completeHostToolchains.ToolchainImage = ""
	completeHostToolchains.ToolchainImages = map[string]string{"amd64": goodImage, "arm64": goodImage}
	if err := validate(completeHostToolchains); err != nil {
		t.Fatalf("complete per-host VM toolchain routes rejected: %v", err)
	}
	validModes := []struct {
		name    string
		kvm     string
		network string
	}{
		{"KVM required with network", VMKVMRequired, VMNetworkDefault},
		{"KVM optional with network", VMKVMOptional, VMNetworkDefault},
		{"TCG-only offline", VMKVMForbidden, VMNetworkNone},
	}
	for _, test := range validModes {
		t.Run(test.name, func(t *testing.T) {
			component := validComponent()
			component.VMImage.KVM = test.kvm
			component.VMImage.Network = test.network
			if err := validate(component); err != nil {
				t.Fatalf("valid VM image mode rejected: %v", err)
			}
		})
	}
	withVersionStamp := validComponent()
	withVersionStamp.VersionStamp = map[string]string{"NKE_VERSION": "v1.0.0"}
	if err := validate(withVersionStamp); err != nil {
		t.Fatalf("pin-rendered VM image version stamp rejected: %v", err)
	}
	impossible := validComponent()
	impossible.VMImage.KVM = VMKVMRequired
	impossible.VMImage.AllowedHostArchs = []string{"amd64"}
	cat := base(impossible)
	cat.Recipes[0].Criteria.Category = "os"
	cat.Recipes[0].Criteria.OS = "ubuntu"
	cat.Recipes[0].Criteria.Arch = "arm64"
	if err := cat.Validate(); catalogErrorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("required KVM without a same-architecture host route returned %v", err)
	}
}

func TestVMImageRequirementsOverrideIsStrictAndPreservesUntouchedFields(t *testing.T) {
	component := goodComponent()
	component.BuilderType = BuilderVMImage
	component.OutputType = "disk-image"
	component.Disk = &DiskSpec{Format: "qcow2", Encryption: "none"}
	component.VMImage = &VMImageRequirements{
		AllowedHostArchs: []string{"amd64", "arm64"}, MinHostCPUs: 4, MinHostMemoryMiB: 8192,
		MinScratchMiB: 32768, MinOutputMiB: 12288, KVM: VMKVMOptional,
		Network: VMNetworkDefault, TimeoutSeconds: 18000,
	}
	cat := base(component)
	got, err := cat.ComposeComponents([]compose.ComponentRef{{
		Name: "x", Overrides: map[string]any{"vmImage": map[string]any{"minHostCPUs": 8}},
	}})
	if err != nil || len(got) != 1 {
		t.Fatalf("nested VM requirements override = %#v, %v", got, err)
	}
	vm := got[0].VMImage
	if vm.MinHostCPUs != 8 || vm.MinHostMemoryMiB != 8192 || vm.TimeoutSeconds != 18000 ||
		!slices.Equal(vm.AllowedHostArchs, []string{"amd64", "arm64"}) {
		t.Fatalf("nested override lost untouched VM requirements: %#v", vm)
	}
	if cat.Components["x"].VMImage.MinHostCPUs != 4 {
		t.Fatal("applying a VM requirements override mutated the source catalog")
	}
	for _, overrides := range []map[string]any{
		{"vmImage": map[string]any{"network": "sometimes"}},
		{"vmImage": map[string]any{"minHostCPU": 8}},
	} {
		if got, err := cat.ComposeComponents([]compose.ComponentRef{{Name: "x", Overrides: overrides}}); catalogErrorCode(err) != nkderr.CodeInvalidCatalog || got != nil {
			t.Fatalf("invalid VM requirements override = %#v, %v; want no result and %s", got, err, nkderr.CodeInvalidCatalog)
		}
	}
}

func TestDiskImageSchemaValidation(t *testing.T) {
	valid := goodComponent()
	valid.OutputType = "disk-image"
	valid.Disk = &DiskSpec{Format: "qcow2", Encryption: "none"}
	validate := func(component Component) error {
		cat := base(component)
		cat.Recipes[0].Criteria.Category = "os"
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
		{"vm builder not from source", func(c *Component) {
			c.BuilderType = BuilderVMImage
			c.FromSource = false
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
			tt.edit(&component)
			err := validate(component)
			if coded, ok := err.(*nkderr.Error); !ok || coded.Code != nkderr.CodeInvalidCatalog {
				t.Fatalf("error = %v, want %s", err, nkderr.CodeInvalidCatalog)
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
