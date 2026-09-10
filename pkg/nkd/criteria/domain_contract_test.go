package criteria

import (
	"errors"
	"reflect"
	"sort"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestDomainListsAreOwnedSortedAndPartitioned(t *testing.T) {
	for _, tc := range []struct {
		name  string
		list  func() []string
		valid func(string) bool
		want  []string
	}{
		{"roles", Roles, ValidRole, []string{"control-plane", "gpu-worker", "worker"}},
		{"arches", Arches, ValidArch, []string{"amd64", "arm64"}},
		{"node categories", NodeCategories, IsNodeCategory, []string{"kubernetes", "os"}},
		{"cluster categories", ClusterCategories, IsClusterCategory, []string{"accelerated-runtime", "core-runtime", "operations", "platform-services"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.list()
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("domain = %v, want %v", got, tc.want)
			}
			for _, value := range got {
				if !tc.valid(value) {
					t.Fatalf("listed value rejected: %s", value)
				}
			}
			if tc.valid("unknown") || tc.valid("any") {
				t.Fatal("predicate accepted a non-member")
			}
			got[0] = "mutated"
			if !reflect.DeepEqual(tc.list(), tc.want) {
				t.Fatal("caller mutation changed domain")
			}
		})
	}
	union := append(NodeCategories(), ClusterCategories()...)
	sort.Strings(union)
	if !reflect.DeepEqual(Categories(), union) {
		t.Fatal("category lists do not form the complete domain")
	}
	for _, value := range union {
		if !ValidCategory(value) || IsNodeCategory(value) == IsClusterCategory(value) {
			t.Fatalf("category partition overlaps or omits %s", value)
		}
	}
}

func TestAICRTupleValidationRequiresConcreteSupportedValues(t *testing.T) {
	for index, name := range []string{"service", "accelerator", "os", "intent", "platform"} {
		for _, value := range []string{"", "unknown"} {
			t.Run(name+"="+value, func(t *testing.T) {
				tuple := []string{"eks", "h100", "ubuntu", "training", "kubeflow"}
				tuple[index] = value
				err := ValidateAICRValues(tuple[0], tuple[1], tuple[2], tuple[3], tuple[4])
				if index == 4 && value == "" {
					if err != nil {
						t.Fatal(err)
					}
					return
				}
				want := e.CodeUnsupported
				if value == "" {
					want = e.CodeInvalidCatalog
				}
				assertCriteriaCode(t, err, want)
			})
		}
	}
	if err := ValidateAICRValues("eks", "h100", "ubuntu", "training", "kubeflow"); err != nil {
		t.Fatal(err)
	}
}

func TestReservedAxesRemainExplicitAndDeterministic(t *testing.T) {
	reserved := map[string]string{"service": "eks", "accelerator": "h100", "platform": "", "tier": "gold", "compliance": "fips"}
	if got := AICRAxesSet(reserved); !reflect.DeepEqual(got, []string{"accelerator", "service"}) {
		t.Fatalf("AICR axes = %v", got)
	}
	assertCriteriaCode(t, CheckHardReserved(reserved), e.CodeReservedAxis)
	for _, axes := range []map[string]string{nil, {}, {"tier": ""}, {"service": "eks", "accelerator": "h100"}} {
		if err := CheckHardReserved(axes); err != nil {
			t.Fatalf("inactive or AICR axis rejected: %v", err)
		}
	}
	if err := CheckReserved(nil); err != nil {
		t.Fatal(err)
	}
	assertCriteriaCode(t, CheckReserved(map[string]string{"service": "eks"}), e.CodeReservedAxis)
}

func TestCriteriaResolutionUsesRecipeFallbackAndQueryComposition(t *testing.T) {
	query := Criteria{Category: "any", K8sVersion: "", OS: "any", Arch: "", Role: "worker", ClusterType: "tenant"}
	pattern := Criteria{Category: "os", K8sVersion: "1.32.0", OS: "talos", Arch: "arm64"}
	want := pattern
	want.Role, want.ClusterType = "worker", "tenant"
	if got := Resolve(query, pattern); got != want {
		t.Fatalf("resolved = %+v, want %+v", got, want)
	}
	for _, bad := range []Criteria{{Category: "unknown"}, {Role: "unknown"}, {Arch: "unknown"}, {ClusterType: "unknown"}} {
		assertCriteriaCode(t, bad.ValidateActive(), e.CodeUnsupported)
		assertCriteriaCode(t, bad.ValidateRecipePattern(), e.CodeUnsupported)
	}
	if err := pattern.ValidateRecipePattern(); err != nil {
		t.Fatal(err)
	}
}

func assertCriteriaCode(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error = %v, want %s", err, want)
	}
}
