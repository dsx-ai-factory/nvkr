package compose

import (
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/criteria"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// mapProvider serves a layered catalog from in-memory path→bytes, implementing the
// FileProvider surface (ReadFile + ReadDir).
type mapProvider map[string][]byte

func (m mapProvider) ReadFile(name string) ([]byte, error) {
	if b, ok := m[name]; ok {
		return b, nil
	}
	return nil, fs.ErrNotExist
}

// ReadDir mirrors the real fsProvider's recursive contract: every file path under dir,
// relative to dir (so nested overlays/mixins are visible to the loader).
func (m mapProvider) ReadDir(dir string) ([]string, error) {
	prefix := dir + "/"
	var out []string
	for p := range m {
		if rest, ok := strings.CutPrefix(p, prefix); ok {
			out = append(out, rest)
		}
	}
	sort.Strings(out)
	return out, nil
}

const baseDoc = `kind: RecipeMetadata
apiVersion: nkd.dev/recipe/v1alpha1
metadata: {name: base}
spec:
  componentRefs: [{name: core}]
`

const workerDoc = `kind: RecipeMetadata
apiVersion: nkd.dev/recipe/v1alpha1
metadata: {name: worker}
spec:
  base: base
  criteria: {category: kubernetes}
  componentRefs: [{name: kubelet}]
`

func TestLoadStoreValid(t *testing.T) {
	st, err := LoadStore(mapProvider{
		"overlays/base.yaml":   []byte(baseDoc),
		"overlays/worker.yaml": []byte(workerDoc),
	})
	if err != nil {
		t.Fatal(err)
	}
	if st == nil || st.Base == nil || st.Overlays["worker"] == nil {
		t.Fatalf("store not loaded: %+v", st)
	}
	got, err := st.Compose(criteria.Criteria{Category: "kubernetes"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Components) != 2 { // core + kubelet
		t.Fatalf("composed %d components, want 2", len(got.Components))
	}
}

// base.yml (not base.yaml) is still classified as the root (isYAML accepts both), not
// misfiled as an overlay named "base".
func TestLoadStoreBaseYmlExtension(t *testing.T) {
	st, err := LoadStore(mapProvider{
		"overlays/base.yml":    []byte(baseDoc),
		"overlays/worker.yaml": []byte(workerDoc),
	})
	if err != nil || st == nil || st.Base == nil {
		t.Fatalf("base.yml must be recognized as the root: err=%v store=%v", err, st)
	}
}

// Overlays organized in a subdirectory are loaded, not silently dropped (recursive
// ReadDir). The nested file's overlay name comes from metadata.name, not its path.
func TestLoadStoreNestedOverlay(t *testing.T) {
	st, err := LoadStore(mapProvider{
		"overlays/base.yaml":         []byte(baseDoc),
		"overlays/roles/worker.yaml": []byte(workerDoc),
	})
	if err != nil {
		t.Fatal(err)
	}
	if st.Overlays["worker"] == nil {
		t.Fatalf("nested overlay must load; overlays=%v", st.Overlays)
	}
}

// A catalog with no overlays/ dir is flat-only: LoadStore returns (nil, nil).
func TestLoadStoreFlatOnly(t *testing.T) {
	st, err := LoadStore(mapProvider{"registry.yaml": []byte("x")})
	if err != nil || st != nil {
		t.Fatalf("flat-only catalog must yield (nil,nil), got store=%v err=%v", st, err)
	}
}

func TestLoadStoreFailClosed(t *testing.T) {
	cases := []struct {
		name  string
		files mapProvider
	}{
		{"overlays present but no base", mapProvider{"overlays/worker.yaml": []byte(workerDoc)}},
		{"wrong kind for base", mapProvider{"overlays/base.yaml": []byte(strings.Replace(baseDoc, "RecipeMetadata", "RecipeMixin", 1))}},
		{"duplicate overlay name", mapProvider{
			"overlays/base.yaml": []byte(baseDoc),
			"overlays/a.yaml":    []byte(workerDoc),
			"overlays/b.yaml":    []byte(workerDoc), // same metadata.name: worker
		}},
		{"overlay without criteria", mapProvider{
			"overlays/base.yaml": []byte(baseDoc),
			"overlays/x.yaml":    []byte("kind: RecipeMetadata\napiVersion: nkd.dev/recipe/v1alpha1\nmetadata: {name: x}\nspec: {componentRefs: [{name: core}]}\n"),
		}},
		{"unknown mixin reference", mapProvider{
			"overlays/base.yaml": []byte(baseDoc),
			"overlays/w.yaml":    []byte(workerDoc + "  mixins: [ghost]\n"),
		}},
		{"reserved overlay name base", mapProvider{
			"overlays/base.yaml": []byte(baseDoc),
			"overlays/dup.yaml":  []byte("kind: RecipeMetadata\napiVersion: nkd.dev/recipe/v1alpha1\nmetadata: {name: base}\nspec: {criteria: {category: kubernetes}}\n"),
		}},
		{"wrong apiVersion", mapProvider{
			"overlays/base.yaml": []byte(strings.Replace(baseDoc, APIVersion, "totally/wrong-v9", 1)),
		}},
		{"base.yaml with a non-base metadata.name", mapProvider{
			"overlays/base.yaml": []byte("kind: RecipeMetadata\napiVersion: nkd.dev/recipe/v1alpha1\nmetadata: {name: root}\nspec: {componentRefs: [{name: core}]}\n"),
		}},
		{"base criteria are silently inapplicable", mapProvider{
			"overlays/base.yaml": []byte("kind: RecipeMetadata\napiVersion: nkd.dev/recipe/v1alpha1\nmetadata: {name: base}\nspec: {criteria: {category: kubernetes}, componentRefs: [{name: core}]}\n"),
		}},
		{"base cannot inherit", mapProvider{
			"overlays/base.yaml": []byte("kind: RecipeMetadata\napiVersion: nkd.dev/recipe/v1alpha1\nmetadata: {name: base}\nspec: {base: other, componentRefs: [{name: core}]}\n"),
		}},
		{"typo'd top-level spec field (strict decode)", mapProvider{
			"overlays/base.yaml": []byte(baseDoc),
			"overlays/x.yaml":    []byte("kind: RecipeMetadata\napiVersion: nkd.dev/recipe/v1alpha1\nmetadata: {name: x}\nspec: {criteria: {category: kubernetes}, componentRef: [{name: core}]}\n"),
		}},
		{"role is composition metadata, not overlay selection", mapProvider{
			"overlays/base.yaml": []byte(baseDoc),
			"overlays/x.yaml":    []byte("kind: RecipeMetadata\napiVersion: nkd.dev/recipe/v1alpha1\nmetadata: {name: x}\nspec: {criteria: {role: worker}, componentRefs: [{name: core}]}\n"),
		}},
		{"clusterType is composition metadata, not overlay selection", mapProvider{
			"overlays/base.yaml": []byte(baseDoc),
			"overlays/x.yaml":    []byte("kind: RecipeMetadata\napiVersion: nkd.dev/recipe/v1alpha1\nmetadata: {name: x}\nspec: {criteria: {clusterType: tenant}, componentRefs: [{name: core}]}\n"),
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := LoadStore(tc.files); code(err) != nkderr.CodeInvalidCatalog {
				t.Fatalf("want CodeInvalidCatalog, got %v", err)
			}
		})
	}
}
