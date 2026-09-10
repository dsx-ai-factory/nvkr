package compose

import (
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/aicr"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func boolp(b bool) *bool { return &b }

func code(err error) nkderr.Code {
	if e, ok := err.(*nkderr.Error); ok {
		return e.Code
	}
	return ""
}

// meta builds a named overlay/base RecipeMetadata.
func meta(name, base string, crit *criteria.Criteria, spec Spec) *RecipeMetadata {
	spec.Base = base
	spec.Criteria = crit
	rm := &RecipeMetadata{Kind: RecipeMetadataKind, APIVersion: APIVersion}
	rm.Metadata.Name = name
	rm.Spec = spec
	return rm
}

func names(refs []ComponentRef) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = r.Name
	}
	return out
}

// A base + a single-parent chain (base ← mid ← leaf): the maximal leaf's whole chain is
// merged root→leaf, child-wins, and only the leaf is a candidate (mid is filtered as an
// ancestor). This is the core inheritance behavior.
func TestComposeInheritanceChainChildWins(t *testing.T) {
	base := meta("base", "", nil, Spec{
		ComponentRefs: []ComponentRef{
			{Name: "etcd", Overrides: map[string]any{"replicas": 1}},
			{Name: "proxy"},
		},
		Constraints: []Constraint{{Name: "k8s", Value: ">= 1.25"}},
	})
	mid := meta("worker", "base", &criteria.Criteria{Category: "kubernetes"}, Spec{
		ComponentRefs: []ComponentRef{
			{Name: "kubelet"},
			{Name: "etcd", Overrides: map[string]any{"replicas": 3, "tls": true}},
		},
	})
	leaf := meta("worker-ubuntu", "worker", &criteria.Criteria{Category: "kubernetes", OS: "ubuntu"}, Spec{
		ComponentRefs: []ComponentRef{
			{Name: "proxy", Enabled: boolp(false)}, // leaf disables a base component
			{Name: "etcd", Overrides: map[string]any{"replicas": 5}},
		},
	})
	st := &Store{Base: base, Overlays: map[string]*RecipeMetadata{"worker": mid, "worker-ubuntu": leaf}}
	if err := st.Validate(); err != nil {
		t.Fatal(err)
	}

	got, err := st.Compose(criteria.Criteria{Category: "kubernetes", OS: "ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	// proxy disabled; etcd + kubelet remain, sorted by name.
	if want := []string{"etcd", "kubelet"}; !reflect.DeepEqual(names(got.Components), want) {
		t.Fatalf("effective set = %v, want %v", names(got.Components), want)
	}
	// child (leaf) wins the deep-merged override: replicas 5, tls true inherited from mid.
	var etcd ComponentRef
	for _, c := range got.Components {
		if c.Name == "etcd" {
			etcd = c
		}
	}
	if etcd.Overrides["replicas"] != 5 || etcd.Overrides["tls"] != true {
		t.Fatalf("etcd overrides not child-wins-merged: %v", etcd.Overrides)
	}
	// applied layers = base + the leaf's chain (mid, leaf); mid merged once.
	if want := []string{"base", "worker", "worker-ubuntu"}; !reflect.DeepEqual(got.AppliedLayers, want) {
		t.Fatalf("appliedLayers = %v, want %v", got.AppliedLayers, want)
	}
}

// deepMergeMap: nested maps merge (src wins per-leaf), scalars/slices replace.
func TestDeepMerge(t *testing.T) {
	dst := map[string]any{"cfg": map[string]any{"a": 1, "b": 2}, "keep": "x"}
	src := map[string]any{"cfg": map[string]any{"b": 3, "c": 4}, "list": []any{1, 2}}
	deepMergeMap(dst, src)
	cfg := dst["cfg"].(map[string]any)
	if cfg["a"] != 1 || cfg["b"] != 3 || cfg["c"] != 4 || dst["keep"] != "x" {
		t.Fatalf("bad merge: %v", dst)
	}
}

// Orthogonal maximal leaves (category + os as separate overlays) are BOTH merged, ordered
// least-specific-first then by name — the "merge many" model (not pick-one).
func TestComposeMergesMultipleMaximalLeaves(t *testing.T) {
	base := meta("base", "", nil, Spec{ComponentRefs: []ComponentRef{{Name: "core"}}})
	worker := meta("worker", "base", &criteria.Criteria{Category: "kubernetes"}, Spec{ComponentRefs: []ComponentRef{{Name: "kubelet"}}})
	ubuntu := meta("ubuntu", "base", &criteria.Criteria{OS: "ubuntu"}, Spec{ComponentRefs: []ComponentRef{{Name: "ubuntu-tweak"}}})
	st := &Store{Base: base, Overlays: map[string]*RecipeMetadata{"worker": worker, "ubuntu": ubuntu}}

	got, err := st.Compose(criteria.Criteria{Category: "kubernetes", OS: "ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"core", "kubelet", "ubuntu-tweak"}; !reflect.DeepEqual(names(got.Components), want) {
		t.Fatalf("effective set = %v, want %v", names(got.Components), want)
	}
	// both spec-1 leaves applied, ordered by name (ubuntu before worker), base first.
	if want := []string{"base", "ubuntu", "worker"}; !reflect.DeepEqual(got.AppliedLayers, want) {
		t.Fatalf("appliedLayers = %v, want %v", got.AppliedLayers, want)
	}
}

// ADR-11 §1 sibling ambiguity: two equally-specific, non-ancestor leaves that write
// DIFFERING values at the same component+path fail closed (CodeAmbiguous).
func TestComposeSiblingConflictFailsClosed(t *testing.T) {
	base := meta("base", "", nil, Spec{ComponentRefs: []ComponentRef{{Name: "core"}}})
	worker := meta("worker", "base", &criteria.Criteria{Category: "kubernetes"},
		Spec{ComponentRefs: []ComponentRef{{Name: "core", Overrides: map[string]any{"replicas": 3}}}})
	ubuntu := meta("ubuntu", "base", &criteria.Criteria{OS: "ubuntu"},
		Spec{ComponentRefs: []ComponentRef{{Name: "core", Overrides: map[string]any{"replicas": 5}}}})
	st := &Store{Base: base, Overlays: map[string]*RecipeMetadata{"worker": worker, "ubuntu": ubuntu}}

	_, err := st.Compose(criteria.Criteria{Category: "kubernetes", OS: "ubuntu"})
	if code(err) != nkderr.CodeAmbiguous {
		t.Fatalf("expected CodeAmbiguous on conflicting sibling writes, got %v", err)
	}
}

// The predicate is keyed on same path only: siblings touching the SAME component at
// DIFFERENT paths still compose cleanly (orthogonal), never failing closed.
func TestComposeSiblingDisjointPathsCompose(t *testing.T) {
	base := meta("base", "", nil, Spec{ComponentRefs: []ComponentRef{{Name: "core"}}})
	worker := meta("worker", "base", &criteria.Criteria{Category: "kubernetes"},
		Spec{ComponentRefs: []ComponentRef{{Name: "core", Overrides: map[string]any{"cpu": 2}}}})
	ubuntu := meta("ubuntu", "base", &criteria.Criteria{OS: "ubuntu"},
		Spec{ComponentRefs: []ComponentRef{{Name: "core", Overrides: map[string]any{"mem": 4}}}})
	st := &Store{Base: base, Overlays: map[string]*RecipeMetadata{"worker": worker, "ubuntu": ubuntu}}

	got, err := st.Compose(criteria.Criteria{Category: "kubernetes", OS: "ubuntu"})
	if err != nil {
		t.Fatalf("disjoint-path siblings should compose, got %v", err)
	}
	var core ComponentRef
	for _, c := range got.Components {
		if c.Name == "core" {
			core = c
		}
	}
	if core.Overrides["cpu"] != 2 || core.Overrides["mem"] != 4 {
		t.Fatalf("expected both orthogonal overrides merged, got %v", core.Overrides)
	}
}

// A mixin contributes constraints + components; an unknown mixin and a constraint-name
// collision both fail closed.
func TestComposeMixins(t *testing.T) {
	mkStore := func(mixins []string, mx *RecipeMixin) *Store {
		base := meta("base", "", nil, Spec{
			ComponentRefs: []ComponentRef{{Name: "core"}},
			Constraints:   []Constraint{{Name: "k8s", Value: ">= 1.25"}},
			Mixins:        mixins,
		})
		st := &Store{Base: base, Overlays: map[string]*RecipeMetadata{}, Mixins: map[string]*RecipeMixin{}}
		if mx != nil {
			st.Mixins[mx.Metadata.Name] = mx
		}
		return st
	}
	osMixin := &RecipeMixin{Kind: RecipeMixinKind}
	osMixin.Metadata.Name = "os-ubuntu"
	osMixin.Spec.Constraints = []Constraint{{Name: "os", Value: "ubuntu"}}
	osMixin.Spec.ComponentRefs = []ComponentRef{{Name: "ubuntu-agent"}}

	got, err := mkStore([]string{"os-ubuntu"}, osMixin).Compose(criteria.Criteria{})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"core", "ubuntu-agent"}; !reflect.DeepEqual(names(got.Components), want) {
		t.Fatalf("mixin components not merged: %v", names(got.Components))
	}

	// unknown mixin fails closed.
	if _, err := mkStore([]string{"missing"}, nil).Compose(criteria.Criteria{}); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("unknown mixin must fail closed, got %v", err)
	}

	// constraint-name collision (mixin restates a base constraint name) fails closed.
	clash := &RecipeMixin{Kind: RecipeMixinKind}
	clash.Metadata.Name = "clash"
	clash.Spec.Constraints = []Constraint{{Name: "k8s", Value: ">= 1.30"}}
	if _, err := mkStore([]string{"clash"}, clash).Compose(criteria.Criteria{}); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("mixin constraint collision must fail closed, got %v", err)
	}
}

