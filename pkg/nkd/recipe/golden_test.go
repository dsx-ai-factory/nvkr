package recipe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
)

// goldenCase pins one resolved recipe's identity + component set.
type goldenCase struct {
	Query        criteria.Criteria `json:"query"`
	RecipeID     string            `json:"recipeId"`
	RecipeDigest string            `json:"recipeDigest"`
	Components   []string          `json:"components"`
}

// TestGoldenRecipeIdentities is the ADR-11 §2 golden-fixture gate: it pins the composed
// recipe identity for the explicit layered Kubernetes fixture across criteria combinations. A bare
// `go test` COMPARES and fails closed on unintended drift; the documented regen path is
// `NKD_UPDATE_GOLDEN=1 go test ./pkg/nkd/recipe/`, which rewrites the fixture (a
// reviewable diff paired with the change that moved the identity). Any merge-algebra,
// codec, or pin change surfaces here as a fixture diff.
func TestGoldenRecipeIdentities(t *testing.T) {
	cat := layeredKubernetesFixture(t)
	// ADR-12: the recipe axis is `category`; the kubernetes category is ONE identity per
	// (k8sVersion, os, arch). Role is a delivery SKU filter and does NOT change identity.
	queries := []criteria.Criteria{
		{Category: "kubernetes", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"},
		{Category: "kubernetes", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "arm64"},
	}
	var got []goldenCase
	for _, q := range queries {
		res, err := Resolve(cat, q, nil)
		if err != nil {
			t.Fatalf("resolve %+v: %v", q, err)
		}
		digest, err := mustCompile(t, res).Digest()
		if err != nil {
			t.Fatal(err)
		}
		names := make([]string, 0, len(res.Components))
		for _, c := range res.Components {
			names = append(names, c.Name)
		}
		sort.Strings(names)
		got = append(got, goldenCase{Query: q, RecipeID: res.RecipeID, RecipeDigest: digest, Components: names})
	}

	// Role is composition, not identity: adding --role must NOT change the recipeDigest.
	base, _ := mustCompile(t, mustResolve(t, cat, criteria.Criteria{Category: "kubernetes", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"})).Digest()
	for _, role := range []string{"control-plane", "worker"} {
		d, _ := mustCompile(t, mustResolve(t, cat, criteria.Criteria{Category: "kubernetes", Role: role, K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"})).Digest()
		if d != base {
			t.Fatalf("role %q changed the recipe identity (%s != %s) — role must be composition, not identity", role, d, base)
		}
	}

	path := filepath.Join("testdata", "golden_recipes.json")
	gotBytes, _ := json.MarshalIndent(got, "", "  ")
	if os.Getenv("NKD_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(gotBytes, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Log("regenerated golden fixture; commit the diff")
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden fixture missing (regen: NKD_UPDATE_GOLDEN=1 go test ./pkg/nkd/recipe/): %v", err)
	}
	if string(append(gotBytes, '\n')) != string(want) {
		t.Errorf("composed recipe identities drifted from the golden fixture.\nIf intended, regen: NKD_UPDATE_GOLDEN=1 go test ./pkg/nkd/recipe/\n--- got ---\n%s", gotBytes)
	}
}

func mustResolve(t *testing.T, cat *catalog.Catalog, q criteria.Criteria) *Resolved {
	t.Helper()
	res, err := Resolve(cat, q, nil)
	if err != nil {
		t.Fatalf("resolve %+v: %v", q, err)
	}
	return res
}
