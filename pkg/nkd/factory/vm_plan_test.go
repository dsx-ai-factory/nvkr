package factory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func vmFactoryInputs(t *testing.T, platform string, policy execution.CapabilityPolicy) (authored.Definition, authored.Environment, *catalog.Stack) {
	t.Helper()
	component := catalog.Component{
		Platforms: []string{"linux/amd64", "linux/arm64"}, FromSource: true,
		BuilderType: catalog.BuilderVMImage, OutputType: authored.OutputDiskImage,
		Disk: &catalog.DiskSpec{Format: "qcow2", Encryption: "none"},
		BuildRequirements: &execution.Requirements{
			Resources: &execution.ResourceRequest{
				CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 32768,
			},
			AllowedHostPlatforms: []execution.HostPlatform{
				execution.HostPlatformLinuxAMD64, execution.HostPlatformLinuxARM64,
			},
			Capabilities:   []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: policy}},
			TimeoutSeconds: 7200,
		},
		Repo: "https://example.test/image", Tag: "v1.0.0",
		Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImages: map[string]string{
			"amd64": "example.test/vm-amd64@sha256:" + strings.Repeat("c", 64),
			"arm64": "example.test/vm-arm64@sha256:" + strings.Repeat("d", 64),
		},
		BuildCommand: []string{"make", "image", "ARCH={{.Arch}}"},
		ArtifactPath: "out/node-{{.Arch}}.qcow2", SourceDate: 1,
	}
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{"image": component}})
	if err != nil {
		t.Fatal(err)
	}
	definition := authored.Definition{
		APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind,
		Metadata: authored.Metadata{Name: "vm-plan"}, Catalog: authored.Catalog{Base: "catalog"},
		Outputs: []authored.DefinitionOutput{{
			Name: "node-image", Platform: platform, Format: authored.OutputDiskImage,
			Components: []string{"image"},
		}},
	}
	environment := authored.Environment{
		APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind,
		Metadata: authored.Metadata{Name: "test"}, UtilityContainer: fixtureUtilityContainer,
		UtilityPlatform: "linux/amd64",
		ExecutionClasses: []execution.Class{
			{
				Name: "amd64-kvm", RunsOn: "linux-amd64-cpu16", HostPlatform: execution.HostPlatformLinuxAMD64,
				Resources:    execution.ResourceCapacity{CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536},
				Capabilities: []execution.CapabilityName{execution.CapabilityKVM}, MaxTimeoutSeconds: 10800,
			},
			{
				Name: "arm64-kvm", RunsOn: "linux-arm64-cpu16", HostPlatform: execution.HostPlatformLinuxARM64,
				Resources:    execution.ResourceCapacity{CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536},
				Capabilities: []execution.CapabilityName{execution.CapabilityKVM}, MaxTimeoutSeconds: 10800,
			},
		},
	}
	return definition, environment, stack
}

func TestCompileProjectsDiskRootWithoutBinaryBundle(t *testing.T) {
	definition, environment, stack := vmFactoryInputs(t, "linux/arm64", execution.CapabilityRequired)
	plan, err := Compile(context.Background(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.LifecycleOutputs) != 1 ||
		plan.LifecycleOutputs[0].ArtifactID != "artifact/node-image/image" {
		t.Fatalf("disk lifecycle output = %#v", plan.LifecycleOutputs)
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodePackage || node.Kind == NodeSBOM {
			t.Fatalf("disk output gained unsupported package or SBOM work: %#v", node)
		}
	}
	var build ir.Node
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.Component == "image" {
			build = node
		}
	}
	if build.BuilderType != ir.BuilderVMImage || build.Disk == nil || build.Disk.Format != "qcow2" ||
		build.ExecutionBinding == nil || build.ExecutionBinding.Class.HostPlatform != execution.HostPlatformLinuxARM64 ||
		build.Toolchain != stack.Catalog.Components["image"].ToolchainImages["arm64"] {
		t.Fatalf("disk Plan lost VM host/toolchain contract: %#v", build)
	}
	if err := rejectUnsupportedPlanRequirements(plan, "test execution"); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatalf("execution admission error = %v, want %s", err, e.CodeEngineCapability)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateBuildCatalogBindsDiskOutputFormat(t *testing.T) {
	definition, environment, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityPreferred)
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateBuildCatalog(plan.Builds[0], authored.OutputDiskImage, plan.Environment); err != nil {
		t.Fatalf("valid disk catalog binding failed: %v", err)
	}
	err = validateBuildCatalog(plan.Builds[0], authored.OutputExecutable, plan.Environment)
	if factoryErrorCode(err) != e.CodeInvalidCatalog || !strings.Contains(err.Error(), "cannot select a disk-image component") {
		t.Fatalf("mismatched disk output format error = %v, want %s", err, e.CodeInvalidCatalog)
	}
}