// ADR-11 makes the merge primitive itself authoritative for the invariant that
// constraints never compose. Every call path therefore gets the same fail-closed
// behavior, including direct merge, an inheritance chain, and sibling leaves.
func TestConstraintCollisionsFailClosedAtMergeSeam(t *testing.T) {
	t.Run("direct duplicate", func(t *testing.T) {
		s := &Spec{Constraints: []Constraint{{Name: "kernel", Value: ">=6.6"}}}
		err := s.Merge(&Spec{Constraints: []Constraint{{Name: "kernel", Value: ">=6.8"}}})
		if code(err) != nkderr.CodeInvalidCatalog {
			t.Fatalf("duplicate constraint must fail closed, got %v", err)
		}
	})

	t.Run("duplicate inside one layer", func(t *testing.T) {
		s := &Spec{}
		err := s.Merge(&Spec{Constraints: []Constraint{
			{Name: "kernel", Value: ">=6.6"},
			{Name: "kernel", Value: ">=6.8"},
		}})
		if code(err) != nkderr.CodeInvalidCatalog {
			t.Fatalf("intra-layer duplicate must fail closed, got %v", err)
		}
	})

	t.Run("whitespace alias", func(t *testing.T) {
		s := &Spec{}
		if err := s.Merge(&Spec{Constraints: []Constraint{{Name: " kernel ", Value: ">=6.6"}}}); code(err) != nkderr.CodeInvalidCatalog {
			t.Fatalf("constraint names with surrounding whitespace must fail closed, got %v", err)
		}
	})

	t.Run("duplicate component inside one layer", func(t *testing.T) {
		s := &Spec{Constraints: []Constraint{{Name: "existing", Value: "1"}}}
		before := append([]Constraint(nil), s.Constraints...)
		err := s.Merge(&Spec{
			Constraints:   []Constraint{{Name: "new", Value: "2"}},
			ComponentRefs: []ComponentRef{{Name: "runtime"}, {Name: "runtime"}},
		})
		if code(err) != nkderr.CodeInvalidCatalog {
			t.Fatalf("duplicate same-layer component must fail closed, got %v", err)
		}
		if !reflect.DeepEqual(s.Constraints, before) {
			t.Fatalf("failed merge partially mutated receiver: got %v want %v", s.Constraints, before)
		}
	})

	t.Run("inheritance chain", func(t *testing.T) {
		base := meta("base", "", nil, Spec{Constraints: []Constraint{{Name: "kernel", Value: ">=6.6"}}})
		leaf := meta("leaf", "base", &criteria.Criteria{Category: "kubernetes"},
			Spec{Constraints: []Constraint{{Name: "kernel", Value: ">=6.8"}}})
		st := &Store{Base: base, Overlays: map[string]*RecipeMetadata{"leaf": leaf}}
		if err := st.Validate(); code(err) != nkderr.CodeInvalidCatalog {
			t.Fatalf("chain collision must fail at validation, got %v", err)
		}
	})

	t.Run("sibling leaves", func(t *testing.T) {
		base := meta("base", "", nil, Spec{})
		category := meta("category", "base", &criteria.Criteria{Category: "kubernetes"},
			Spec{Constraints: []Constraint{{Name: "kernel", Value: ">=6.6"}}})
		os := meta("os", "base", &criteria.Criteria{OS: "ubuntu"},
			Spec{Constraints: []Constraint{{Name: "kernel", Value: ">=6.8"}}})
		st := &Store{Base: base, Overlays: map[string]*RecipeMetadata{"category": category, "os": os}}
		_, err := st.Compose(criteria.Criteria{Category: "kubernetes", OS: "ubuntu"})
		if code(err) != nkderr.CodeInvalidCatalog {
			t.Fatalf("sibling constraint collision must fail closed, got %v", err)
		}
	})
}

