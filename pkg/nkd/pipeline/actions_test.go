package pipeline

import (
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func testActionPins() map[string]catalog.ActionPin {
	pins := map[string]catalog.ActionPin{}
	for name, version := range map[string]string{"checkout": "v7.0.1", "upload-artifact": "v7.0.1", "download-artifact": "v8.0.1", "setup-go": "v7.0.0"} {
		pins[name] = catalog.ActionPin{Repo: "actions/" + name, Version: version, SHA: strings.Repeat("a", 40)}
	}
	return pins
}

func TestGitHubActionAdmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]catalog.ActionPin)
		code   e.Code
	}{
		{"supported", func(map[string]catalog.ActionPin) {}, ""},
		{"missing", func(p map[string]catalog.ActionPin) { delete(p, "checkout") }, e.CodeUnpinnedSource},
		{"mutable commit", func(p map[string]catalog.ActionPin) { v := p["checkout"]; v.SHA = "v7"; p["checkout"] = v }, e.CodeUnpinnedSource},
		{"moving version", func(p map[string]catalog.ActionPin) { v := p["checkout"]; v.Version = "v7"; p["checkout"] = v }, e.CodeInvalidCatalog},
		{"future major", func(p map[string]catalog.ActionPin) { v := p["checkout"]; v.Version = "v8.0.0"; p["checkout"] = v }, e.CodeEngineCapability},
		{"wrong repository", func(p map[string]catalog.ActionPin) {
			v := p["checkout"]
			v.Repo = "example/checkout"
			p["checkout"] = v
		}, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pins := testActionPins()
			tc.mutate(pins)
			refs, err := ResolveGitHubActions(pins)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				if refs.Checkout != pins["checkout"].Uses() {
					t.Fatal("reference differs from input pin")
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("got %v; want %s", err, tc.code)
			}
			if refs != (GitHubActionRefs{}) {
				t.Fatal("failed admission returned partial references")
			}
		})
	}
}
