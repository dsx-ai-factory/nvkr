package recipe

import (
	"context"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
)

func layeredKubernetesFixture(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatalf("layered Kubernetes fixture must load and validate: %v", err)
	}
	return cat
}

// End-to-end: the explicit Kubernetes fixture is layered. A
// control-plane query COMPOSES base + the control-plane overlay to the full Tier-1 set;
// a worker query composes base alone. Component counts are preserved from the prior flat
// recipes; the recipe id is the composed coordinate (NKD-FR-RC-014/016).
func TestResolveLayeredKubernetesFixture(t *testing.T) {
	cat := layeredKubernetesFixture(t)
	if cat.Layers == nil {
		t.Fatal("Kubernetes fixture must be a layered catalog")
	}
	// ADR-12: the `kubernetes` category is ONE recipe producing every node-level binary
	// (the full Tier-1 set, 15) under ONE identity; role is a delivery SKU filter.
	k8s, err := Resolve(cat, criteria.Criteria{Category: "kubernetes", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if k8s.RecipeID != "kubernetes-1.32.0-ubuntu-amd64" || len(k8s.Components) != 15 {
		t.Fatalf("kubernetes: got %s with %d components", k8s.RecipeID, len(k8s.Components))
	}
	// The control-plane SKU is the full set; the worker SKU is the roles⊇worker subset
	// (the shared substrate, 8) — carved by FilterByRole, NOT a separate recipe.
	cp, err := FilterByRole(k8s, "control-plane")
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Components) != 15 {
		t.Fatalf("control-plane SKU: want 15, got %d", len(cp.Components))
	}
	wk, err := FilterByRole(k8s, "worker")
	if err != nil {
		t.Fatal(err)
	}
	if len(wk.Components) != 8 {
		t.Fatalf("worker SKU: want 8, got %d", len(wk.Components))
	}
	if gpu, err := FilterByRole(k8s, "gpu-worker"); err != nil {
		t.Fatal(err)
	} else if len(gpu.Components) != 8 {
		t.Fatalf("gpu-worker SKU: want the same 8-component node substrate, got %d", len(gpu.Components))
	}
	// Role is composition, not identity: the SKU query shares the category recipe id.
	if wkRes, _ := Resolve(cat, criteria.Criteria{Category: "kubernetes", Role: "worker", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"}, nil); wkRes.RecipeID != k8s.RecipeID {
		t.Fatalf("worker SKU recipe id %q must equal the category id %q", wkRes.RecipeID, k8s.RecipeID)
	}
}

// A flat catalog whose recipes do not match the query fails closed with NoMatch. (The
// layered default composes from base for any in-domain query, so NoMatch is exercised
// against a flat catalog — the path that still uses asymmetric single-recipe match.)
func TestResolveNoMatch(t *testing.T) {
	cat := &catalog.Catalog{
		Recipes:    []catalog.Recipe{{ID: "os", Criteria: criteria.Criteria{Category: "os"}, Components: []string{"x"}}},
		Components: map[string]catalog.Component{"x": {FromSource: false, ExclusionReason: "test"}},
	}
	_, err := Resolve(cat, criteria.Criteria{Category: "kubernetes"}, nil)
	if code(err) != nkderr.CodeNoMatch {
		t.Fatalf("want CodeNoMatch, got %v", err)
	}
}

func TestResolveReservedFailsClosed(t *testing.T) {
	cat := layeredKubernetesFixture(t)
	_, err := Resolve(cat, criteria.Criteria{Category: "kubernetes"}, map[string]string{"accelerator": "h100"})
	if code(err) != nkderr.CodeReservedAxis {
		t.Fatalf("want CodeReservedAxis, got %v", err)
	}
}

func TestResolveAmbiguousFailsClosed(t *testing.T) {
	cat := &catalog.Catalog{
		Recipes: []catalog.Recipe{
			{ID: "a", Criteria: criteria.Criteria{Category: "kubernetes"}, Components: []string{"x"}},
			{ID: "b", Criteria: criteria.Criteria{OS: "ubuntu"}, Components: []string{"x"}},
		},
		Components: map[string]catalog.Component{"x": {FromSource: false, ExclusionReason: "test"}},
	}
	// query satisfies both single-constraint recipes equally.
	_, err := Resolve(cat, criteria.Criteria{Category: "kubernetes", OS: "ubuntu"}, nil)
	if code(err) != nkderr.CodeAmbiguous {
		t.Fatalf("want CodeAmbiguous, got %v", err)
	}
}

// AICR reconciliation (NKD v1 goal 10 / §8.1): the accelerated-runtime axes
// (accelerator/service/intent/platform) activate for a recipe that declares a
// digest-pinned AICR profile, and fail closed otherwise. Hard-reserved axes
// (tier/compliance) fail closed regardless.
func TestAICRAxisGating(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	generatorDigest := "sha256:" + strings.Repeat("c", 64)
	catAICR := &catalog.Catalog{
		Recipes: []catalog.Recipe{
			{
				ID:       "accel-h100",
				Criteria: criteria.Criteria{Category: "accelerated-runtime"},
				AICRProfile: &catalog.AICRProfile{
					Identity: "h100-eks", Digest: digest, GeneratorDigest: generatorDigest, Service: "eks", Accelerator: "h100",
					OS: "ubuntu", Intent: "training", Platform: "kubeflow",
				},
				Components: []string{"x"},
			},
			{
				ID:       "accel-h200",
				Criteria: criteria.Criteria{Category: "accelerated-runtime"},
				AICRProfile: &catalog.AICRProfile{
					Identity: "h200-eks", Digest: "sha256:" + strings.Repeat("b", 64), GeneratorDigest: generatorDigest, Service: "eks", Accelerator: "h200",
					OS: "ubuntu", Intent: "training", Platform: "kubeflow",
				},
				Components: []string{"x"},
			},
		},
		Components: map[string]catalog.Component{"x": {FromSource: false, ExclusionReason: "test"}},
	}
	// The complete AICR tuple disambiguates otherwise-identical category recipes, and
	// the selected identity+digest is carried through to Resolved.
	axes := map[string]string{"service": "eks", "accelerator": "h100", "intent": "training", "platform": "kubeflow"}
	res, err := Resolve(catAICR, criteria.Criteria{Category: "accelerated-runtime", OS: "ubuntu"}, axes)
	if err != nil {
		t.Fatalf("AICR-governed accelerator must resolve under an AICR recipe: %v", err)
	}
	if res.RecipeID != "accel-h100" || res.AICRProfile == nil || res.AICRProfile.Identity != "h100-eks" {
		t.Fatalf("resolved recipe must carry the AICR profile, got %+v", res.AICRProfile)
	}
	if _, err := Resolve(catAICR, criteria.Criteria{Category: "accelerated-runtime", OS: "ubuntu"},
		map[string]string{"service": "eks", "accelerator": "b200", "intent": "training", "platform": "kubeflow"}); code(err) != nkderr.CodeNoMatch {
		t.Fatalf("unmatched AICR tuple must fail closed with no-match, got %v", err)
	}
	if _, err := Resolve(catAICR, criteria.Criteria{Category: "accelerated-runtime", OS: "ubuntu"},
		map[string]string{"accelerator": "h100"}); code(err) != nkderr.CodeNoMatch {
		t.Fatalf("incomplete asymmetric AICR tuple must not wildcard silently, got %v", err)
	}
	// An omitted optional profile platform matches only an omitted query platform. A
	// requested platform is an activated generator input and may never be wildcarded.
	withoutPlatform := *catAICR.Recipes[0].AICRProfile
	withoutPlatform.Platform = ""
	catAICR.Recipes[0].AICRProfile = &withoutPlatform
	if _, err := Resolve(catAICR, criteria.Criteria{Category: "accelerated-runtime", OS: "ubuntu"}, axes); code(err) != nkderr.CodeNoMatch {
		t.Fatalf("requested platform must not match a profile that omits it, got %v", err)
	}
	// A hard-reserved axis still fails closed even with AICR present.
	if _, err := Resolve(catAICR, criteria.Criteria{Category: "accelerated-runtime", OS: "ubuntu"}, map[string]string{"compliance": "fips"}); code(err) != nkderr.CodeReservedAxis {
		t.Fatalf("hard-reserved compliance must fail closed, got %v", err)
	}
	// A catalog with no AICR recipe: accelerator fails closed (v1 default preserved).
	catNo := &catalog.Catalog{
		Recipes:    []catalog.Recipe{{ID: "w", Criteria: criteria.Criteria{Category: "accelerated-runtime"}, Components: []string{"x"}}},
		Components: map[string]catalog.Component{"x": {FromSource: false, ExclusionReason: "test"}},
	}
	if _, err := Resolve(catNo, criteria.Criteria{Category: "accelerated-runtime", OS: "ubuntu"}, axes); code(err) != nkderr.CodeReservedAxis {
		t.Fatalf("accelerator must fail closed with no AICR recipe, got %v", err)
	}
}

func TestAICROutputInputsBindRecipeDigestButLabelsDoNot(t *testing.T) {
	profile := &catalog.AICRProfile{
		Identity: "friendly-name", Repo: "https://github.com/NVIDIA/aicr",
		Digest: "sha256:" + strings.Repeat("a", 64), GeneratorDigest: "sha256:" + strings.Repeat("c", 64), Service: "eks", Accelerator: "h100",
		OS: "ubuntu", Intent: "training", Platform: "kubeflow",
	}
	resolved := &Resolved{
		RecipeID: "accelerated-runtime", Criteria: criteria.Criteria{Category: "accelerated-runtime", OS: "ubuntu", Arch: "amd64"},
		AICRProfile: profile,
	}
	digest := func(r *Resolved) string {
		d, err := mustCompile(t, r).Digest()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	baseDigest := digest(resolved)
	if got := mustCompile(t, resolved).AICR; got == nil || got.RecipeDigest != profile.Digest ||
		got.GeneratorDigest != profile.GeneratorDigest || got.Accelerator != "h100" {
		t.Fatalf("compiled graph is missing typed AICR activation: %+v", got)
	}

	labels := *profile
	labels.Identity = "renamed-for-humans"
	labels.Repo = "https://mirror.invalid/aicr"
	if got := digest(&Resolved{RecipeID: resolved.RecipeID, Criteria: resolved.Criteria, AICRProfile: &labels}); got != baseDigest {
		t.Fatalf("provenance label/transport moved recipe identity: %s != %s", got, baseDigest)
	}

	for name, mutate := range map[string]func(*catalog.AICRProfile){
		"service":       func(p *catalog.AICRProfile) { p.Service = "gke" },
		"accelerator":   func(p *catalog.AICRProfile) { p.Accelerator = "h200" },
		"os":            func(p *catalog.AICRProfile) { p.OS = "rhel" },
		"intent":        func(p *catalog.AICRProfile) { p.Intent = "inference" },
		"platform":      func(p *catalog.AICRProfile) { p.Platform = "dynamo" },
		"recipe digest": func(p *catalog.AICRProfile) { p.Digest = "sha256:" + strings.Repeat("b", 64) },
		"generator digest": func(p *catalog.AICRProfile) {
			p.GeneratorDigest = "sha256:" + strings.Repeat("d", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := *profile
			mutate(&changed)
			changedCriteria := resolved.Criteria
			if name == "os" {
				changedCriteria.OS = changed.OS
			}
			if got := digest(&Resolved{RecipeID: resolved.RecipeID, Criteria: changedCriteria, AICRProfile: &changed}); got == baseDigest {
				t.Fatalf("output-affecting AICR %s did not move recipe identity", name)
			}
		})
	}
}

func TestResolveRequiresToolchainForSelectedNativeArch(t *testing.T) {
	component := catalog.Component{
		FromSource: true, Repo: "https://github.com/example/tool",
		Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImages: map[string]string{"amd64": "sha256:" + strings.Repeat("c", 64)},
		BuildCommand:    []string{"make"}, ArtifactPath: "out/tool", OutputType: "host-package", SourceDate: 1,
	}
	cat := &catalog.Catalog{
		Recipes:    []catalog.Recipe{{ID: "native", Criteria: criteria.Criteria{Category: "kubernetes"}, Components: []string{"tool"}}},
		Components: map[string]catalog.Component{"tool": component},
	}
	if _, err := Resolve(cat, criteria.Criteria{Category: "kubernetes", Arch: "arm64"}, nil); code(err) != nkderr.CodeUnpinnedSource {
		t.Fatalf("missing selected-arch toolchain must fail closed, got %v", err)
	}
	if _, err := Resolve(cat, criteria.Criteria{Category: "kubernetes", Arch: "amd64"}, nil); err != nil {
		t.Fatalf("present selected-arch toolchain should resolve: %v", err)
	}
}

// The compiled IR shares one fetch across all core components, has a build node
// per component, and ends in a single verify node depending on every build.
func TestCompileGraphShape(t *testing.T) {
	cat := layeredKubernetesFixture(t)
	res, err := Resolve(cat, criteria.Criteria{Category: "kubernetes", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := mustCompile(t, res)
	var fetch, build, verify int
	var verifyNeeds int
	for _, n := range g.Nodes {
		switch n.Kind {
		case ir.KindFetch:
			fetch++
		case ir.KindBuild:
			build++
			if n.Network != ir.NetworkNone {
				t.Errorf("build node %s not hermetic (network=%q)", n.ID, n.Network)
			}
		case ir.KindVerify:
			verify++
			verifyNeeds = len(n.Needs)
		}
	}
	// Eight sources => eight fetch nodes (the 8 kubernetes components incl. pause
	// share one fetch; etcd, runc, coredns, containerd, cni-plugins, crictl, helm
	// each have their own).
	if fetch != 8 {
		t.Errorf("expected 8 fetch nodes (one per source), got %d", fetch)
	}
	if build != 15 {
		t.Errorf("expected 15 build nodes, got %d", build)
	}
	if verify != 1 || verifyNeeds != 15 {
		t.Errorf("expected 1 verify node needing 15 builds, got verify=%d needs=%d", verify, verifyNeeds)
	}
}

func TestCompileSeparatesVendorClosuresForSameSourcePin(t *testing.T) {
	base := catalog.Component{
		FromSource: true, Repo: "https://example.test/shared", Tag: "v1.0.0",
		Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40),
		ToolchainImage: "registry.example/toolchain@sha256:" + strings.Repeat("c", 64),
		OutputType:     "raw", BuildCommand: []string{"make"}, WriteDir: "out",
		ArtifactPath: "out/artifact", SourceDate: 1,
	}
	first, second := base, base
	first.VendorCommand = []string{"go", "mod", "vendor"}
	second.VendorCommand = []string{"make", "vendor"}
	resolved := &Resolved{
		RecipeID: "vendor-split", Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{
			{Name: "alpha", Component: first},
			{Name: "beta", Component: second},
		},
	}
	graph := mustCompile(t, resolved)
	if err := graph.Validate(); err != nil {
		t.Fatalf("separate vendor closures must compile to valid IR: %v", err)
	}
	var fetches []ir.Node
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindFetch {
			fetches = append(fetches, node)
		}
	}
	if len(fetches) != 2 || fetches[0].ID == fetches[1].ID || fetches[0].Dest == fetches[1].Dest {
		t.Fatalf("different vendor specs collided: %+v", fetches)
	}
	if fetches[0].VendorSpecDigest == fetches[1].VendorSpecDigest {
		t.Fatalf("different vendor commands produced one closure identity: %+v", fetches)
	}
}

func TestCompileSourceIdentityIsTransportIndependentAndCollisionFree(t *testing.T) {
	component := func(name, repo, tree, image string) catalog.NamedComponent {
		return catalog.NamedComponent{Name: name, Component: catalog.Component{
			FromSource: true, Repo: repo, Tag: "v1.0.0",
			Commit: strings.Repeat("a", 40), TreeHash: tree,
			ToolchainImage: image, OutputType: "raw", BuildCommand: []string{"make"},
			WriteDir: "out", ArtifactPath: "out/artifact", SourceDate: 1,
		}}
	}
	digest := strings.Repeat("c", 64)
	left := &Resolved{
		RecipeID: "source-content", Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{component("alpha", "https://one.example/team/shared.git", strings.Repeat("b", 40), "one.example/toolchain@sha256:"+digest)},
	}
	right := &Resolved{
		RecipeID: left.RecipeID, Criteria: left.Criteria,
		Components: []catalog.NamedComponent{component("alpha", "ssh://mirror.example/other/name.git", strings.Repeat("b", 40), "mirror.example/toolchain@sha256:"+digest)},
	}
	leftGraph, rightGraph := mustCompile(t, left), mustCompile(t, right)
	leftDigest, err := leftGraph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	rightDigest, err := rightGraph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if leftDigest != rightDigest {
		t.Fatalf("repository/registry relocation changed content identity: %s != %s", leftDigest, rightDigest)
	}
	if leftGraph.Nodes[1].ID != rightGraph.Nodes[1].ID || leftGraph.Nodes[1].Dest != rightGraph.Nodes[1].Dest {
		t.Fatalf("transport relocation changed source node identity: %+v != %+v", leftGraph.Nodes[1], rightGraph.Nodes[1])
	}
	if got := strings.TrimPrefix(leftGraph.Nodes[1].Dest, "source-"); len(got) != 64 {
		t.Fatalf("source destination truncates its collision-resistant content identity: %q", leftGraph.Nodes[1].Dest)
	}

	collision := &Resolved{
		RecipeID: "source-collision", Criteria: left.Criteria,
		Components: []catalog.NamedComponent{
			component("alpha", "https://one.example/team/shared.git", strings.Repeat("b", 40), "one.example/toolchain@sha256:"+digest),
			component("beta", "https://two.example/other/shared.git", strings.Repeat("d", 40), "one.example/toolchain@sha256:"+digest),
		},
	}
	graph := mustCompile(t, collision)
	var fetches []ir.Node
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindFetch {
			fetches = append(fetches, node)
		}
	}
	if len(fetches) != 2 || fetches[0].ID == fetches[1].ID || fetches[0].Dest == fetches[1].Dest {
		t.Fatalf("different content pins with colliding repo basenames shared a fetch: %+v", fetches)
	}
}

func TestCompileRejectsMalformedPublicResolvedWithoutPanic(t *testing.T) {
	malformed := &Resolved{
		RecipeID: "malformed", Criteria: criteria.Criteria{OS: "ubuntu", Arch: "amd64"},
		Components: []catalog.NamedComponent{{Name: "bad", Component: catalog.Component{
			FromSource: true, Repo: "https://example.test/source", Commit: "short", TreeHash: strings.Repeat("b", 40),
			ToolchainImage: "toolchain@sha256:" + strings.Repeat("c", 64), VendorCommand: []string{"bad\xff"},
			Tag: "v1", SourceDate: 1, OutputType: "raw", BuildCommand: []string{"make"}, WriteDir: "out", ArtifactPath: "out/bad",
		}}},
	}
	if graph, err := Compile(malformed); err == nil || graph != nil {
		t.Fatalf("malformed public Resolved compiled successfully: graph=%+v err=%v", graph, err)
	}
}

// Every build node carries the reproducibility flag discipline pinned in the IR
// itself, so it is part of recipeDigest and provably applied to every Go lane
// regardless of the toolchain image's ENV (NKD-FR-BLD-023, NKD-FR-REP-004,
// NKD-NFR-005). GOFLAGS is authoritative and cannot be shadowed by a catalog stamp.
func TestBuildNodesPinReproFlags(t *testing.T) {
	cat := layeredKubernetesFixture(t)
	res, err := Resolve(cat, criteria.Criteria{Category: "kubernetes", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := mustCompile(t, res)
	seen := 0
	for _, n := range g.Nodes {
		if n.Kind != ir.KindBuild {
			continue
		}
		seen++
		var goflags string
		for _, ev := range n.Env {
			if ev.Name == "GOFLAGS" {
				goflags = ev.Value
			}
		}
		for _, want := range []string{"-trimpath", "-buildvcs=false"} {
			if !strings.Contains(goflags, want) {
				t.Errorf("build %s: GOFLAGS %q missing %q", n.Component, goflags, want)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no build nodes found")
	}
}

// Multi-arch (model B): the SAME recipe resolves for amd64 and arm64, yielding
// DISTINCT recipeDigests, each build node carries the target GOARCH, and the runc
// CGO lane selects a toolchain image NATIVE to the arch (no cross-compilation).
func TestMultiArchDistinctDigestsAndNativeToolchain(t *testing.T) {
	cat := layeredKubernetesFixture(t)
	base := criteria.Criteria{Category: "kubernetes", K8sVersion: "1.32.0", OS: "ubuntu"}
	res := func(arch string) *Resolved {
		c := base
		c.Arch = arch
		r, err := Resolve(cat, c, nil)
		if err != nil {
			t.Fatalf("resolve arch %q: %v", arch, err)
		}
		return r
	}
	gAmd, gArm := mustCompile(t, res("amd64")), mustCompile(t, res("arm64"))
	dAmd, err := gAmd.Digest()
	if err != nil {
		t.Fatal(err)
	}
	dArm, _ := gArm.Digest()
	if dAmd == dArm {
		t.Fatalf("amd64 and arm64 recipeDigests must differ (got %s for both)", dAmd)
	}

	runcTC := func(g *ir.Graph) (tc, goarch string) {
		for _, n := range g.Nodes {
			if n.Kind == ir.KindBuild && n.Component == "runc" {
				tc = n.Toolchain
				for _, ev := range n.Env {
					if ev.Name == "GOARCH" {
						goarch = ev.Value
					}
				}
			}
		}
		return
	}
	tcAmd, gaAmd := runcTC(gAmd)
	tcArm, gaArm := runcTC(gArm)
	if gaAmd != "amd64" || gaArm != "arm64" {
		t.Fatalf("runc GOARCH mismatch: amd64-build=%q arm64-build=%q", gaAmd, gaArm)
	}
	if tcAmd == "" || tcArm == "" || tcAmd == tcArm {
		t.Fatalf("runc must use a native per-arch toolchain: amd64=%q arm64=%q", tcAmd, tcArm)
	}

	// Omitting arch defaults to amd64 (deterministic, backward-compatible).
	if d := res(""); d.Criteria.Arch != "amd64" {
		t.Fatalf("default arch must be amd64, got %q", d.Criteria.Arch)
	}
}

// Multi-arch across the WHOLE set (the "expand to all components" guarantee): the
// same control-plane recipe (all 15 components) compiles for amd64 and arm64 into
// distinct recipeDigests, and EVERY build node is native to its arch (model B) —
// target platform + GOARCH match, no foreign-arch literal leaks into the command or
// artifact path, and kubernetes' CGO/C lanes use a per-arch native toolchain + plain
// gcc (no cross-compiler prefix).
func TestMultiArchAllComponents(t *testing.T) {
	cat := layeredKubernetesFixture(t)
	base := criteria.Criteria{Category: "kubernetes", K8sVersion: "1.32.0", OS: "ubuntu"}
	compileArch := func(arch string) *ir.Graph {
		c := base
		c.Arch = arch
		r, err := Resolve(cat, c, nil)
		if err != nil {
			t.Fatalf("resolve %s: %v", arch, err)
		}
		return mustCompile(t, r)
	}
	gAmd, gArm := compileArch("amd64"), compileArch("arm64")

	dAmd, err := gAmd.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if dArm, _ := gArm.Digest(); dAmd == dArm {
		t.Fatalf("full-set amd64 and arm64 recipeDigests must differ (got %s for both)", dAmd)
	}

	// The foreign arch's literals must never appear in a build's command/path — this is
	// the concrete "no amd64 hardcoding" assertion across every lane.
	foreign := map[string][]string{"amd64": {"arm64", "aarch64"}, "arm64": {"amd64", "x86_64"}}
	check := func(g *ir.Graph, arch string) {
		builds := 0
		for _, n := range g.Nodes {
			if n.Kind != ir.KindBuild {
				continue
			}
			builds++
			if n.Platform != "linux/"+arch {
				t.Errorf("%s: build %s platform=%q, want linux/%s", arch, n.Component, n.Platform, arch)
			}
			var goarch string
			for _, ev := range n.Env {
				if ev.Name == "GOARCH" {
					goarch = ev.Value
				}
			}
			if goarch != arch {
				t.Errorf("%s: build %s GOARCH=%q, want %s", arch, n.Component, goarch, arch)
			}
			hay := strings.Join(n.Command, " ") + " " + n.ArtifactPath
			for _, bad := range foreign[arch] {
				if strings.Contains(hay, bad) {
					t.Errorf("%s: build %s leaks foreign-arch literal %q in %q", arch, n.Component, bad, hay)
				}
			}
		}
		if builds != 15 {
			t.Fatalf("%s: expected 15 build nodes, got %d", arch, builds)
		}
	}
	check(gAmd, "amd64")
	check(gArm, "arm64")

	// kubernetes' toolchain is a per-arch NATIVE pin: distinct image per arch.
	toolchainOf := func(g *ir.Graph, comp string) string {
		for _, n := range g.Nodes {
			if n.Kind == ir.KindBuild && n.Component == comp {
				return n.Toolchain
			}
		}
		return ""
	}
	if a, b := toolchainOf(gAmd, "kube-apiserver"), toolchainOf(gArm, "kube-apiserver"); a == "" || b == "" || a == b {
		t.Fatalf("kubernetes toolchain must be native-per-arch: amd64=%q arm64=%q", a, b)
	}
	// kubelet (the k8s CGO lane) uses plain native gcc, not a cross triple, on both arches.
	for _, g := range []*ir.Graph{gAmd, gArm} {
		for _, n := range g.Nodes {
			if n.Kind == ir.KindBuild && n.Component == "kubelet" {
				var cc string
				for _, ev := range n.Env {
					if ev.Name == "CC" {
						cc = ev.Value
					}
				}
				if cc != "gcc" {
					t.Errorf("kubelet CC=%q, want native gcc (model B, no cross prefix)", cc)
				}
			}
		}
	}
}

// recipeDigest is a pure function of the resolved recipe (NKD-FR-RC-018).
func TestRecipeDigestDeterministic(t *testing.T) {
	cat := layeredKubernetesFixture(t)
	q := criteria.Criteria{Category: "kubernetes", K8sVersion: "1.32.0", OS: "ubuntu", Arch: "amd64"}
	res1, _ := Resolve(cat, q, nil)
	res2, _ := Resolve(cat, q, nil)
	d1, err := mustCompile(t, res1).Digest()
	if err != nil {
		t.Fatal(err)
	}
	d2, _ := mustCompile(t, res2).Digest()
	if d1 != d2 {
		t.Fatalf("recipeDigest not deterministic: %s vs %s", d1, d2)
	}
}

func code(err error) nkderr.Code {
	if e, ok := err.(*nkderr.Error); ok {
		return e.Code
	}
	return ""
}
