package lifecycle

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestMaintainedNKEOSARM64ExecutionModesShareArchitectureRenderedGraph(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repositoryRoot)
	for _, test := range []struct {
		name        string
		definition  string
		environment string
		wantKVM     bool
		wantCPU     string
	}{
		{name: "native KVM", definition: "definition-arm64-kvm.yaml",
			environment: "environment-local-linux-arm64-docker.yaml", wantKVM: true},
		{name: "macOS Podman TCG", definition: "definition-arm64-tcg.yaml",
			environment: "environment-local-macos-apple-m4-pro-podman.yaml", wantCPU: "cortex-a57"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join("definitions", "nke-os")
			plan, err := CompileFiles(t.Context(), filepath.Join(root, test.definition),
				filepath.Join(root, test.environment), "test")
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Builds) != 1 || plan.Builds[0].Graph == nil {
				t.Fatalf("NKE OS plan has %d builds, want one complete build", len(plan.Builds))
			}
			var producer ir.Node
			for _, node := range plan.Builds[0].Graph.Nodes {
				if node.Kind == ir.KindFetch && strings.Contains(strings.Join(node.VendorCmd, "\n"), "{{.Arch}}") {
					t.Fatalf("fetch node %q retained an architecture placeholder", node.ID)
				}
				if node.Component == "nke-node-image" {
					producer = node
				}
			}
			if producer.Packer == nil || producer.ExecutionBinding == nil {
				t.Fatal("NKE node-image producer lost Packer or execution binding")
			}
			if producer.ToolchainPlatform != "linux/arm64" {
				t.Fatalf("toolchain platform = %q, want linux/arm64", producer.ToolchainPlatform)
			}
			if len(producer.ExecutionBinding.Capabilities) != 1 ||
				producer.ExecutionBinding.Capabilities[0].Name != execution.CapabilityKVM ||
				producer.ExecutionBinding.Capabilities[0].Enabled != test.wantKVM {
				t.Fatalf("KVM resolution = %+v, want enabled=%t",
					producer.ExecutionBinding.Capabilities, test.wantKVM)
			}
			variables := make(map[string]string, len(producer.Packer.Variables))
			for _, variable := range producer.Packer.Variables {
				variables[variable.Name] = variable.Value
			}
			if got := variables["cpu_model"]; got != test.wantCPU {
				t.Fatalf("cpu_model = %q, want %q", got, test.wantCPU)
			}
			if !slices.Equal(plan.Builds[0].Components, []string{
				"nke-agent", "nke-cni-calico", "nke-cni-ovn", "nke-image-inputs", "nke-kubeadm-distro",
				"nke-node-image", "nke-patched-installer-iso", "nke-spire-agent", "ovn-kube-ubuntu",
				"ovn-kubernetes-chart", "ubuntu-installer-iso",
			}) {
				t.Fatalf("selected component graph = %v", plan.Builds[0].Components)
			}
		})
	}
}
