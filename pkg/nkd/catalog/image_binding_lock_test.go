package catalog

import (
	"fmt"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestImageBindingLoadRequiresExactParentDescription(t *testing.T) {
	parent := "sha256:" + strings.Repeat("b", 64)
	childA := "sha256:" + strings.Repeat("a", 64)
	childB := "sha256:" + strings.Repeat("c", 64)
	registry := []byte("apiVersion: nkd.dev/catalog/v3\nkind: SourceBuildRegistry\nsources: {s: {repo: https://github.com/o/r}}\ncomponents:\n  tool:\n    platforms: [linux/amd64, linux/arm64]\n    source: s\n    outputType: oci-image\n    prebuilt:\n      image: registry.example/tool@" + parent + "\n      evidence: {none: true}\n")
	for _, tc := range []struct {
		name, parent string
		code         e.Code
	}{
		{"exact index", parent, ""}, {"missing parent", "", e.CodeUnverifiedRef}, {"substituted parent", childA, e.CodeUnverifiedRef}, {"malformed parent", "invalid", e.CodeUnverifiedRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binding := ""
			if tc.parent != "" {
				binding = "    imageParentDigest: " + tc.parent + "\n"
			}
			lock := []byte(fmt.Sprintf("apiVersion: nkd.dev/lock/v1\nkind: PinLock\npins:\n  s: {tag: v1.0.0, commit: %s, treeHash: %s, sourceDateEpoch: 1}\nartifacts:\n  tool:\n%s    tag: v1.0.0\n    digests: {amd64: %s, arm64: %s}\n", goodCommit, goodTree, binding, childA, childB))
			loaded, err := LoadContext(t.Context(), bytesProvider{RegistryFile: registry, LockFile: lock})
			if tc.code != "" {
				assertCatalogContractCode(t, err, tc.code)
				if loaded != nil {
					t.Fatal("failed binding returned catalog")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Components["tool"].Prebuilt.ImageParentDigest != parent {
				t.Fatal("loader lost authored parent")
			}
			if loaded.Components["tool"].Prebuilt.Digests["amd64"] != childA || loaded.Components["tool"].Prebuilt.Digests["arm64"] != childB {
				t.Fatal("loader swapped native subjects")
			}
		})
	}
}

func TestArtifactPinEqualityRetainsParentAndNoOp(t *testing.T) {
	original := ArtifactPin{Tag: "v1.0.0", ImageParentDigest: "sha256:" + strings.Repeat("b", 64), Digests: map[string]string{"amd64": goodArtifactDigest}}
	identical := original
	identical.Digests = map[string]string{"amd64": goodArtifactDigest}
	if !artifactPinEqual(original, identical) {
		t.Fatal("same parent/native binding is not a no-op")
	}
	changed := identical
	changed.ImageParentDigest = "sha256:" + strings.Repeat("c", 64)
	if artifactPinEqual(original, changed) {
		t.Fatal("parent content constraint change ignored")
	}
}