func TestMergeAssociativeForCollisionFreeSpecs(t *testing.T) {
	a := &Spec{
		Constraints:   []Constraint{{Name: "a", Value: "1"}},
		ComponentRefs: []ComponentRef{{Name: "core", Overrides: map[string]any{"nested": map[string]any{"a": 1}}}},
	}
	b := &Spec{
		Constraints:   []Constraint{{Name: "b", Value: "2"}},
		ComponentRefs: []ComponentRef{{Name: "core", Overrides: map[string]any{"nested": map[string]any{"b": 2}}}},
	}
	c := &Spec{
		Constraints:   []Constraint{{Name: "c", Value: "3"}},
		ComponentRefs: []ComponentRef{{Name: "core", Overrides: map[string]any{"nested": map[string]any{"a": 3}}}},
	}

	left := &Spec{}
	if err := left.Merge(a); err != nil {
		t.Fatal(err)
	}
	if err := left.Merge(b); err != nil {
		t.Fatal(err)
	}
	if err := left.Merge(c); err != nil {
		t.Fatal(err)
	}

	bc := &Spec{}
	if err := bc.Merge(b); err != nil {
		t.Fatal(err)
	}
	if err := bc.Merge(c); err != nil {
		t.Fatal(err)
	}
	right := &Spec{}
	if err := right.Merge(a); err != nil {
		t.Fatal(err)
	}
	if err := right.Merge(bc); err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(left.Constraints, right.Constraints) ||
		!reflect.DeepEqual(left.ComponentRefs, right.ComponentRefs) {
		t.Fatalf("merge is not associative: left=%+v right=%+v", left, right)
	}
}

