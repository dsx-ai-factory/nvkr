package catalog_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/catalog"
	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
	"github.com/nhensley/nkdctl/pkg/nkd/recipe"
)

func TestCatalogStackReplacesOneProducerWithoutForkingCatalog(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	baseDigest, err := base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	originalKubectl := base.Components["kubectl"]
	overlay := catalog.Overlay{
		APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind,
		Name: "trusted-containerd", RequiresCatalogDigest: baseDigest,
		Components: map[string]catalog.ComponentReplacement{
			"containerd": {
				Version: "2.0.5", SourceDateEpoch: 1_750_000_000,
				OutputType: base.Components["containerd"].OutputType,
				Prebuilt: &catalog.Prebuilt{
					URL: "https://supplier.example/containerd-{{.Arch}}",
					Digests: map[string]string{
						"amd64": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
						"arm64": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
					},
					Evidence: &catalog.ExternalEvidence{None: true},
				},
			},
		},
	}
	stack, err := catalog.ApplyStack(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	if stack.Digest == baseDigest || len(stack.Applied) != 1 {
		t.Fatalf("stack did not bind overlay: %#v", stack)
	}
	if got := stack.Catalog.Components["kubectl"]; got.Commit != originalKubectl.Commit || got.Prebuilt != nil {
		t.Fatalf("unrelated kubectl producer changed: %#v", got)
	}
	if got := stack.Catalog.Components["containerd"]; got.FromSource || got.Prebuilt == nil || got.Repo != "" {
		t.Fatalf("containerd was not replaced as a clean prebuilt lane: %#v", got)
	}

	resolved, err := recipe.Resolve(stack.Catalog, criteria.Criteria{
		Category: "kubernetes", Role: "worker", K8sVersion: "1.34.0", OS: "ubuntu", Arch: "amd64",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := recipe.Compile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	foundPull := false
	for _, node := range graph.Nodes {
		if node.Component == "containerd" {
			if node.Kind != ir.KindPull {
				t.Fatalf("containerd node kind=%s, want pull", node.Kind)
			}
			foundPull = true
		}
	}
	if !foundPull {
		t.Fatal("resolved graph omitted supplier containerd")
	}
}

func TestCatalogStackRejectsReplayAndUnknownReplacement(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	replacement := catalog.ComponentReplacement{
		Version: "1", SourceDateEpoch: 1_750_000_000, OutputType: "host-package",
		Prebuilt: &catalog.Prebuilt{URL: "https://supplier.example/tool", Digests: map[string]string{
			"amd64": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}, Evidence: &catalog.ExternalEvidence{None: true}},
	}
	_, err = catalog.ApplyStack(base, catalog.Overlay{APIVersion: catalog.OverlayAPIVersion,
		Kind: catalog.OverlayKind, Name: "replay", RequiresCatalogDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Components: map[string]catalog.ComponentReplacement{"kubectl": replacement}})
	if errorCode(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("replay code=%s err=%v", errorCode(err), err)
	}
	baseDigest, _ := base.SnapshotDigest()
	_, err = catalog.ApplyStack(base, catalog.Overlay{APIVersion: catalog.OverlayAPIVersion,
		Kind: catalog.OverlayKind, Name: "unknown", RequiresCatalogDigest: baseDigest,
		Components: map[string]catalog.ComponentReplacement{"not-real": replacement}})
	if errorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("unknown code=%s err=%v", errorCode(err), err)
	}
}

func TestLoadOverlayContextIsStrictAndNoFollow(t *testing.T) {
	base, err := catalog.LoadContext(context.Background(), catalog.Dir("../../../testdata/catalogs/kubernetes-layered"))
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := base.SnapshotDigest()
	dir := t.TempDir()
	path := filepath.Join(dir, "supplier.yaml")
	raw := fmt.Sprintf(`apiVersion: %s
kind: %s
name: supplier
requiresCatalogDigest: %s
components:
  containerd:
    version: "2.0.5"
    sourceDateEpoch: 1750000000
    outputType: oci-image
    prebuilt:
      image: supplier.example/containerd@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      digests:
        amd64: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
      evidence:
        none: true
`, catalog.OverlayAPIVersion, catalog.OverlayKind, digest)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay, err := catalog.LoadOverlayContext(context.Background(), path)
	if err != nil || overlay.Name != "supplier" {
		t.Fatalf("load overlay: %#v %v", overlay, err)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.LoadOverlayContext(context.Background(), link); errorCode(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("symlink code=%s err=%v", errorCode(err), err)
	}
}

func errorCode(err error) nkderr.Code {
	var coded *nkderr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}
