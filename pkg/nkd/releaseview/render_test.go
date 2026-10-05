package releaseview

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestRenderKeepsOutputEvidenceMembershipAndIntegrityScope(t *testing.T) {
	description := delivery.PublicationDescription{
		Reference: "registry.test/releases@sha256:" + strings.Repeat("a", 64), RootDigest: "sha256:" + strings.Repeat("a", 64),
		Outputs: []delivery.PublicationOutput{
			{Output: delivery.Output{Name: "first", Kind: "executable", Reference: "registry.test/first@sha256:exact"}},
			{Output: delivery.Output{Name: "second", Kind: "executable"}},
		},
		Evidence: []delivery.PublicationEvidence{
			{Name: "shared", Binding: delivery.EvidenceBinding{Outputs: []string{"first", "second"}, SubjectDigest: "sha256:exact", Stage: "build-output", SubjectKind: "file"}},
			{Name: "other", Binding: delivery.EvidenceBinding{Outputs: []string{"second"}}},
		},
	}
	for _, format := range []string{"", "text", "json", "mermaid"} {
		t.Run(format, func(t *testing.T) {
			raw, err := Render(description, Options{Format: format, Output: "first"})
			if err != nil || !strings.Contains(string(raw), "first") || !strings.Contains(string(raw), "signatures and policy not verified") {
				t.Fatalf("view loses scope or selected output: %s, %v", raw, err)
			}
			if strings.Contains(string(raw), "other") {
				t.Fatalf("filtered view includes unrelated evidence: %s", raw)
			}
			if format == "json" {
				var got view
				if err := json.Unmarshal(raw, &got); err != nil || len(got.Outputs) != 1 || len(got.Evidence) != 1 || len(got.Evidence[0].Binding.Outputs) != 2 || got.TotalOutputs != 2 || got.SelectedOutput != "first" {
					t.Fatalf("filtered JSON hides shared membership or full scope: %s, %v", raw, err)
				}
			}
		})
	}
	if len(description.Outputs) != 2 || len(description.Evidence) != 2 {
		t.Fatal("render mutated its input")
	}
	for _, options := range []Options{{Format: "html"}, {Output: "unknown"}, {Output: "bad\x1b[31m"}} {
		raw, err := Render(description, options)
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeUsage || raw != nil {
			t.Fatalf("bad presentation did not fail closed: %q, %v", raw, err)
		}
	}
}

func TestRenderEscapesUntrustedLabels(t *testing.T) {
	description := delivery.PublicationDescription{Reference: "registry\x1b[31m", Outputs: []delivery.PublicationOutput{{Output: delivery.Output{Name: "x\"]\nattack-->root<svg>"}}}}
	for _, format := range []string{"text", "json", "mermaid"} {
		raw, err := Render(description, Options{Format: format})
		if err != nil || strings.ContainsRune(string(raw), '\x1b') || strings.Contains(string(raw), "\nattack-->root") {
			t.Fatalf("unsafe %s rendering: %q, %v", format, raw, err)
		}
	}
}