// An inheritance cycle and an unknown base both fail closed at resolution.
func TestComposeFailClosed(t *testing.T) {
	base := meta("base", "", nil, Spec{ComponentRefs: []ComponentRef{{Name: "core"}}})

	// unknown base
	orphan := meta("orphan", "does-not-exist", &criteria.Criteria{Category: "kubernetes"}, Spec{})
	st := &Store{Base: base, Overlays: map[string]*RecipeMetadata{"orphan": orphan}}
	if _, err := st.Compose(criteria.Criteria{Category: "kubernetes"}); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("unknown base must fail closed, got %v", err)
	}

	// A base-chain cycle a -> b -> a is caught at load/validate time (Store.Validate
	// walks every overlay's chain), which is where the loader gates it. (In Compose a
	// mutual cycle where both nodes match is filtered out as mutual ancestors — same as
	// AICR — so Validate is the authoritative cycle guard.)
	a := meta("a", "b", &criteria.Criteria{Category: "kubernetes"}, Spec{})
	b := meta("b", "a", &criteria.Criteria{Category: "kubernetes"}, Spec{})
	st2 := &Store{Base: base, Overlays: map[string]*RecipeMetadata{"a": a, "b": b}}
	if err := st2.Validate(); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("inheritance cycle must fail closed at validate, got %v", err)
	}

	// A single surviving leaf with an unknown base fails closed in Compose too.
	leafBad := meta("leaf", "ghost", &criteria.Criteria{Category: "kubernetes", OS: "ubuntu"}, Spec{})
	st3 := &Store{Base: base, Overlays: map[string]*RecipeMetadata{"leaf": leafBad}}
	if _, err := st3.Compose(criteria.Criteria{Category: "kubernetes", OS: "ubuntu"}); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("surviving leaf with unknown base must fail closed in Compose, got %v", err)
	}
}

