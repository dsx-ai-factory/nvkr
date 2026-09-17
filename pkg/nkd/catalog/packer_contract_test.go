package catalog

import (
	"context"
	"strings"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

func TestDirectPackerProducerLoadsStrictlyAndAffectsCatalogIdentity(t *testing.T) {
	registry := `apiVersion: nkd.dev/catalog/v1
kind: SourceBuildRegistry
sources:
  image-source:
    repo: https://github.com/example/node-image
    toolchainImage: ` + goodImage + `
recipes:
  - id: node-amd64
    criteria:
      category: os
      os: ubuntu
      arch: amd64
    components:
      - node-image
components:
  node-image:
    fromSource: true
    source: image-source
    builderType: vm-image
    outputType: disk-image
    disk:
      format: qcow2
      encryption: none
    buildRequirements:
      resources:
        cpuMillis: 4000
        memoryMiB: 8192
        ephemeralStorageMiB: 32768
      allowedHostPlatforms:
        - linux/amd64
      capabilities:
        - name: kvm
          policy: preferred
      timeoutSeconds: 10800
    packer:
      projectPath: os-image/packer
      buildSelector: qemu.nke-node
      architectureVariable: architecture
      materialManifestVariable: material_manifest
      outputDirectoryVariable: output_directory
    artifactPath: output/node-{{.Arch}}.qcow2
`
	lock := `apiVersion: nkd.dev/lock/v1
kind: PinLock
pins:
  image-source:
    tag: v1.0.0
    commit: ` + goodCommit + `
    treeHash: ` + goodTree + `
    sourceDateEpoch: 1733939955
`
	load := func(t *testing.T, authored string) *Catalog {
		t.Helper()
		cat, err := LoadContext(context.Background(), bytesProvider{
			RegistryFile: []byte(authored), LockFile: []byte(lock),
		})
		if err != nil {
			t.Fatal(err)
		}
		return cat
	}

	cat := load(t, registry)
	component := cat.Components["node-image"]
	if component.Packer == nil || component.Packer.ProjectPath != "os-image/packer" ||
		component.Packer.BuildSelector != "qemu.nke-node" ||
		component.Packer.ArchitectureVariable != "architecture" ||
		component.Packer.MaterialManifestVariable != "material_manifest" ||
		component.Packer.OutputDirectoryVariable != "output_directory" {
		t.Fatalf("loaded component lost its direct Packer contract: %#v", component)
	}
	if len(component.BuildCommand) != 0 {
		t.Fatalf("direct Packer component unexpectedly has buildCommand: %#v", component.BuildCommand)
	}

	unknown := strings.Replace(registry, "buildSelector: qemu.nke-node", "buildSelector: qemu.nke-node\n      futureOption: true", 1)
	if _, err := LoadContext(context.Background(), bytesProvider{
		RegistryFile: []byte(unknown), LockFile: []byte(lock),
	}); catalogErrorCode(err) != nkderr.CodeInvalidCatalog || !strings.Contains(err.Error(), "futureOption") {
		t.Fatalf("unknown nested Packer field was not rejected strictly: %v", err)
	}

	before, err := cat.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, old, replacement string
	}{
		{name: "project path", old: "projectPath: os-image/packer", replacement: "projectPath: image/packer"},
		{name: "build selector", old: "buildSelector: qemu.nke-node", replacement: "buildSelector: qemu.nke-node-fde"},
		{name: "architecture variable", old: "architectureVariable: architecture", replacement: "architectureVariable: target_arch"},
		{name: "material manifest variable", old: "materialManifestVariable: material_manifest", replacement: "materialManifestVariable: inputs_manifest"},
		{name: "output directory variable", old: "outputDirectoryVariable: output_directory", replacement: "outputDirectoryVariable: build_output"},
	} {
		t.Run(test.name+" changes snapshot identity", func(t *testing.T) {
			changed := load(t, strings.Replace(registry, test.old, test.replacement, 1))
			after, err := changed.SnapshotDigest()
			if err != nil {
				t.Fatal(err)
			}
			if before == after {
				t.Fatalf("%s did not affect catalog snapshot identity", test.name)
			}
		})
	}
}

