package recipe

import (
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
)

// A category-model catalog (ADR-12): ONE `kubernetes` category recipe produces every
// binary under one identity, each component tagged with role affinity. FilterByRole
// then carves node SKUs WITHOUT rebuilding or changing the recipe identity.
func kubeCategoryCatalog() *catalog.Catalog {
	comp := func(roles ...string) catalog.Component {
		return catalog.Component{
			FromSource: true, Repo: "https://github.com/kubernetes/kubernetes",
			Commit: "70d3cc986aa8221cd1dfb1121852688902d3bf53", TreeHash: "8f82f106c65896250424161432791051e9f2cd99",
			ToolchainImage: "golang@sha256:59b8183301af6dc358c9258d7b2ab0ee1a9363618552334fb3b160d454cbda72",
			OutputType:     "host-package", BuildCommand: []string{"make"}, ArtifactPath: "out/artifact", WriteDir: "out",
			Tag: "v1.0.0", SourceDate: 1733939955, Roles: roles,
		}
	}
	return &catalog.Catalog{
		Recipes: []catalog.Recipe{{
			ID: "kubernetes", Criteria: criteria.Criteria{Category: "kubernetes"},
			Components: []string{"apiserver", "kubelet", "kubectl"},
		}},
		Components: map[string]catalog.Component{
			"apiserver": comp("control-plane"),
			"kubelet":   comp("worker"),
			"kubectl":   comp(), // role-agnostic: in every SKU
		},
	}
}

func TestCategoryOneIdentitySKUFilter(t *testing.T) {
	cat := kubeCategoryCatalog()
	if err := cat.Validate(); err != nil {
		t.Fatalf("category catalog must validate: %v", err)
	}
	res, err := Resolve(cat, criteria.Criteria{Category: "kubernetes", OS: "ubuntu", Arch: "amd64"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The kubernetes category produces EVERY binary under one identity.
	if len(res.Components) != 3 {
		t.Fatalf("category recipe must produce all 3 components, got %d", len(res.Components))
	}
	oneIdentity, err := mustCompile(t, res).Digest()
	if err != nil {
		t.Fatal(err)
	}

	// SKU filtering is composition — it does not change the recipe identity.
	cp, err := FilterByRole(res, "control-plane")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, c := range cp.Components {
		names[c.Name] = true
	}
	if !names["apiserver"] || !names["kubectl"] || names["kubelet"] {
		t.Fatalf("control-plane SKU = %v (want apiserver+kubectl, not kubelet)", names)
	}
	// A Node SKU is a view, not a recipe: it names its parent identity and never mints
	// its own. (The type system additionally forbids Compile(cp): NodeSKU is not *Resolved.)
	if cp.ParentRecipeID != res.RecipeID {
		t.Fatalf("SKU parent recipe id = %q, want %q", cp.ParentRecipeID, res.RecipeID)
	}
	wk, err := FilterByRole(res, "worker")
	if err != nil {
		t.Fatal(err)
	}
	names = map[string]bool{}
	for _, c := range wk.Components {
		names[c.Name] = true
	}
	if !names["kubelet"] || !names["kubectl"] || names["apiserver"] {
		t.Fatalf("worker SKU = %v (want kubelet+kubectl, not apiserver)", names)
	}
	// The category identity is unchanged by role filtering (role is composition, not
	// identity): the full-set recipe digest is what a SKU verifies against.
	if d, _ := mustCompile(t, res).Digest(); d != oneIdentity {
		t.Fatal("role filtering must not change the category recipe identity")
	}
}

func TestQualifiedRoleFilterFailsClosed(t *testing.T) {
	res := &Resolved{RecipeID: "kubernetes", Criteria: criteria.Criteria{Category: "kubernetes"}}
	if _, err := FilterByRole(res, "database"); err == nil {
		t.Fatal("unknown role must return a coded error")
	}
	if _, err := FilterByRole(nil, "worker"); err == nil {
		t.Fatal("nil resolved recipe must fail closed")
	}
	clusterLevel := &Resolved{RecipeID: "operations", Criteria: criteria.Criteria{Category: "operations"}}
	// Regression: a valid role on a non-node category must return the coded error rather
	// than a nil result the caller dereferences into an uncoded panic (the old
	// FilterByRole footgun). FilterByRole now has a single fail-closed signature.
	if _, err := FilterByRole(clusterLevel, "worker"); err == nil {
		t.Fatal("role filtering a cluster-level category must fail closed")
	}
}