func TestCompileDiskWithPinnedSBOMToolRoundTripsWithoutOrdinarySBOM(t *testing.T) {
	definition, environment, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityPreferred)
	definition.SBOMTool = "syft"
	digest := "sha256:" + strings.Repeat("9", 64)
	catalogWithTool := *stack.Catalog
	catalogWithTool.Tools = map[string]catalog.ToolPin{"syft": {
		Format: "raw", URL: "https://example.test/syft", Platform: "linux/amd64",
		ExecutablePath: "syft", ArchiveDigest: digest, ExecutableDigest: digest,
	}}
	stack, err := catalog.ApplyStack(&catalogWithTool)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeSBOM {
			t.Fatalf("disk Plan gained ordinary per-component SBOM node: %#v", node)
		}
	}
	if err := plan.validateSBOMGraph(); err != nil {
		t.Fatal(err)
	}

	changed := *plan
	changed.LifecycleOutputs = append([]LifecycleOutputPlan(nil), plan.LifecycleOutputs...)
	changed.LifecycleOutputs[0].Kind = authored.OutputExecutable
	if err := changed.validateSBOMGraph(); factoryErrorCode(err) != e.CodeProvenanceMissing {
		t.Fatalf("non-authenticated disk exemption error = %v, want %s", err, e.CodeProvenanceMissing)
	}

	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plan.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPlanContext(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LifecycleOutputs[0].ArtifactID != plan.LifecycleOutputs[0].ArtifactID {
		t.Fatalf("round trip changed disk lifecycle artifact: %#v", loaded.LifecycleOutputs)
	}
}

func TestCompiledDiskTargetRecognizerRejectsNearMisses(t *testing.T) {
	definition, environment, stack := vmFactoryInputs(t, "linux/amd64", execution.CapabilityPreferred)
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, baselineArtifacts := planIndex(plan)
	if !plan.hasCompiledDiskTarget("node-image", baselineArtifacts) {
		t.Fatal("valid compiled disk target was not recognized")
	}

	for _, test := range []struct {
		name   string
		mutate func(*Plan, map[string]Artifact)
	}{
		{name: "wrong payload producer", mutate: func(_ *Plan, artifacts map[string]Artifact) {
			payload := artifacts["artifact/node-image/image"]
			payload.Producer = "node-image/build-other"
			artifacts[payload.ID] = payload
		}},
		{name: "wrong payload kind", mutate: func(_ *Plan, artifacts map[string]Artifact) {
			payload := artifacts["artifact/node-image/image"]
			payload.Kind = authored.OutputExecutable
			artifacts[payload.ID] = payload
		}},
		{name: "extra disk root", mutate: func(changed *Plan, _ map[string]Artifact) {
			changed.Builds = append([]BuildPlan(nil), changed.Builds...)
			changed.Builds[0].ResolvedComponents = append(
				[]catalog.NamedComponent(nil), changed.Builds[0].ResolvedComponents...)
			extra := changed.Builds[0].ResolvedComponents[0]
			extra.Name = "other-image"
			changed.Builds[0].ResolvedComponents = append(changed.Builds[0].ResolvedComponents, extra)
		}},
		{name: "non VM root producer", mutate: func(changed *Plan, _ map[string]Artifact) {
			changed.Builds = append([]BuildPlan(nil), changed.Builds...)
			graph := *changed.Builds[0].Graph
			graph.Nodes = append([]ir.Node(nil), graph.Nodes...)
			for index := range graph.Nodes {
				if graph.Nodes[index].Component == "image" {
					graph.Nodes[index].BuilderType = ir.BuilderOCIHermetic
				}
			}
			changed.Builds[0].Graph = &graph
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := *plan
			artifacts := make(map[string]Artifact, len(baselineArtifacts))
			for id, artifact := range baselineArtifacts {
				artifacts[id] = artifact
			}
			test.mutate(&changed, artifacts)
			if changed.hasCompiledDiskTarget("node-image", artifacts) {
				t.Fatal("near-miss disk shape received the SBOM exemption")
			}
		})
	}
}

