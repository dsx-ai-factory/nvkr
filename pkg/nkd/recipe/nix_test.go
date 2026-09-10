package recipe

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// nixCatalog is a single-component from-source recipe whose component is built with the
// nix builder (ADR-10): a locked flake in a pinned nix container.
func nixCatalog(builder string) *catalog.Catalog {
	comp := catalog.Component{
		FromSource: true, BuilderType: builder,
		Repo: "https://github.com/example/tool", Commit: strings.Repeat("e", 40),
		TreeHash: strings.Repeat("f", 40), Tag: "v1.0.0", SourceDate: 1700000000,
		ToolchainImage: "golang@sha256:" + strings.Repeat("a", 64),
		BuildCommand:   []string{"nix", "build", ".#default"},
		ArtifactPath:   "out/tool", WriteDir: "out", OutputType: "host-package",
	}
	if builder == ir.BuilderNix {
		comp.Flake = &catalog.FlakePin{
			Ref:        "github:example/tool?rev=" + strings.Repeat("e", 40),
			LockDigest: "sha256:" + strings.Repeat("b", 64),
			Output:     "packages.x86_64-linux.default",
			Image:      "nixos/nix@sha256:" + strings.Repeat("c", 64),
		}
	}
	return &catalog.Catalog{
		Recipes:    []catalog.Recipe{{ID: "n", Criteria: criteria.Criteria{Category: "kubernetes"}, Components: []string{"tool"}}},
		Components: map[string]catalog.Component{"tool": comp},
	}
}

// ADR-10 §2/§4: a nix component compiles to a build node carrying the locked-flake
// pins, and the pinned nix image runs the build (Flake.Image overrides the toolchain).
func TestCompileNixBuildNode(t *testing.T) {
	res, err := Resolve(nixCatalog(ir.BuilderNix), criteria.Criteria{Category: "kubernetes", Role: "worker", Arch: "amd64"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := mustCompile(t, res)
	var b *ir.Node
	for i := range g.Nodes {
		if g.Nodes[i].Kind == ir.KindBuild {
			b = &g.Nodes[i]
		}
	}
	if b == nil {
		t.Fatal("no build node emitted")
	}
	if b.BuilderType != ir.BuilderNix {
		t.Errorf("builderType = %q, want nix", b.BuilderType)
	}
	if b.FlakeLockDigest == "" || b.FlakeRef == "" || b.FlakeOutput == "" {
		t.Errorf("nix flake pins not carried onto the IR: %+v", b)
	}
	if !strings.HasPrefix(b.Toolchain, "nixos/nix@sha256:") {
		t.Errorf("pinned nix image should run the build, got toolchain %q", b.Toolchain)
	}
	if b.Network != ir.NetworkNone {
		t.Errorf("nix build must be offline (network=none), got %q", b.Network)
	}
}

// ADR-10 §4: switching a component's builder type changes recipeDigest — a builder
// switch is a NEW recipe, never a variant. The flake pins are build-semantic inputs.
func TestNixSwitchChangesRecipeDigest(t *testing.T) {
	q := criteria.Criteria{Category: "kubernetes", Role: "worker", Arch: "amd64"}
	ociRes, err := Resolve(nixCatalog(ir.BuilderOCIHermetic), q, nil)
	if err != nil {
		t.Fatal(err)
	}
	nixRes, err := Resolve(nixCatalog(ir.BuilderNix), q, nil)
	if err != nil {
		t.Fatal(err)
	}
	ociDig, err := mustCompile(t, ociRes).Digest()
	if err != nil {
		t.Fatal(err)
	}
	nixDig, err := mustCompile(t, nixRes).Digest()
	if err != nil {
		t.Fatal(err)
	}
	if ociDig == nixDig {
		t.Fatalf("oci-hermetic and nix builds must have distinct recipeDigests, both %s", nixDig)
	}
}
