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
      - agent-package
      - node-image
      - runtime-package
components:
  agent-package:
    fromSource: true
    source: image-source
    outputType: host-package
    buildCommand: [make, agent-package]
    artifactPath: out/agent.deb
  runtime-package:
    fromSource: true
    source: image-source
    outputType: host-package
    buildCommand: [make, runtime-package]
    artifactPath: out/runtime.deb
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
      buildName: qemu.nke-non-fde
      architectureVariable: architecture
      materialManifestVariable: material_manifest
      outputDirectoryVariable: output_directory
      variables:
        image_variant: standard
    materials:
      agent-package: agent-package
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
		component.Packer.BuildName != "qemu.nke-non-fde" ||
		component.Packer.ArchitectureVariable != "architecture" ||
		component.Packer.MaterialManifestVariable != "material_manifest" ||
		component.Packer.OutputDirectoryVariable != "output_directory" {
		t.Fatalf("loaded component lost its direct Packer contract: %#v", component)
	}
	if len(component.BuildCommand) != 0 {
		t.Fatalf("direct Packer component unexpectedly has buildCommand: %#v", component.BuildCommand)
	}
	if component.Materials["agent-package"] != "agent-package" ||
		component.Packer.Variables["image_variant"] != "standard" {
		t.Fatalf("loaded component lost its declared material or variable bindings: %#v", component)
	}

	unknown := strings.Replace(registry, "buildName: qemu.nke-non-fde", "buildName: qemu.nke-non-fde\n      futureOption: true", 1)
	if _, err := LoadContext(context.Background(), bytesProvider{
		RegistryFile: []byte(unknown), LockFile: []byte(lock),
	}); catalogErrorCode(err) != nkderr.CodeInvalidCatalog || !strings.Contains(err.Error(), "futureOption") {
		t.Fatalf("unknown nested Packer field was not rejected strictly: %v", err)
	}
	duplicateBinding := strings.Replace(registry, "agent-package: agent-package",
		"agent-package: agent-package\n      agent-package: agent-package", 1)
	if _, err := LoadContext(context.Background(), bytesProvider{
		RegistryFile: []byte(duplicateBinding), LockFile: []byte(lock),
	}); catalogErrorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("duplicate material binding was not rejected strictly: %v", err)
	}
	before, err := cat.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, old, replacement string
	}{
		{name: "project path", old: "projectPath: os-image/packer", replacement: "projectPath: image/packer"},
		{name: "build name", old: "buildName: qemu.nke-non-fde", replacement: "buildName: qemu.nke-fde"},
		{name: "architecture variable", old: "architectureVariable: architecture", replacement: "architectureVariable: target_arch"},
		{name: "material manifest variable", old: "materialManifestVariable: material_manifest", replacement: "materialManifestVariable: inputs_manifest"},
		{name: "output directory variable", old: "outputDirectoryVariable: output_directory", replacement: "outputDirectoryVariable: build_output"},
		{name: "material binding name", old: "agent-package: agent-package", replacement: "host-agent-package: agent-package"},
		{name: "material component", old: "agent-package: agent-package", replacement: "agent-package: runtime-package"},
		{name: "Packer variable name", old: "image_variant: standard", replacement: "node_variant: standard"},
		{name: "Packer variable value", old: "image_variant: standard", replacement: "image_variant: minimal"},
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
		BuildName:                "qemu.nke-non-fde",
		ArchitectureVariable:     "architecture",
		MaterialManifestVariable: "material_manifest",
		OutputDirectoryVariable:  "output_directory",
	}
	valid.ArtifactPath = "output/node-{{.Arch}}.qcow2"
	validate := func(component Component) error {
		cat := base(component)
		cat.Recipes[0].Criteria.Category = "os"
		return cat.Validate()
	}
	if err := validate(valid); err != nil {
		t.Fatalf("valid direct Packer producer rejected: %v", err)
	}
	opaquePackerNames := valid
	opaquePackerConfig := *valid.Packer
	opaquePackerConfig.BuildName = "image.qemu.nke-non-fde"
	opaquePackerConfig.ArchitectureVariable = "target-arch"
	opaquePackerNames.Packer = &opaquePackerConfig
	if err := validate(opaquePackerNames); err != nil {
		t.Fatalf("transport-safe Packer-owned names rejected: %v", err)
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
			name: "project path with control character",
			edit: func(component *Component) {
				component.Packer.ProjectPath = "os-image/\npacker"
			},
			wantText: "clean source-relative path",
		},
		{
			name: "project path exceeds bound",
			edit: func(component *Component) {
				component.Packer.ProjectPath = strings.Repeat("a", maxPackerProjectPathBytes+1)
			},
			wantText: "clean source-relative path",
		},
		{
			name: "empty build name",
			edit: func(component *Component) {
				component.Packer.BuildName = ""
			},
			wantText: "exact Packer build name",
		},
		{
			name: "wildcard build selection",
			edit: func(component *Component) {
				component.Packer.BuildName = "qemu.*"
			},
			wantText: "without list or glob syntax",
		},
		{
			name: "build-name list",
			edit: func(component *Component) {
				component.Packer.BuildName = "qemu.node,qemu.fde"
			},
			wantText: "without list or glob syntax",
		},
		{
			name: "question-mark build selection",
			edit: func(component *Component) {
				component.Packer.BuildName = "qemu.node?"
			},
			wantText: "without list or glob syntax",
		},
		{
			name: "bracket build selection",
			edit: func(component *Component) {
				component.Packer.BuildName = "qemu.[nf]de"
			},
			wantText: "without list or glob syntax",
		},
		{
			name: "backslash build selection",
			edit: func(component *Component) {
				component.Packer.BuildName = `qemu.node\fde`
			},
			wantText: "without list or glob syntax",
		},
		{
			name: "build name with control character",
			edit: func(component *Component) {
				component.Packer.BuildName = "qemu.node\n"
			},
			wantText: "exact Packer build name",
		},
		{
			name: "build name exceeds bound",
			edit: func(component *Component) {
				component.Packer.BuildName = strings.Repeat("a", 257)
			},
			wantText: "exact Packer build name",
		},
		{
			name: "empty architecture variable",
			edit: func(component *Component) {
				component.Packer.ArchitectureVariable = ""
			},
			wantText: "reserved variable name",
		},
		{
			name: "delimiter in architecture variable",
			edit: func(component *Component) {
				component.Packer.ArchitectureVariable = "architecture=other"
			},
			wantText: "reserved variable name",
		},
		{
			name: "control character in material manifest variable",
			edit: func(component *Component) {
				component.Packer.MaterialManifestVariable = "material\nmanifest"
			},
			wantText: "reserved variable name",
		},
		{
			name: "delimiter in material manifest variable",
			edit: func(component *Component) {
				component.Packer.MaterialManifestVariable = "material_manifest=other"
			},
			wantText: "reserved variable name",
		},
		{
			name: "invalid UTF-8 output variable",
			edit: func(component *Component) {
				component.Packer.OutputDirectoryVariable = string([]byte{0xff})
			},
			wantText: "reserved variable name",
		},
		{
			name: "delimiter in output directory variable",
			edit: func(component *Component) {
				component.Packer.OutputDirectoryVariable = "output_directory=other"
			},
			wantText: "reserved variable name",
		},
		{
			name: "variable exceeds bound",
			edit: func(component *Component) {
				component.Packer.OutputDirectoryVariable = strings.Repeat("a", 129)
			},
			wantText: "reserved variable name",
		},
		{
			name: "duplicate architecture and material variables",
			edit: func(component *Component) {
				component.Packer.MaterialManifestVariable = component.Packer.ArchitectureVariable
			},
			wantText: "reserved variable name",
		},
		{
			name: "duplicate material and output variables",
			edit: func(component *Component) {
				component.Packer.OutputDirectoryVariable = component.Packer.MaterialManifestVariable
			},
			wantText: "reserved variable name",
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
		{
			name: "artifact path with control character",
			edit: func(component *Component) {
				component.ArtifactPath = "output/\nnode.qcow2"
			},
			wantText: "relative to the private output directory",
		},
		{
			name: "artifact path exceeds bound",
			edit: func(component *Component) {
				component.ArtifactPath = strings.Repeat("a", maxPackerArtifactPathBytes+1)
			},
			wantText: "relative to the private output directory",
		},
		{
			name: "output-directory artifact path",
			edit: func(component *Component) {
				component.ArtifactPath = "."
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
