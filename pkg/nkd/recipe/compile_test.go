package recipe

import (
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func mustCompile(t testing.TB, resolved *Resolved) *ir.Graph {
	t.Helper()
	graph, err := Compile(resolved)
	if err != nil {
		t.Fatalf("compile recipe: %v", err)
	}
	return graph
}

func TestCompileRejectsUnimplementedDiskCapabilityBeforeGraphConstruction(t *testing.T) {
	tests := []struct {
		name      string
		component catalog.Component
	}{
		{name: "disk output", component: catalog.Component{FromSource: true, OutputType: "disk-image"}},
		{name: "VM builder", component: catalog.Component{FromSource: true, BuilderType: catalog.BuilderVMImage}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Compile(&Resolved{RecipeID: "disk", Components: []catalog.NamedComponent{{Name: "image", Component: tt.component}}})
			if coded, ok := err.(*nkderr.Error); !ok || coded.Code != nkderr.CodeEngineCapability {
				t.Fatalf("error = %v, want %s", err, nkderr.CodeEngineCapability)
			}
		})
	}
}
