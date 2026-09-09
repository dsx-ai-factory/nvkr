package recipe

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/aicr"
	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/compose"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
)

// ADR-12 assigns category to recipe selection and role to SKU composition. Keep the
// checked-in demonstrator on that contract: one category identity is resolved first,
// then its component role affinities carve the control-plane delivery set.
func TestLayeredCatalogUsesCategoryThenRoleComposition(t *testing.T) {
	cat, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/examples/layered-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := Resolve(cat, criteria.Criteria{
		Category:   "kubernetes",
		Role:       "control-plane",
		K8sVersion: "1.32.0",
		OS:         "ubuntu",
		Arch:       "amd64",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.RecipeID != "kubernetes-1.32.0-ubuntu-amd64" {
		t.Fatalf("recipe id = %q", resolved.RecipeID)
	}
	sku, err := FilterByRole(resolved, "control-plane")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(sku.Components))
	for _, component := range sku.Components {
		names = append(names, component.Name)
	}
	want := []string{"kube-apiserver", "kube-controller-manager", "kube-proxy", "kubectl", "kubelet", "pause"}
	if !slices.Equal(names, want) {
		t.Fatalf("control-plane components = %v, want %v", names, want)
	}
}

func TestLayeredBaseDoesNotResolveAnUndeclaredCategory(t *testing.T) {
	cat, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/examples/layered-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Resolve(cat, criteria.Criteria{
		Category: "operations", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64",
	}, nil)
	if code(err) != "NKD-E-NO-MATCH" {
		t.Fatalf("undeclared category resolved through the abstract base: %v", err)
	}
}

func TestLayeredCategoryInferenceRequiresExactlyOneDeclaredCategory(t *testing.T) {
	defaultCatalog := layeredKubernetesFixture(t)
	resolved, err := Resolve(defaultCatalog, criteria.Criteria{
		K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Criteria.Category != "kubernetes" {
		t.Fatalf("single inferred category = %q", resolved.Criteria.Category)
	}

	multiple, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/examples/layered-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Resolve(multiple, criteria.Criteria{
		K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64",
	}, nil)
	if code(err) != "NKD-E-AMBIGUOUS" {
		t.Fatalf("omitted category in multi-category catalog did not fail ambiguous: %v", err)
	}
}

func TestResolveLayeredAICRProfileCarriesActivationIntoRecipeIdentity(t *testing.T) {
	profile := &aicr.Profile{
		Identity:        "h100-eks-ubuntu-training",
		Digest:          "sha256:" + strings.Repeat("a", 64),
		GeneratorDigest: "sha256:" + strings.Repeat("b", 64),
		Service:         "eks", Accelerator: "h100", OS: "ubuntu", Intent: "training",
	}
	base := &compose.RecipeMetadata{Kind: compose.RecipeMetadataKind, APIVersion: compose.APIVersion}
	base.Metadata.Name = "base"
	overlay := &compose.RecipeMetadata{Kind: compose.RecipeMetadataKind, APIVersion: compose.APIVersion}
	overlay.Metadata.Name = "accelerated-h100"
	overlay.Spec.Criteria = &criteria.Criteria{Category: "accelerated-runtime", OS: "ubuntu"}
	overlay.Spec.AICRProfile = profile
	cat := &catalog.Catalog{Layers: &compose.Store{Base: base, Overlays: map[string]*compose.RecipeMetadata{
		"accelerated-h100": overlay,
	}}, Components: map[string]catalog.Component{}}

	resolved, err := Resolve(cat, criteria.Criteria{
		Category: "accelerated-runtime", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64",
	}, map[string]string{"service": "eks", "accelerator": "h100", "intent": "training"})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if graph.AICR == nil || graph.AICR.RecipeDigest != profile.Digest || graph.AICR.GeneratorDigest != profile.GeneratorDigest {
		t.Fatalf("compiled layered activation = %+v", graph.AICR)
	}
	if _, err := Resolve(cat, resolved.Criteria, nil); code(err) != "NKD-E-NO-MATCH" {
		t.Fatalf("layered AICR recipe resolved without exact selection: %v", err)
	}
}
