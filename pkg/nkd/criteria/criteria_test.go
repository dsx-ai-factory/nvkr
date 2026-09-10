package criteria

import (
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestAsymmetricMatch(t *testing.T) {
	tests := []struct {
		name   string
		recipe Criteria
		query  Criteria
		want   bool
	}{
		// NKD-FR-RC-002: recipe wildcard matches any query value, "" == "any".
		{"wildcard os matches specific", Criteria{OS: "any"}, Criteria{OS: "ubuntu"}, true},
		{"wildcard os matches empty", Criteria{OS: "any"}, Criteria{OS: ""}, true},
		{"empty recipe field is wildcard", Criteria{Category: "kubernetes"}, Criteria{Category: "kubernetes", OS: "ubuntu"}, true},
		// NKD-FR-RC-004: recipe constrains a field the query omits -> no match.
		{"specific recipe unmet by generic query", Criteria{Category: "kubernetes", OS: "ubuntu"}, Criteria{Category: "kubernetes"}, false},
		{"specific mismatch", Criteria{Category: "kubernetes"}, Criteria{Category: "os"}, false},
		{"role does not select recipes", Criteria{Category: "kubernetes"}, Criteria{Category: "kubernetes", Role: "worker"}, true},
		{"cluster type does not select recipes", Criteria{Category: "kubernetes"}, Criteria{Category: "kubernetes", ClusterType: "tenant"}, true},
	}
	for _, tc := range tests {
		if got := tc.recipe.Matches(tc.query); got != tc.want {
			t.Errorf("%s: Matches=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestSpecificity(t *testing.T) {
	generic := Criteria{Category: "kubernetes"}
	specific := Criteria{Category: "kubernetes", K8sVersion: "1.32.0", Arch: "amd64"}
	if generic.Specificity() >= specific.Specificity() {
		t.Fatalf("expected specific (%d) > generic (%d)", specific.Specificity(), generic.Specificity())
	}
	if (Criteria{OS: "any"}).Specificity() != 0 {
		t.Errorf("wildcard field must not count toward specificity")
	}
}

func TestCheckReservedFailsClosed(t *testing.T) {
	if err := CheckReserved(map[string]string{"compliance": "fips"}); err == nil {
		t.Fatal("expected reserved axis to fail closed")
	} else if ne, ok := err.(*nkderr.Error); !ok || ne.Code != nkderr.CodeReservedAxis {
		t.Fatalf("want CodeReservedAxis, got %v", err)
	}
	// empty reserved values are fine
	if err := CheckReserved(map[string]string{"compliance": ""}); err != nil {
		t.Errorf("empty reserved value must resolve normally: %v", err)
	}
}

func TestResolveDefaultsOS(t *testing.T) {
	// both query and recipe leave os wildcard -> resolved os must be the ubuntu
	// default, never an empty axis (NKD-FR-RC-009/019).
	got := Resolve(Criteria{Role: "control-plane"}, Criteria{Role: "control-plane", OS: "any"})
	if got.OS != DefaultOS {
		t.Fatalf("os must default to %q, got %q", DefaultOS, got.OS)
	}
	// an explicit query os is preserved.
	if Resolve(Criteria{OS: "debian"}, Criteria{OS: "any"}).OS != "debian" {
		t.Error("explicit query os must win over the default")
	}
}

func TestValidateActiveDomain(t *testing.T) {
	if err := (Criteria{Role: "edge"}).ValidateActive(); err == nil {
		t.Error("bad role must fail closed")
	}
	// arm64 is now a supported arch (multi-arch, model B).
	if err := (Criteria{Arch: "arm64"}).ValidateActive(); err != nil {
		t.Errorf("arm64 must be a valid arch: %v", err)
	}
	// an arch outside the domain still fails closed.
	if err := (Criteria{Arch: "riscv64"}).ValidateActive(); err == nil {
		t.Error("arch outside domain {amd64, arm64} must fail closed")
	}
	for _, a := range []string{"amd64", "arm64"} {
		if err := (Criteria{Role: "worker", Arch: a, OS: "debian"}).ValidateActive(); err != nil {
			t.Errorf("valid active axes + open os must pass for arch %s: %v", a, err)
		}
	}
}

// Role and clusterType are validated composition metadata, not recipe selectors.
// They survive query concretization, but authored recipe patterns must reject them so
// an author cannot believe they select a distinct build identity.
func TestCompositionAxesDoNotSelectRecipes(t *testing.T) {
	// domain
	if err := (Criteria{ClusterType: "operator"}).ValidateActive(); err != nil {
		t.Errorf("operator must be a valid clusterType: %v", err)
	}
	if err := (Criteria{ClusterType: "bogus"}).ValidateActive(); err == nil {
		t.Error("clusterType outside {operator, tenant} must fail closed")
	}
	if !(Criteria{Role: "worker", ClusterType: "tenant"}).Matches(Criteria{}) {
		t.Error("composition fields must not participate in recipe matching")
	}
	if got := (Criteria{Role: "control-plane", ClusterType: "operator"}).Specificity(); got != 0 {
		t.Errorf("composition fields must not add specificity, got %d", got)
	}
	for _, pattern := range []Criteria{{Role: "worker"}, {ClusterType: "tenant"}} {
		if err := pattern.ValidateRecipePattern(); err == nil {
			t.Errorf("authored recipe pattern %+v must fail closed", pattern)
		} else if ne, ok := err.(*nkderr.Error); !ok || ne.Code != nkderr.CodeInvalidCatalog {
			t.Errorf("want CodeInvalidCatalog for %+v, got %v", pattern, err)
		}
	}
	// never defaulted: resolving without clusterType leaves it empty (so recipeDigest is
	// unchanged for catalogs that don't use the axis).
	if got := Resolve(Criteria{Role: "worker"}, Criteria{Role: "worker"}); got.ClusterType != "" {
		t.Errorf("clusterType must not be defaulted, got %q", got.ClusterType)
	}
	got := Resolve(Criteria{Role: "gpu-worker", ClusterType: "tenant"}, Criteria{})
	if got.Role != "gpu-worker" || got.ClusterType != "tenant" {
		t.Fatalf("query composition metadata not preserved: %+v", got)
	}
}
