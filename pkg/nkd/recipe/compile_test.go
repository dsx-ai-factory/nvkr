package recipe

import (
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func mustCompile(t testing.TB, resolved *Resolved) *ir.Graph {
	t.Helper()
	graph, err := Compile(resolved)
	if err != nil {
		t.Fatalf("compile recipe: %v", err)
	}
	return graph
}

func TestCompileRejectsUnimplementedDiskCapabilityBeforeGraphConstruction(t *testing.T) {
	validVM := catalog.Component{
		FromSource: true, BuilderType: catalog.BuilderVMImage, OutputType: "disk-image",
		Disk: &catalog.DiskSpec{Format: "qcow2", Encryption: "none"},
		VMImage: &catalog.VMImageRequirements{
			AllowedHostArchs: []string{"amd64"}, MinHostCPUs: 4, MinHostMemoryMiB: 8192,
			MinScratchMiB: 32768, MinOutputMiB: 12288, KVM: catalog.VMKVMOptional,
			Network: catalog.VMNetworkDefault, TimeoutSeconds: 10800,
		},
		Repo: testVMRepo, Commit: testVMCommit, TreeHash: testVMTree,
		ToolchainImage: testVMToolchain,
		BuildCommand:   []string{"make", "-C", "os-image/packer", "os-image-non-fde", "ARCH={{.Arch}}"},
		ArtifactPath:   "os-image/packer/output-non-fde/nke-node-non-fde-ubuntu-26.04.1-{{.Arch}}.qcow2",
		SourceDate:     1733939955,
	}
	tests := []struct {
		name      string
		component catalog.Component
	}{
		{name: "disk output from non-VM builder", component: catalog.Component{FromSource: true, OutputType: "disk-image"}},
		{name: "VM builder", component: catalog.Component{FromSource: true, BuilderType: catalog.BuilderVMImage}},
		{name: "valid disk output from VM builder", component: validVM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graph, err := Compile(&Resolved{RecipeID: "disk", Components: []catalog.NamedComponent{{Name: "image", Component: tt.component}}})
			if coded, ok := err.(*nkderr.Error); !ok || coded.Code != nkderr.CodeEngineCapability {
				t.Fatalf("error = %v, want %s", err, nkderr.CodeEngineCapability)
			}
			if graph != nil {
				t.Fatalf("unsupported disk capability produced an executable graph: %#v", graph)
			}
		})
	}
}

func TestCompileSkipsExcludedDiskCapability(t *testing.T) {
	graph, err := Compile(&Resolved{RecipeID: "disk", Criteria: criteria.Criteria{
		Category: "os",
		OS:       "ubuntu",
		Arch:     "amd64",
	}, Components: []catalog.NamedComponent{{
		Name: "image",
		Component: catalog.Component{
			OutputType:      "disk-image",
			ExclusionReason: "not built",
		},
	}}})
	if err != nil {
		t.Fatalf("compile excluded disk recipe: %v", err)
	}
	if len(graph.Nodes) != 1 || graph.Nodes[0].Kind != ir.KindVerify || len(graph.Nodes[0].Needs) != 0 {
		t.Fatalf("excluded disk emitted executable nodes: %#v", graph.Nodes)
	}
}
