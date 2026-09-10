package compose

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/criteria"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
)

type failingCatalogProvider struct {
	mapProvider
	failDir, failFile string
}

func (p failingCatalogProvider) ReadDir(name string) ([]string, error) {
	if name == p.failDir {
		return nil, fs.ErrPermission
	}
	return p.mapProvider.ReadDir(name)
}

func (p failingCatalogProvider) ReadFile(name string) ([]byte, error) {
	if name == p.failFile {
		return nil, fs.ErrPermission
	}
	return p.mapProvider.ReadFile(name)
}

const contractMixin = `kind: RecipeMixin
apiVersion: nkd.dev/recipe/v1alpha1
metadata: {name: extra}
spec:
  componentRefs: [{name: addon}]
`

func TestLoadStoreMixinCompositionAndInvalidDocuments(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(mapProvider)
	}{
		{name: "valid nested mixin"},
		{name: "wrong mixin kind", change: func(p mapProvider) {
			p["mixins/nested/extra.yml"] = []byte(strings.Replace(contractMixin, "RecipeMixin", "RecipeMetadata", 1))
		}},
		{name: "wrong mixin version", change: func(p mapProvider) {
			p["mixins/nested/extra.yml"] = []byte(strings.Replace(contractMixin, APIVersion, "wrong/v1", 1))
		}},
		{name: "mixin missing name", change: func(p mapProvider) {
			p["mixins/nested/extra.yml"] = []byte(strings.Replace(contractMixin, "name: extra", "name: ''", 1))
		}},
		{name: "duplicate mixin", change: func(p mapProvider) { p["mixins/duplicate.yaml"] = []byte(contractMixin) }},
		{name: "invalid mixin YAML", change: func(p mapProvider) { p["mixins/nested/extra.yml"] = []byte("[") }},
		{name: "unknown mixin field", change: func(p mapProvider) { p["mixins/nested/extra.yml"] = []byte(contractMixin + "  unknown: true\n") }},
		{name: "invalid overlay YAML", change: func(p mapProvider) { p["overlays/worker.yaml"] = []byte("[") }},
		{name: "overlay missing name", change: func(p mapProvider) {
			p["overlays/worker.yaml"] = []byte(strings.Replace(workerDoc, "name: worker", "name: ''", 1))
		}},
		{name: "duplicate base", change: func(p mapProvider) { p["overlays/base.yml"] = []byte(baseDoc) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := mapProvider{"overlays/base.yaml": []byte(baseDoc), "overlays/worker.yaml": []byte(workerDoc + "  mixins: [extra]\n"), "mixins/nested/extra.yml": []byte(contractMixin), "overlays/README.md": []byte("ignored"), "mixins/README.md": []byte("ignored")}
			if tc.change != nil {
				tc.change(files)
			}
			store, err := LoadStore(files)
			if tc.change != nil {
				if code(err) != e.CodeInvalidCatalog || store != nil {
					t.Fatalf("invalid store escaped: %+v, %v", store, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := store.Compose(criteria.Criteria{Category: "kubernetes"})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Components) != 3 {
				t.Fatalf("mixin lost component: %+v", result.Components)
			}
			for _, name := range []string{"core", "kubelet", "addon"} {
				found := false
				for _, c := range result.Components {
					if c.Name == name {
						found = true
					}
				}
				if !found {
					t.Fatalf("missing %s", name)
				}
			}
		})
	}
}

func TestLoadStoreIOFailuresReturnNoPartialStore(t *testing.T) {
	for _, tc := range []struct{ name, dir, file string }{
		{"overlay listing", "overlays", ""},
		{"mixin listing", "mixins", ""},
		{"base read", "", "overlays/base.yaml"},
		{"mixin read", "", "mixins/extra.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := failingCatalogProvider{mapProvider: mapProvider{"overlays/base.yaml": []byte(baseDoc), "mixins/extra.yaml": []byte(contractMixin)}, failDir: tc.dir, failFile: tc.file}
			store, err := LoadStore(p)
			if code(err) != e.CodeInvalidCatalog || store != nil {
				t.Fatalf("partial store escaped read failure: %+v, %v", store, err)
			}
		})
	}
}
