package sbom

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/buildgraph"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func resolveCP(t *testing.T) (*buildgraph.Resolved, string) {
	t.Helper()
	cat, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := buildgraph.Resolve(cat, buildgraph.Selection{Components: []string{"cni-plugins", "kube-apiserver", "kubectl", "kubelet"}, Platform: "linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	graph, err := buildgraph.Compile(res)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := graph.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return res, digest
}

func TestSBOMDeterministic(t *testing.T) {
	res, digest := resolveCP(t)
	a, err := fromBuild(res, digest, nil, false).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := fromBuild(res, digest, nil, false).Marshal()
	if !bytes.Equal(a, b) {
		t.Fatal("SBOM marshaling is not deterministic")
	}
}

func TestSBOMFoldsArtifactHashes(t *testing.T) {
	res, digest := resolveCP(t)
	b := fromBuild(res, digest, map[string]string{"kubectl": "sha256:abc"}, false)
	var found bool
	for _, c := range b.Components {
		if c.Name == "kubectl" {
			if len(c.Hashes) != 1 || c.Hashes[0].Content != "abc" || c.Hashes[0].Alg != "SHA-256" {
				t.Fatalf("kubectl hash not folded correctly: %+v", c.Hashes)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("kubectl not in SBOM")
	}
}

func TestPURL(t *testing.T) {
	if got := purl("https://github.com/etcd-io/etcd", "9f4b12"); got != "pkg:github/etcd-io/etcd@9f4b12" {
		t.Errorf("github purl wrong: %s", got)
	}
	if got := purl("https://example.com/foo/bar.git", "abc"); got != "pkg:generic/bar@abc?download_url=https://example.com/foo/bar.git" {
		t.Errorf("generic purl wrong: %s", got)
	}
}

// ADR-13 §1: per-artifact SBOMs + an aggregate index, all deterministic.
func TestPerArtifactAndIndex(t *testing.T) {
	res, digest := resolveCP(t)
	hashes := make(map[string]string)
	for _, component := range res.Components {
		if component.FromSource || component.Prebuilt != nil {
			hashes[component.Name] = "sha256:" + strings.Repeat("a", 64)
		}
	}
	boms1, idx1, err := PerArtifact(res, digest, hashes)
	if err != nil {
		t.Fatal(err)
	}
	if len(boms1) == 0 || len(idx1.Entries) != len(boms1) {
		t.Fatalf("expected one SBOM+index entry per artifact, got %d boms / %d entries", len(boms1), len(idx1.Entries))
	}
	// each per-artifact SBOM is bound to a single subject artifact
	for name, b := range boms1 {
		if len(b.Components) != 0 || b.Metadata.Component.Name != name {
			t.Fatalf("per-artifact SBOM for %q is not single-subject in metadata: components=%+v subject=%q",
				name, b.Components, b.Metadata.Component.Name)
		}
	}
	// index digest is deterministic across runs
	_, idx2, _ := PerArtifact(res, digest, hashes)
	d1, _ := idx1.Digest()
	d2, _ := idx2.Digest()
	if d1 != d2 {
		t.Fatalf("sbom index digest not deterministic: %s vs %s", d1, d2)
	}
}

func TestSBOMAndIndexRejectSemanticAmbiguityBeforeDigest(t *testing.T) {
	res, buildDigest := resolveCP(t)
	hashes := make(map[string]string)
	for _, component := range res.Components {
		if component.FromSource || component.Prebuilt != nil {
			hashes[component.Name] = "sha256:" + strings.Repeat("a", 64)
		}
	}
	boms, index, err := PerArtifact(res, buildDigest, hashes)
	if err != nil {
		t.Fatal(err)
	}

	var subject *BOM
	for _, bom := range boms {
		subject = bom
		break
	}
	if subject == nil {
		t.Fatal("missing per-artifact SBOM fixture")
	}
	badHash := cloneBOM(subject)
	badHash.Metadata.Component.Hashes[0].Content = strings.Repeat("A", 64)
	if _, err := badHash.Digest(); sbomCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("uppercase subject hash must not acquire an SBOM identity, got %v", err)
	}

	badSubject := cloneBOM(subject)
	badSubject.Metadata.Component.Name = "other"
	if _, err := badSubject.Marshal(); sbomCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("metadata/subject substitution must fail closed, got %v", err)
	}

	badIndex := *index
	badIndex.Entries = append([]IndexEntry(nil), index.Entries...)
	badIndex.Entries[1] = badIndex.Entries[0]
	if _, err := badIndex.Digest(); sbomCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("duplicate index artifact must not acquire an identity, got %v", err)
	}
	badIndex = *index
	badIndex.Entries = append([]IndexEntry(nil), index.Entries...)
	badIndex.Entries[0].ArtifactSHA = ""
	if _, err := badIndex.Digest(); sbomCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("unbound index artifact must not acquire an identity, got %v", err)
	}
}
