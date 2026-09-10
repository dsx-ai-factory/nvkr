package catalog

import (
	"errors"
	"reflect"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/compose"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestComposeComponentsValidatesWholeSelectionBeforeReturning(t *testing.T) {
	for _, tc := range []struct {
		name string
		refs []compose.ComponentRef
		code e.Code
	}{
		{"preserved order", []compose.ComponentRef{{Name: "y"}, {Name: "x"}}, ""},
		{"merged stamps", []compose.ComponentRef{{Name: "x", Overrides: map[string]any{"versionStamp": map[string]any{"SECOND": "new"}}}}, ""},
		{"replaced command", []compose.ComponentRef{{Name: "x", Overrides: map[string]any{"buildCommand": []string{"make", "release"}}}}, ""},
		{"missing after valid", []compose.ComponentRef{{Name: "x"}, {Name: "absent"}}, e.CodeInvalidCatalog},
		{"unknown override", []compose.ComponentRef{{Name: "x", Overrides: map[string]any{"outputTyp": "raw"}}}, e.CodeInvalidCatalog},
		{"wrong field type", []compose.ComponentRef{{Name: "x", Overrides: map[string]any{"buildCommand": true}}}, e.CodeInvalidCatalog},
		{"unserializable override", []compose.ComponentRef{{Name: "x", Overrides: map[string]any{"buildCommand": make(chan int)}}}, e.CodeInvalidCatalog},
		{"invalid output", []compose.ComponentRef{{Name: "x", Overrides: map[string]any{"outputType": "unknown"}}}, e.CodeInvalidCatalog},
		{"unpinned source", []compose.ComponentRef{{Name: "x", Overrides: map[string]any{"commit": "main"}}}, e.CodeUnpinnedSource},
	} {
		t.Run(tc.name, func(t *testing.T) {
			component := goodComponent()
			component.VersionStamp = map[string]string{"FIRST": "kept", "SECOND": "old"}
			cat := base(component)
			cat.Components["y"] = goodComponent()
			got, err := cat.ComposeComponents(tc.refs)
			if tc.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.code || got != nil {
					t.Fatalf("composition = %v, %v; want no result and %s", got, err, tc.code)
				}
				return
			}
			if err != nil || len(got) != len(tc.refs) {
				t.Fatalf("composition = %v, %v", got, err)
			}
			for i, ref := range tc.refs {
				if got[i].Name != ref.Name {
					t.Fatalf("selection order changed: %v", got)
				}
			}
			if tc.name == "merged stamps" {
				if !reflect.DeepEqual(got[0].VersionStamp, map[string]string{"FIRST": "kept", "SECOND": "new"}) {
					t.Fatalf("nested override discarded existing stamps: %v", got[0])
				}
				got[0].VersionStamp["FIRST"] = "caller change"
			}
			if tc.name == "replaced command" && !reflect.DeepEqual(got[0].BuildCommand, []string{"make", "release"}) {
				t.Fatalf("command was not replaced: %v", got[0])
			}
			if !reflect.DeepEqual(cat.Components["x"].VersionStamp, map[string]string{"FIRST": "kept", "SECOND": "old"}) || !reflect.DeepEqual(cat.Components["x"].BuildCommand, []string{"make"}) {
				t.Fatal("applying overrides mutated the source catalog")
			}
		})
	}
}
