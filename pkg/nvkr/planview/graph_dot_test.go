// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package planview

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
)

func TestDOTLabelsUseDOTEscapes(t *testing.T) {
	for _, tc := range dotLabelCases() {
		t.Run(tc.name, func(t *testing.T) {
			graph := Graph{Vertices: []Vertex{{Key: "node", Label: tc.label}}}
			raw := graph.dot()
			if !strings.Contains(raw, "label="+tc.quoted+",shape=box") {
				t.Fatalf("incorrect DOT string for %q: %s", tc.label, raw)
			}
			for _, control := range []rune{'\a', '\v', '\t', '\r', '\x1b', '\u0085', '\u200b', '\u202e', '\U000e0001'} {
				if strings.ContainsRune(raw, control) {
					t.Fatalf("DOT contains raw display control %U", control)
				}
			}
		})
	}
}

func TestDOTLabelsRenderWithGraphvizWhenAvailable(t *testing.T) {
	dot, err := exec.LookPath("dot")
	if err != nil {
		t.Skip("Graphviz dot is not installed; DOT encoding contract is tested separately")
	}
	for _, tc := range dotLabelCases() {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, dot, "-Tsvg")
			command.Stdin = strings.NewReader((Graph{Vertices: []Vertex{{Key: "node", Label: tc.label}}}).dot())
			var diagnostics bytes.Buffer
			command.Stderr = &diagnostics
			raw, err := command.Output()
			if err != nil {
				t.Fatalf("Graphviz rejected DOT: %v, %s", err, diagnostics.String())
			}
			decoder := xml.NewDecoder(bytes.NewReader(raw))
			var labels []string
			nodes := 0
			for {
				token, err := decoder.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				start, ok := token.(xml.StartElement)
				if !ok || start.Name.Local != "g" {
					continue
				}
				for _, attribute := range start.Attr {
					if attribute.Name.Local != "class" || attribute.Value != "node" {
						continue
					}
					var node struct {
						Text []string `xml:"text"`
					}
					if err := decoder.DecodeElement(&node, &start); err != nil {
						t.Fatal(err)
					}
					nodes++
					labels = append(labels, node.Text...)
				}
			}
			if nodes != 1 || !reflect.DeepEqual(labels, tc.rendered) {
				t.Fatalf("Graphviz changed label or graph structure: nodes=%d labels=%q want=%q", nodes, labels, tc.rendered)
			}
		})
	}
}

func dotLabelCases() []struct {
	name, label, quoted string
	rendered            []string
} {
	return []struct {
		name, label, quoted string
		rendered            []string
	}{
		{"ordinary Unicode", "café 日本語", `"café 日本語"`, []string{"café 日本語"}},
		{"quotes", `say "hello"`, `"say \"hello\""`, []string{`say "hello"`}},
		{"DOT substitutions stay literal", `\N \G \E \T \H \L \n \l \r \`, `"\\N \\G \\E \\T \\H \\L \\n \\l \\r \\"`, []string{`\N \G \E \T \H \L \n \l \r \`}},
		{"newline", "first\nsecond", `"first\nsecond"`, []string{"first", "second"}},
		{"controls", "\a\v\t\r\x1b\u0085", `"\\u0007\\u000b\\u0009\\u000d\\u001b\\u0085"`, []string{`\u0007\u000b\u0009\u000d\u001b\u0085`}},
		{"format controls", "\u200b\u202e\U000e0001", `"\\u200b\\u202e\\U000e0001"`, []string{`\u200b\u202e\U000e0001`}},
		{"syntax injection", "evil\"];\nx -> y [label=\"injected", `"evil\"];\nx -> y [label=\"injected"`, []string{`evil"];`, `x -> y [label="injected`}},
	}
}

// Producer and artifact IDs are restricted to ASCII, but a canonical registry
// authority can contain Unicode. Exercise the complete Plan admission boundary
// so this is not solely a renderer test with an impossible producer name.
func TestDOTQuotesAdmittedUnicodePublicationRoute(t *testing.T) {
	// This presentation regression must not acquire catalogs or depend on Git credentials.
	t.Setenv("PATH", t.TempDir())
	// Compile the checked-in local catalog fixture through the public factory
	// boundary, with the Unicode authority as an authored publication input.
	plan := comparisonPlan(t, func(_ *catalog.Catalog, _ *definition.Definition, env *definition.Environment) {
		env.Publication = &definition.Publication{RepositoryPrefix: "registry\u200b.example/ns", Anonymous: true}
	})
	document := inspectPlan(t, plan)
	raw, err := document.Graph(GraphOptions{View: "complete", Format: "dot"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(string(raw), '\u200b') || !strings.Contains(string(raw), `registry\\u200b.example/ns/releases`) {
		t.Fatalf("admitted route lacks a visible DOT escape: %s", raw)
	}
	found := false
	for _, vertex := range document.graph("complete").Vertices {
		if vertex.Kind == "route" && strings.Contains(vertex.Label, "registry\u200b.example/ns/releases") {
			found = true
		}
	}
	if !found {
		t.Fatal("display encoding changed the underlying route label")
	}
}
