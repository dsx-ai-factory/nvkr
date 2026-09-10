package recipe

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

var (
	digAmd = "sha256:" + strings.Repeat("a", 64)
	digArm = "sha256:" + strings.Repeat("b", 64)
	imgAmd = "sha256:" + strings.Repeat("c", 64)
	imgArm = "sha256:" + strings.Repeat("d", 64)
)

// prebuiltCatalog is a worker recipe with two prebuilt components: a binary URL
// (host-package) and an image (oci-image), each pinned per arch.
func prebuiltCatalog() *catalog.Catalog {
	return &catalog.Catalog{
		Recipes: []catalog.Recipe{{ID: "p", Criteria: criteria.Criteria{Category: "kubernetes"}, Components: []string{"tool", "img"}}},
		Components: map[string]catalog.Component{
			"tool": {OutputType: "host-package", Tag: "v1.32.0", SourceDate: 123, Prebuilt: &catalog.Prebuilt{
				URL: "https://dl/{{.Arch}}/tool", Digests: map[string]string{"amd64": digAmd, "arm64": digArm}, Evidence: &catalog.ExternalEvidence{None: true}}},
			"img": {OutputType: "oci-image", Tag: "v1.32.0", Prebuilt: &catalog.Prebuilt{
				Image: "registry.k8s.io/pause:3.10", Digests: map[string]string{"amd64": imgAmd, "arm64": imgArm}, Evidence: &catalog.ExternalEvidence{None: true}}},
		},
	}
}

// A prebuilt recipe compiles to pull nodes (no fetch, no build), the terminal verify
// depends on every pull, and each pull carries the per-arch acquisition ref + pinned
// digest. The binary lane carries SOURCE_DATE_EPOCH for deterministic .deb packaging;
// the image lane carries none (nothing to package).
func TestCompilePrebuiltPullNodes(t *testing.T) {
	res, err := Resolve(prebuiltCatalog(), criteria.Criteria{Category: "kubernetes", Role: "worker", Arch: "amd64"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	g := mustCompile(t, res)

	var pulls, builds, fetches, verifyNeeds int
	byComp := map[string]ir.Node{}
	for _, n := range g.Nodes {
		switch n.Kind {
		case ir.KindPull:
			pulls++
			byComp[n.Component] = n
			if n.Network != ir.NetworkDefault {
				t.Errorf("pull %s: network=%q, want default", n.Component, n.Network)
			}
			if n.Platform != "linux/amd64" {
				t.Errorf("pull %s: platform=%q", n.Component, n.Platform)
			}
		case ir.KindBuild:
			builds++
		case ir.KindFetch:
			fetches++
		case ir.KindVerify:
			verifyNeeds = len(n.Needs)
		}
	}
	if pulls != 2 || builds != 0 || fetches != 0 {
		t.Fatalf("want 2 pull / 0 build / 0 fetch, got %d/%d/%d", pulls, builds, fetches)
	}
	if verifyNeeds != 2 {
		t.Fatalf("verify must depend on both pulls, got %d", verifyNeeds)
	}

	tool := byComp["tool"]
	if tool.Artifact != "https://dl/amd64/tool" {
		t.Errorf("tool artifact ref not arch-rendered: %q", tool.Artifact)
	}
	if tool.Digest != digAmd {
		t.Errorf("tool digest=%q, want %q", tool.Digest, digAmd)
	}
	if epoch := envValue(tool, "SOURCE_DATE_EPOCH"); epoch != "123" {
		t.Errorf("host-package pull must carry SOURCE_DATE_EPOCH=123, got %q", epoch)
	}

	img := byComp["img"]
	if img.Artifact != "registry.k8s.io/pause:3.10" || img.Digest != imgAmd {
		t.Errorf("img pull ref/digest wrong: %q %q", img.Artifact, img.Digest)
	}
	if len(img.Env) != 0 {
		t.Errorf("oci-image pull must carry no env, got %v", img.Env)
	}
}

// The pinned artifact digest is part of the IR, so a per-arch digest change yields a
// distinct recipeDigest (a repin is a recipe change — exactly like a source bump).
func TestPrebuiltPerArchDistinctDigest(t *testing.T) {
	cat := prebuiltCatalog()
	dg := func(arch string) string {
		res, err := Resolve(cat, criteria.Criteria{Category: "kubernetes", Role: "worker", Arch: arch}, nil)
		if err != nil {
			t.Fatalf("resolve %s: %v", arch, err)
		}
		d, err := mustCompile(t, res).Digest()
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if a, b := dg("amd64"), dg("arm64"); a == b {
		t.Fatalf("amd64 and arm64 recipeDigests must differ (got %s for both)", a)
	}
}

// A prebuilt component with no pinned digest for the resolved arch fails closed at
// resolve — never acquire an unpinned artifact (NKD-FR-BLD-051).
func TestPrebuiltMissingArchFailsClosed(t *testing.T) {
	cat := &catalog.Catalog{
		Recipes: []catalog.Recipe{{ID: "p", Criteria: criteria.Criteria{Category: "kubernetes"}, Components: []string{"tool"}}},
		Components: map[string]catalog.Component{
			"tool": {OutputType: "raw", Tag: "v1", Prebuilt: &catalog.Prebuilt{
				URL: "https://dl/{{.Arch}}/tool", Digests: map[string]string{"amd64": digAmd}, Evidence: &catalog.ExternalEvidence{None: true}}}, // no arm64
		},
	}
	if _, err := Resolve(cat, criteria.Criteria{Category: "kubernetes", Role: "worker", Arch: "arm64"}, nil); code(err) != nkderr.CodeUnpinnedSource {
		t.Fatalf("missing arm64 pin must fail closed, got %v", err)
	}
	// amd64 (pinned) still resolves.
	if _, err := Resolve(cat, criteria.Criteria{Category: "kubernetes", Role: "worker", Arch: "amd64"}, nil); err != nil {
		t.Fatalf("amd64 (pinned) must resolve: %v", err)
	}
}

func envValue(n ir.Node, name string) string {
	for _, ev := range n.Env {
		if ev.Name == name {
			return ev.Value
		}
	}
	return ""
}
