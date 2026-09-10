package recipe

import (
	"testing"

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
