package catalog

import (
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestCatalogSourcePin(t *testing.T) {
	tool := ToolPin{Format: "raw", URL: "https://example.com/tool", Platform: "linux/amd64", ExecutablePath: "tool"}
	tests := []struct {
		name string
		cat  *Catalog
		want e.Code
	}{
		{name: "nil catalog", want: e.CodeInvalidCatalog},
		{
			name: "unknown source",
			cat:  &Catalog{Sources: map[string]Source{}, sourcePins: map[string]Pin{}},
			want: e.CodeInvalidCatalog,
		},
		{
			name: "missing validated pin",
			cat:  &Catalog{Sources: map[string]Source{"source": {}}, sourcePins: map[string]Pin{}},
			want: e.CodeUnpinnedSource,
		},
		{
			name: "validated pin",
			cat: &Catalog{
				Sources: map[string]Source{"source": {}},
				sourcePins: map[string]Pin{
					"source": {Commit: goodCommit, TreeHash: goodTree, Tool: &tool},
				},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pin, err := test.cat.SourcePin("source")
			if codeOf(err) != test.want {
				t.Fatalf("SourcePin() error = %v, want code %s", err, test.want)
			}
			if test.want != "" {
				return
			}
			if pin.Commit != goodCommit || pin.TreeHash != goodTree || pin.Tool == nil {
				t.Fatalf("SourcePin() = %#v", pin)
			}
			pin.Tool.ExecutablePath = "mutated"
			if test.cat.sourcePins["source"].Tool.ExecutablePath != "tool" {
				t.Fatal("returned pin aliases the validated catalog snapshot")
			}
			cloned, err := cloneCatalog(test.cat)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := cloned.SourcePin("source"); err != nil {
				t.Fatalf("cloned catalog lost its validated source pin: %v", err)
			}
		})
	}
}