func TestCompileProjectsPackerMaterialsAsExactProducerInputs(t *testing.T) {
	definition, environment, _ := vmFactoryInputs(t, "linux/amd64", execution.CapabilityPreferred)
	root := catalog.Component{
		Platforms: []string{"linux/amd64", "linux/arm64"}, FromSource: true,
		BuilderType: catalog.BuilderVMImage, OutputType: authored.OutputDiskImage,
		Disk: &catalog.DiskSpec{Format: "qcow2", Encryption: "none"},
		BuildRequirements: &execution.Requirements{
			Resources: &execution.ResourceRequest{
				CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 32768,
			},
			AllowedHostPlatforms: []execution.HostPlatform{
				execution.HostPlatformLinuxAMD64, execution.HostPlatformLinuxARM64,
			},
			Capabilities: []execution.CapabilityRequirement{{
				Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred,
			}},
			TimeoutSeconds: 7200,
		},
		Repo: "https://example.test/image", Tag: "v1.0.0",
		Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImages: map[string]string{
			"amd64": "example.test/vm-amd64@sha256:" + strings.Repeat("c", 64),
			"arm64": "example.test/vm-arm64@sha256:" + strings.Repeat("d", 64),
		},
		Packer: &catalog.PackerConfig{
			ProjectPath: "os-image/packer", PackerVersion: "1.14.2", BuildName: "image.qemu.node",
			ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator", MaterialManifestVariable: "material_manifest",
			OutputDirectoryVariable: "output_directory",
			Variables:               map[string]string{"image_variant": "standard"},
		},
		Materials:    map[string]string{"agent-package": "agent"},
		ArtifactPath: "out/node-amd64.qcow2", SourceDate: 1,
	}
	agent := catalog.Component{
		Platforms: []string{"linux/amd64", "linux/arm64"}, FromSource: true,
		Repo: "https://example.test/agent", Tag: "v1.0.0",
		Commit: strings.Repeat("e", 40), TreeHash: strings.Repeat("f", 40),
		ToolchainImage: "example.test/go@sha256:" + strings.Repeat("1", 64),
		BuildCommand:   []string{"go", "build", "-o", "out/agent", "./cmd/agent"},
		WriteDir:       "out", OutputType: "raw", ArtifactPath: "out/agent", SourceDate: 1,
	}
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{
		"agent": agent, "image": root,
	}})
	if err != nil {
		t.Fatal(err)
	}
	definition.Outputs[0].Components = []string{"agent", "image"}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}

	var build ir.Node
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.ID == "build-image" {
			build = node
		}
	}
	if build.Packer == nil || build.Packer.Variables[0].Name != "image_variant" ||
		len(build.Materials) != 1 || build.Materials[0] != (ir.MaterialBinding{
		Binding: "agent-package", Component: "agent", Producer: "build-agent",
	}) || !contains(build.Needs, "build-agent") {
		t.Fatalf("Packer material contract was not lowered exactly: %#v", build)
	}
	var rootNode Node
	for _, node := range plan.Graph.Nodes {
		if node.ID == "node-image/build-image" {
			rootNode = node
		}
	}
	if rootNode.ID == "" {
		t.Fatal("Packer Plan omitted root build node")
	}
	for _, input := range []string{"artifact/node-image/agent", "result/node-image/agent"} {
		if !contains(rootNode.Inputs, input) {
			t.Fatalf("Packer Plan node omits exact material input %q: %#v", input, rootNode.Inputs)
		}
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCompileDiskPublicationAndCrossArchitectureRequiredKVMFailClosed(t *testing.T) {
	definition, environment, stack := vmFactoryInputs(t, "linux/arm64", execution.CapabilityRequired)
	environment.Publication = &authored.Publication{RepositoryPrefix: "example.test/nkd", Anonymous: true}
	if _, err := Compile(context.Background(), definition, environment, stack, "test"); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatalf("disk publication error = %v, want %s", err, e.CodeEngineCapability)
	}

	_, environment, stack = vmFactoryInputs(t, "linux/arm64", execution.CapabilityRequired)
	environment.ExecutionClasses = environment.ExecutionClasses[:1]
	if _, err := Compile(context.Background(), definition, environment, stack, "test"); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatalf("cross-architecture required-KVM error = %v, want %s", err, e.CodeEngineCapability)
	}
}

func TestVMPlanDataflowCarriesMaterialBytesAndProducerResult(t *testing.T) {
	graph := &ir.Graph{SchemaVersion: ir.SchemaVersion, Platform: "linux/amd64", Nodes: []ir.Node{
		{ID: "build-agent", Kind: ir.KindBuild, Component: "agent", OutputType: "raw", ArtifactPath: "out/agent", WriteDir: "out"},
		{ID: "build-image", Kind: ir.KindBuild, Component: "image", OutputType: authored.OutputDiskImage,
			ArtifactPath: "node.qcow2", Needs: []string{"build-agent", "fetch-image"}},
		{ID: "fetch-image", Kind: ir.KindFetch, Dest: "image-source"},
	}}
	nodes := map[string]Node{}
	artifacts := map[string]Artifact{}
	target := authored.DefinitionOutput{Name: "node-image", Format: authored.OutputDiskImage,
		Components: []string{"agent", "image"}}
	if err := addBuildDataflow(target, "runner", graph, map[string]bool{"agent": true, "image": true},
		fixtureUtilityContainer, "linux/amd64", nodes, artifacts); err != nil {
		t.Fatal(err)
	}
	root := nodes["node-image/build-image"]
	for _, input := range []string{
		"source/node-image/fetch-image", "source-receipt/node-image/fetch-image",
		"artifact/node-image/agent", "result/node-image/agent",
	} {
		if !contains(root.Inputs, input) {
			t.Fatalf("VM Plan node omits exact input %q: %#v", input, root.Inputs)
		}
	}
}

func factoryErrorCode(err error) e.Code {
	var coded *e.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}