// Composition is deterministic: the same query yields byte-identical component ordering
// and applied-layer provenance across runs (sorted, no map-iteration leakage).
func TestComposeDeterministic(t *testing.T) {
	base := meta("base", "", nil, Spec{ComponentRefs: []ComponentRef{{Name: "z"}, {Name: "a"}, {Name: "m"}}})
	o := meta("w", "base", &criteria.Criteria{Category: "kubernetes"}, Spec{ComponentRefs: []ComponentRef{{Name: "b"}, {Name: "y"}}})
	st := &Store{Base: base, Overlays: map[string]*RecipeMetadata{"w": o}}
	first, err := st.Compose(criteria.Criteria{Category: "kubernetes"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		got, err := st.Compose(criteria.Criteria{Category: "kubernetes"})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(names(got.Components), names(first.Components)) {
			t.Fatalf("non-deterministic order: %v vs %v", names(got.Components), names(first.Components))
		}
	}
	if want := []string{"a", "b", "m", "y", "z"}; !reflect.DeepEqual(names(first.Components), want) {
		t.Fatalf("components not name-sorted: %v", names(first.Components))
	}
}

func TestCategoriesEnumeratesOnlyConcreteRecipeDeclarations(t *testing.T) {
	base := meta("base", "", nil, Spec{})
	kubernetes := meta("kubernetes", "base", &criteria.Criteria{Category: "kubernetes"}, Spec{})
	kubernetesUbuntu := meta("kubernetes-ubuntu", "kubernetes", &criteria.Criteria{Category: "kubernetes", OS: "ubuntu"}, Spec{})
	platform := meta("platform", "base", &criteria.Criteria{Category: "platform-services"}, Spec{})
	orthogonal := meta("ubuntu", "base", &criteria.Criteria{OS: "ubuntu"}, Spec{})
	store := &Store{Base: base, Overlays: map[string]*RecipeMetadata{
		"kubernetes": kubernetes, "kubernetes-ubuntu": kubernetesUbuntu,
		"platform": platform, "ubuntu": orthogonal,
	}}
	want := []string{"kubernetes", "platform-services"}
	if got := store.Categories(); !reflect.DeepEqual(got, want) {
		t.Fatalf("categories = %v, want %v", got, want)
	}
	if !store.DeclaresCategory("kubernetes") || store.DeclaresCategory("operations") {
		t.Fatalf("declared-category predicate disagrees with enumeration")
	}
}

func TestComposeSelectsExactLayeredAICRProfile(t *testing.T) {
	base := meta("base", "", nil, Spec{})
	profile := func(accelerator, digestByte string) *aicr.Profile {
		return &aicr.Profile{
			Identity:        accelerator + "-eks-ubuntu-training",
			Digest:          "sha256:" + strings.Repeat(digestByte, 64),
			GeneratorDigest: "sha256:" + strings.Repeat("c", 64),
			Service:         "eks", Accelerator: accelerator, OS: "ubuntu", Intent: "training",
		}
	}
	h100 := meta("accelerated-h100", "base", &criteria.Criteria{Category: "accelerated-runtime", OS: "ubuntu"},
		Spec{AICRProfile: profile("h100", "a")})
	h200 := meta("accelerated-h200", "base", &criteria.Criteria{Category: "accelerated-runtime", OS: "ubuntu"},
		Spec{AICRProfile: profile("h200", "b")})
	store := &Store{Base: base, Overlays: map[string]*RecipeMetadata{
		"accelerated-h100": h100, "accelerated-h200": h200,
	}}

	selection := &aicr.Selection{Service: "eks", Accelerator: "h100", OS: "ubuntu", Intent: "training"}
	got, err := store.ComposeFor(criteria.Criteria{Category: "accelerated-runtime", OS: "ubuntu"}, selection)
	if err != nil {
		t.Fatal(err)
	}
	if got.AICRProfile == nil || got.AICRProfile.Identity != "h100-eks-ubuntu-training" {
		t.Fatalf("selected profile = %+v", got.AICRProfile)
	}
	if _, err := store.Compose(criteria.Criteria{Category: "accelerated-runtime", OS: "ubuntu"}); code(err) != nkderr.CodeNoMatch {
		t.Fatalf("profile-bearing recipe resolved without its AICR selection: %v", err)
	}
}
