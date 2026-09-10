package recipe

import (
	"context"
	"slices"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestEnumerateLayeredCatalogResolvesEveryCategoryInFrame(t *testing.T) {
	cat, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/examples/layered-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Enumerate(cat, criteria.Criteria{
		K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(got))
	for _, item := range got {
		ids = append(ids, item.RecipeID)
		if item.RecipeDigest == "" || len(item.Components) == 0 {
			t.Fatalf("enumerated recipe is incomplete: %+v", item)
		}
	}
	want := []string{
		"kubernetes-1.32.0-ubuntu-amd64",
		"platform-services-1.32.0-ubuntu-amd64",
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("recipe ids = %v, want %v", ids, want)
	}
}

func TestEnumerateRequiresConcreteOpenAxisFrameAndRejectsCompositionAxes(t *testing.T) {
	cat, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/examples/layered-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	for name, frame := range map[string]criteria.Criteria{
		"k8s":  {OS: "ubuntu", Arch: "amd64"},
		"os":   {K8sVersion: "1.32.0", Arch: "amd64"},
		"arch": {K8sVersion: "1.32.0", OS: "ubuntu"},
		"role": {K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64", Role: "worker"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Enumerate(cat, frame, nil); code(err) != nkderr.CodeUsage {
				t.Fatalf("code = %q, want %q: %v", code(err), nkderr.CodeUsage, err)
			}
		})
	}
}

func TestEnumerateCanNarrowOneCategory(t *testing.T) {
	cat, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/examples/layered-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Enumerate(cat, criteria.Criteria{
		Category: "platform-services", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "arm64",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Criteria.Category != "platform-services" || got[0].Criteria.Arch != "arm64" {
		t.Fatalf("narrow enumeration = %+v", got)
	}
}

func TestEnumerateIncludesExactLayeredAICRRecipeWithoutFakeComponents(t *testing.T) {
	cat, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/examples/layered-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Enumerate(cat, criteria.Criteria{
		K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64",
	}, map[string]string{"service": "eks", "accelerator": "h100", "intent": "training"})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range got {
		if item.Criteria.Category != "accelerated-runtime" {
			continue
		}
		if item.AICR == nil || item.AICRProfileIdentity != "h100-eks-ubuntu-training" || len(item.Components) != 0 {
			t.Fatalf("activation-only AICR enumeration = %+v", item)
		}
		return
	}
	t.Fatalf("AICR recipe missing from enumeration: %+v", got)
}
