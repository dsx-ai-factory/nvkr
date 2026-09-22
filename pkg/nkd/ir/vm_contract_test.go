package ir

import (
	"slices"
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
					ProjectPath: "os-image/packer", PackerVersion: "1.14.2", BuildName: "image.qemu.node",
					ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator", MaterialManifestVariable: "material_manifest",
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
		{name: "Packer version", mutate: func(node *Node) { node.Packer.PackerVersion = "1.14.3" }},
		{name: "Packer build name", mutate: func(node *Node) { node.Packer.BuildName = "image.qemu.other" }},
		{name: "accelerator variable", mutate: func(node *Node) { node.Packer.AcceleratorVariable = "vm_accelerator" }},
		{name: "SSH variable pair", mutate: func(node *Node) {
			node.Packer.SSHPrivateKeyVariable = "ssh_private_key_file"
			node.Packer.SSHPublicKeyVariable = "ssh_public_key_file"
		}},
		{name: "build-log directory variable", mutate: func(node *Node) { node.Packer.BuildLogDirectoryVariable = "build_log_directory" }},
		{name: "HTTP directory variable", mutate: func(node *Node) { node.Packer.HTTPDirectoryVariable = "http_directory" }},
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

func TestVMGraphMaterialProducerAffectsBuildIdentity(t *testing.T) {
	base := vmGraph(t)
	want, err := base.Digest()
	if err != nil {
		t.Fatal(err)
	}
	changed := vmGraph(t)
	changed.Nodes[0].Materials[0].Component = "agent-v2"
	changed.Nodes[0].Materials[0].Producer = "pull-agent-v2"
	changed.Nodes[0].Needs[1] = "pull-agent-v2"
	changed.Nodes[2].ID = "pull-agent-v2"
	changed.Nodes[2].Component = "agent-v2"
	changed.Nodes[3].Needs[1] = "pull-agent-v2"
	changed.Normalize()
	got, err := changed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if got == want {
		t.Fatal("changed material producer retained VM build identity")
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
	assertIRCode(t, graph.Validate(), e.CodeEngineCapability)

	graph = vmGraph(t)
	graph.Nodes[0].Requirements = vmRequirements(execution.CapabilityRequired)
	graph.Nodes[0].ToolchainPlatform = "linux/arm64"
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

func TestVMGraphRejectsInvalidVMContracts(t *testing.T) {
	armBinding := func(t *testing.T, requirements *execution.Requirements) *execution.Binding {
		t.Helper()
		binding, err := execution.Match(requirements, []execution.Class{{
			Name: "arm64-kvm", RunsOn: "linux-arm64-cpu16", HostPlatform: execution.HostPlatformLinuxARM64,
			Resources:    execution.ResourceCapacity{CPUMillis: 8000, MemoryMiB: 16384, EphemeralStorageMiB: 65536},
			Capabilities: []execution.CapabilityName{execution.CapabilityKVM}, MaxTimeoutSeconds: 10800,
		}}, execution.MatchOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return binding
	}
	for _, test := range []struct {
		name   string
		code   e.Code
		mutate func(*Graph)
	}{
		{name: "unexpected field", code: e.CodeInvalidCatalog, mutate: func(graph *Graph) {
			graph.Nodes[0].Env = []EnvVar{{Name: "UNEXPECTED", Value: "value"}}
		}},
		{name: "toolchain host not allowed", code: e.CodeInvalidCatalog, mutate: func(graph *Graph) {
			graph.Nodes[0].Requirements.AllowedHostPlatforms = []execution.HostPlatform{execution.HostPlatformLinuxARM64}
		}},
		{name: "binding differs from toolchain host", code: e.CodeEngineCapability, mutate: func(graph *Graph) {
			graph.Nodes[0].ExecutionBinding = armBinding(t, graph.Nodes[0].Requirements)
		}},
		{name: "invalid encryption", code: e.CodeInvalidCatalog, mutate: func(graph *Graph) {
			graph.Nodes[0].Disk.Encryption = "aes"
		}},
		{name: "network enabled", code: e.CodeInvalidCatalog, mutate: func(graph *Graph) {
			graph.Nodes[0].Network = NetworkDefault
		}},
		{name: "both command and Packer", code: e.CodeInvalidCatalog, mutate: func(graph *Graph) {
			graph.Nodes[0].Command = []string{"make", "image"}
		}},
		{name: "no producer", code: e.CodeInvalidCatalog, mutate: func(graph *Graph) {
			graph.Nodes[0].Packer = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph := vmGraph(t)
			test.mutate(&graph)
			assertIRCode(t, graph.Validate(), test.code)
		})
	}
}

func TestVMGraphRejectsInvalidPackerAndMaterialBindings(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Node)
	}{
		{name: "unsafe Packer project", mutate: func(node *Node) { node.Packer.ProjectPath = "../packer" }},
		{name: "glob Packer build", mutate: func(node *Node) { node.Packer.BuildName = "image.*" }},
		{name: "unsorted Packer variables", mutate: func(node *Node) {
			node.Packer.Variables = append(node.Packer.Variables, PackerVariable{Name: "alpha", Value: "value"})
		}},
		{name: "unsafe material binding", mutate: func(node *Node) { node.Materials[0].Binding = "bad\nname" }},
		{name: "unsorted material bindings", mutate: func(node *Node) {
			node.Materials = []MaterialBinding{
				{Binding: "zeta", Component: "zeta", Producer: "pull-zeta"},
				{Binding: "alpha", Component: "alpha", Producer: "pull-alpha"},
			}
		}},
		{name: "duplicate material component", mutate: func(node *Node) {
			node.Materials = append(node.Materials,
				MaterialBinding{Binding: "zeta", Component: "agent", Producer: "pull-zeta"})
		}},
		{name: "duplicate material producer", mutate: func(node *Node) {
			node.Materials = append(node.Materials,
				MaterialBinding{Binding: "zeta", Component: "zeta", Producer: "pull-agent"})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph := vmGraph(t)
			test.mutate(&graph.Nodes[0])
			assertIRCode(t, graph.Validate(), e.CodeInvalidCatalog)
		})
	}
}

func TestVMGraphNormalizeSortsPackerAndMaterialInputsDeterministically(t *testing.T) {
	makeGraph := func(reverse bool) Graph {
		graph := vmGraph(t)
		graph.Nodes[0].Materials = append(graph.Nodes[0].Materials,
			MaterialBinding{Binding: "tool-package", Component: "tool", Producer: "pull-tool"})
		graph.Nodes[0].Packer.Variables = append(graph.Nodes[0].Packer.Variables,
			PackerVariable{Name: "release_channel", Value: "stable"})
		graph.Nodes[0].Needs = append(graph.Nodes[0].Needs, "pull-tool")
		graph.Nodes = append(graph.Nodes, Node{ID: "pull-tool", Kind: KindPull, Component: "tool",
			Artifact: "https://example.test/tool", Digest: "sha256:" + strings.Repeat("e", 64),
			Platform: "linux/arm64", Network: NetworkDefault, OutputType: "raw",
			ArtifactPath: "tool", Version: "1.0.0"})
		graph.Nodes[3].Needs = append(graph.Nodes[3].Needs, "pull-tool")
		if reverse {
			slices.Reverse(graph.Nodes[0].Materials)
			slices.Reverse(graph.Nodes[0].Packer.Variables)
		}
		graph.Normalize()
		return graph
	}
	left, right := makeGraph(false), makeGraph(true)
	leftDigest, err := left.Digest()
	if err != nil {
		t.Fatal(err)
	}
	rightDigest, err := right.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if leftDigest != rightDigest {
		t.Fatalf("normalized input order changed VM build identity: %s != %s", leftDigest, rightDigest)
	}
}