func TestDirectPackerProducerValidation(t *testing.T) {
	valid := goodComponent()
	valid.BuilderType = BuilderVMImage
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
	valid.BuildCommand = nil
	valid.Packer = &PackerConfig{
		ProjectPath:              "os-image/packer",
		BuildSelector:            "qemu.nke-node",
		ArchitectureVariable:     "architecture",
		MaterialManifestVariable: "material_manifest",
		OutputDirectoryVariable:  "output_directory",
	}
	validate := func(component Component) error {
		cat := base(component)
		cat.Recipes[0].Criteria.Category = "os"
		return cat.Validate()
	}
	if err := validate(valid); err != nil {
		t.Fatalf("valid direct Packer producer rejected: %v", err)
	}
	hyphenatedVariable := valid
	hyphenatedPacker := *valid.Packer
	hyphenatedPacker.ArchitectureVariable = "target-arch"
	hyphenatedVariable.Packer = &hyphenatedPacker
	if err := validate(hyphenatedVariable); err != nil {
		t.Fatalf("valid HCL variable identifier with a hyphen rejected: %v", err)
	}
	unicodeVariable := valid
	unicodePacker := *valid.Packer
	unicodePacker.ArchitectureVariable = "архитектура"
	unicodeVariable.Packer = &unicodePacker
	if err := validate(unicodeVariable); err != nil {
		t.Fatalf("valid Unicode HCL variable identifier rejected: %v", err)
	}
	rootProject := valid
	rootPacker := *valid.Packer
	rootPacker.ProjectPath = "."
	rootProject.Packer = &rootPacker
	if err := validate(rootProject); err != nil {
		t.Fatalf("source-root Packer project rejected: %v", err)
	}

	for _, test := range []struct {
		name     string
		edit     func(*Component)
		wantText string
	}{
		{
			name: "command and Packer are contradictory",
			edit: func(component *Component) {
				component.BuildCommand = []string{"make", "image"}
			},
			wantText: "exactly one producer",
		},
		{
			name: "missing producer",
			edit: func(component *Component) {
				component.Packer = nil
			},
			wantText: "exactly one producer",
		},
		{
			name: "Packer requires vm-image",
			edit: func(component *Component) {
				component.BuilderType = BuilderOCIHermetic
				component.OutputType = "raw"
				component.Disk = nil
			},
			wantText: "requires builderType vm-image",
		},
		{
			name: "empty project path",
			edit: func(component *Component) {
				component.Packer.ProjectPath = ""
			},
			wantText: "clean source-relative path",
		},
		{
			name: "absolute project path",
			edit: func(component *Component) {
				component.Packer.ProjectPath = "/os-image/packer"
			},
			wantText: "clean source-relative path",
		},
		{
			name: "traversing project path",
			edit: func(component *Component) {
				component.Packer.ProjectPath = "../packer"
			},
			wantText: "clean source-relative path",
		},
		{
			name: "templated project path",
			edit: func(component *Component) {
				component.Packer.ProjectPath = "packer/{{.Arch}}"
			},
			wantText: "clean source-relative path",
		},
		{
			name: "empty selector",
			edit: func(component *Component) {
				component.Packer.BuildSelector = ""
			},
			wantText: "exact HCL source selector",
		},
		{
			name: "wildcard selector",
			edit: func(component *Component) {
				component.Packer.BuildSelector = "qemu.*"
			},
			wantText: "exact HCL source selector",
		},
		{
			name: "selector list",
			edit: func(component *Component) {
				component.Packer.BuildSelector = "qemu.node,qemu.fde"
			},
			wantText: "exact HCL source selector",
		},
		{
			name: "selector with an empty segment",
			edit: func(component *Component) {
				component.Packer.BuildSelector = "qemu..node"
			},
			wantText: "exact HCL source selector",
		},
		{
			name: "selector exceeds bound",
			edit: func(component *Component) {
				component.Packer.BuildSelector = strings.Repeat("a", 255) + ".b"
			},
			wantText: "exact HCL source selector",
		},
		{
			name: "invalid architecture variable",
			edit: func(component *Component) {
				component.Packer.ArchitectureVariable = "9arch"
			},
			wantText: "HCL variable identifier",
		},
		{
			name: "invalid material manifest variable",
			edit: func(component *Component) {
				component.Packer.MaterialManifestVariable = "material manifest"
			},
			wantText: "HCL variable identifier",
		},
		{
			name: "invalid output variable",
			edit: func(component *Component) {
				component.Packer.OutputDirectoryVariable = ""
			},
			wantText: "HCL variable identifier",
		},
		{
			name: "variable exceeds bound",
			edit: func(component *Component) {
				component.Packer.OutputDirectoryVariable = strings.Repeat("a", 129)
			},
			wantText: "HCL variable identifier",
		},
		{
			name: "duplicate architecture and material variables",
			edit: func(component *Component) {
				component.Packer.MaterialManifestVariable = component.Packer.ArchitectureVariable
			},
			wantText: "must be distinct",
		},
		{
			name: "duplicate material and output variables",
			edit: func(component *Component) {
				component.Packer.OutputDirectoryVariable = component.Packer.MaterialManifestVariable
			},
			wantText: "must be distinct",
		},
		{
			name: "absolute artifact path",
			edit: func(component *Component) {
				component.ArtifactPath = "/output/node.qcow2"
			},
			wantText: "relative to the private output directory",
		},
		{
			name: "traversing artifact path",
			edit: func(component *Component) {
				component.ArtifactPath = "../node.qcow2"
			},
			wantText: "relative to the private output directory",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			component := valid
			disk := *valid.Disk
			component.Disk = &disk
			component.BuildRequirements = execution.Clone(valid.BuildRequirements)
			packer := *valid.Packer
			component.Packer = &packer
			test.edit(&component)
			err := validate(component)
			if catalogErrorCode(err) != nkderr.CodeInvalidCatalog || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("error = %v, want %s containing %q", err, nkderr.CodeInvalidCatalog, test.wantText)
			}
		})
	}
}

func TestVMImageCommandProducerRemainsValid(t *testing.T) {
	component := goodComponent()
	component.BuilderType = BuilderVMImage
	component.OutputType = "disk-image"
	component.Disk = &DiskSpec{Format: "qcow2", Encryption: "none"}
	component.BuildRequirements = &execution.Requirements{
		Resources: &execution.ResourceRequest{
			CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192,
		},
		AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
		Capabilities: []execution.CapabilityRequirement{{
			Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred,
		}},
		TimeoutSeconds: 7200,
	}
	component.BuildCommand = []string{"make", "image"}
	component.Packer = nil
	cat := base(component)
	cat.Recipes[0].Criteria.Category = "os"
	if err := cat.Validate(); err != nil {
		t.Fatalf("existing command/Make vm-image producer rejected: %v", err)
	}
}
