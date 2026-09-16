package recipe

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
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

func TestCompileCarriesGenericBuildRequirementsOutsideRecipeIdentity(t *testing.T) {
	component := catalog.Component{
		FromSource: true, Repo: "https://example.test/source", Tag: "v1.0.0",
		Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImage: "example.test/toolchain@sha256:" + strings.Repeat("c", 64),
		OutputType:     "raw", BuildCommand: []string{"make"}, WriteDir: "out",
		ArtifactPath: "out/artifact", SourceDate: 1,
	}
	resolved := func(c catalog.Component) *Resolved {
		return &Resolved{RecipeID: "generic-requirements",
			Criteria:   criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
			Components: []catalog.NamedComponent{{Name: "artifact", Component: c}}}
	}

	without := mustCompile(t, resolved(component))
	component.BuildRequirements = &execution.Requirements{
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
	with := mustCompile(t, resolved(component))
	if with.Nodes[0].Requirements == nil || !execution.Equal(with.Nodes[0].Requirements, component.BuildRequirements) {
		t.Fatalf("compiled build node lost generic requirements: %#v", with.Nodes[0])
	}
	withoutDigest, err := without.Digest()
	if err != nil {
		t.Fatal(err)
	}
	withDigest, err := with.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if withDigest != withoutDigest {
		t.Fatalf("operational request changed recipeDigest: %s != %s", withDigest, withoutDigest)
	}
}

func TestCompileRejectsUnimplementedDiskCapabilityBeforeGraphConstruction(t *testing.T) {
	validVM := catalog.Component{
		FromSource: true, BuilderType: catalog.BuilderVMImage, OutputType: "disk-image",
		Disk: &catalog.DiskSpec{Format: "qcow2", Encryption: "none"},
		BuildRequirements: &execution.Requirements{
			Resources: &execution.ResourceRequest{
				CPUMillis: 2000, MemoryMiB: 4096, EphemeralStorageMiB: 8192,
			},
			AllowedHostPlatforms: []execution.HostPlatform{execution.HostPlatformLinuxAMD64},
			Capabilities: []execution.CapabilityRequirement{{
				Name: execution.CapabilityKVM, Policy: execution.CapabilityPreferred,
			}},
			TimeoutSeconds: 7200,
		},
		Repo: "https://example.test/nke", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImage: "example.test/vm-builder@sha256:" + strings.Repeat("c", 64),
		BuildCommand:   []string{"make", "-C", "os-image/packer", "os-image-non-fde", "ARCH={{.Arch}}"},
		ArtifactPath:   "os-image/packer/output-non-fde/node-{{.Arch}}.qcow2",
		SourceDate:     1,
	}
	tests := []struct {
		name      string
		component catalog.Component
	}{
		{name: "disk output", component: catalog.Component{FromSource: true, OutputType: "disk-image"}},
		{name: "VM builder", component: catalog.Component{FromSource: true, BuilderType: catalog.BuilderVMImage}},
		{name: "complete VM disk producer", component: validVM},
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
