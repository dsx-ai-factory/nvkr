package ir

import (
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
)

func vmGraph(t *testing.T) Graph {
	t.Helper()
	commit := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 40)
	toolchain := "example.test/vm-toolchain@sha256:" + strings.Repeat("c", 64)
	vendorSpec, err := VendorSpecIdentity(nil, toolchain)
	if err != nil {
		t.Fatal(err)
	}
	return Graph{
		SchemaVersion: SchemaVersion,
		Platform:      "linux/arm64",
		Nodes: []Node{
			{
				ID: "build-image", Kind: KindBuild,
				Needs:        []string{"fetch-image", "pull-agent"},
				Requirements: vmRequirements(execution.CapabilityPreferred),
				Component:    "image", BuilderType: BuilderVMImage,
				Toolchain: toolchain, ToolchainPlatform: "linux/amd64",
				Platform: "linux/arm64", Network: NetworkNone,
				Workdir: "/src", SourceDest: "source-image", SourceCommit: commit,
				SourceTreeHash: tree, VendorSpecDigest: vendorSpec,
				Disk: &DiskSpec{Format: "qcow2", Encryption: "none"},
				Packer: &PackerConfig{
					ProjectPath: "os-image/packer", BuildName: "image.qemu.node",
					ArchitectureVariable: "architecture", MaterialManifestVariable: "material_manifest",
					OutputDirectoryVariable: "output_directory",
					Variables:               []PackerVariable{{Name: "image_variant", Value: "standard"}},
				},
				Materials:  []MaterialBinding{{Binding: "agent-package", Component: "agent", Producer: "pull-agent"}},
				OutputType: "disk-image", ArtifactPath: "node.qcow2", Version: "1.0.0",
			},
			{ID: "fetch-image", Kind: KindFetch, Repo: "https://example.test/image", Commit: commit,
				TreeHash: tree, Dest: "source-image", VendorImage: toolchain,
				VendorSpecDigest: vendorSpec, Network: NetworkDefault},
			{ID: "pull-agent", Kind: KindPull, Component: "agent", Artifact: "https://example.test/agent",
				Digest: "sha256:" + strings.Repeat("d", 64), Platform: "linux/arm64", Network: NetworkDefault,
				OutputType: "raw", ArtifactPath: "agent", Version: "1.0.0"},
			{ID: "verify", Kind: KindVerify, Needs: []string{"build-image", "pull-agent"}},
		},
	}
}

func vmRequirements(policy execution.CapabilityPolicy) *execution.Requirements {
	return &execution.Requirements{
		Resources: &execution.ResourceRequest{
			CPUMillis: 4000, MemoryMiB: 8192, EphemeralStorageMiB: 32768,
		},
		AllowedHostPlatforms: []execution.HostPlatform{
			execution.HostPlatformLinuxAMD64, execution.HostPlatformLinuxARM64,
		},
		Capabilities:   []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: policy}},
		TimeoutSeconds: 7200,
	}
}

func TestVMGraphBindsExactPackerAndMaterialInputs(t *testing.T) {
	graph := vmGraph(t)
	if err := graph.Validate(); err != nil {
		t.Fatal(err)
	}
	base, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*Node)
	}{
		{name: "disk encryption", mutate: func(node *Node) { node.Disk.Encryption = "luks2" }},
		{name: "Packer variable", mutate: func(node *Node) { node.Packer.Variables[0].Value = "minimal" }},
		{name: "material binding", mutate: func(node *Node) { node.Materials[0].Binding = "agent-deb" }},
		{name: "toolchain platform", mutate: func(node *Node) { node.ToolchainPlatform = "linux/arm64" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := vmGraph(t)
			test.mutate(&changed.Nodes[0])
			digest, digestErr := changed.Digest()
			if digestErr != nil {
				t.Fatal(digestErr)
			}
			if digest == base {
				t.Fatalf("%s did not change VM build identity", test.name)
			}
		})
	}
}

func TestVMGraphRejectsMaterialEdgeSubstitution(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Graph)
		code   e.Code
	}{
		{name: "missing producer edge", code: e.CodeInvalidCatalog, mutate: func(graph *Graph) {
			graph.Nodes[0].Needs = []string{"fetch-image"}
		}},
		{name: "producer relabel", code: e.CodeInvalidCatalog, mutate: func(graph *Graph) {
			graph.Nodes[0].Materials[0].Component = "other"
		}},
		{name: "command material handoff", code: e.CodeEngineCapability, mutate: func(graph *Graph) {
			graph.Nodes[0].Command = []string{"make", "image"}
			graph.Nodes[0].Packer = nil
		}},
		{name: "missing requirements", code: e.CodeInvalidCatalog, mutate: func(graph *Graph) {
			graph.Nodes[0].Requirements = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph := vmGraph(t)
			test.mutate(&graph)
			assertIRCode(t, graph.Validate(), test.code)
		})
	}
}

func TestVMRequiredKVMRejectsCrossArchitectureBinding(t *testing.T) {
	graph := vmGraph(t)
	graph.Nodes[0].Requirements = vmRequirements(execution.CapabilityRequired)
	binding, err := execution.Match(graph.Nodes[0].Requirements, []execution.Class{{
		Name: "amd64-kvm", RunsOn: "linux-amd64-cpu16", HostPlatform: execution.HostPlatformLinuxAMD64,
		Resources:    execution.ResourceCapacity{CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536},
		Capabilities: []execution.CapabilityName{execution.CapabilityKVM}, MaxTimeoutSeconds: 10800,
	}}, execution.MatchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	graph.Nodes[0].ExecutionBinding = binding
	assertIRCode(t, graph.Validate(), e.CodeEngineCapability)
}
