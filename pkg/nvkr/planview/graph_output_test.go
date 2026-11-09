// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package planview

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGraphOutputFocusKeepsUpstreamClosureOnly(t *testing.T) {
	doc := inspectPlan(t, maintainedPlan(t))
	for _, view := range []string{"complete", "producers", "artifacts"} {
		for _, format := range []string{"json", "dot", "mermaid"} {
			raw, err := doc.Graph(GraphOptions{View: view, Format: format, Output: "controller-chart"})
			if err != nil {
				t.Fatalf("%s/%s: %v", view, format, err)
			}
			if format != "json" {
				if !strings.Contains(string(raw), "filtered to output controller-chart") {
					t.Fatalf("%s/%s output does not say it is filtered", view, format)
				}
				continue
			}
			var focused Graph
			if err := json.Unmarshal(raw, &focused); err != nil {
				t.Fatal(err)
			}
			full, err := doc.GraphSnapshot(view)
			if err != nil {
				t.Fatal(err)
			}
			if focused.Output != "controller-chart" || focused.PlanDigest != full.PlanDigest || focused.Authority != "description-only" {
				t.Fatalf("%s: filtered view lost its subject: %+v", view, focused)
			}
			if len(focused.Vertices) == 0 || len(focused.Vertices) >= len(full.Vertices) {
				t.Fatalf("%s: %d of %d vertices kept", view, len(focused.Vertices), len(full.Vertices))
			}
			fullVertices := map[string]Vertex{}
			for _, vertex := range full.Vertices {
				fullVertices[vertex.Key] = vertex
			}
			kept := map[string]bool{}
			for _, vertex := range focused.Vertices {
				if fullVertices[vertex.Key] != vertex {
					t.Fatalf("%s: vertex %s changed", view, vertex.Key)
				}
				kept[vertex.Key] = true
			}
			fullEdges := map[Edge]bool{}
			for _, edge := range full.Edges {
				fullEdges[edge] = true
			}
			for _, edge := range focused.Edges {
				if !kept[edge.From] || !kept[edge.To] || !fullEdges[edge] {
					t.Fatalf("%s: dangling or invented edge %+v", view, edge)
				}
			}
			if view != "producers" {
				if !kept["output/controller-chart"] || kept["output/nvkectl"] {
					t.Fatalf("%s: wrong output slots kept", view)
				}
			}
		}
	}
}

func TestGraphWithoutOutputIsUnchangedAndUnknownOutputIsUsageError(t *testing.T) {
	doc := inspectPlan(t, maintainedPlan(t))
	plain, err := doc.Graph(GraphOptions{View: "complete", Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plain, []byte(`"output":`)) {
		t.Fatal("unfiltered graph carries a filter marker")
	}
	for _, name := range []string{"nope", "Controller-Chart", "controller-chart/", "controller"} {
		raw, err := doc.Graph(GraphOptions{View: "complete", Format: "json", Output: name})
		assertCode(t, err, e.CodeUsage)
		if len(raw) != 0 {
			t.Fatalf("%q returned bytes before failing", name)
		}
	}
}

func TestGraphOutputFocusIsUpstreamClosedAndExcludesSiblingOutput(t *testing.T) {
	doc := inspectPlan(t, maintainedPlan(t))
	full, err := doc.GraphSnapshot("complete")
	if err != nil {
		t.Fatal(err)
	}
	focused, err := doc.GraphSnapshotFor(GraphOptions{View: "complete", Format: "json", Output: "controller-chart"})
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]Vertex{}
	for _, vertex := range focused.Vertices {
		kept[vertex.Key] = vertex
	}
	siblingArtifact := ""
	for _, vertex := range full.Vertices {
		if vertex.Key == "output/nvkectl" {
			siblingArtifact = "artifact/" + vertex.Reference
		}
	}
	if siblingArtifact == "" || kept[siblingArtifact].Key != "" {
		t.Fatalf("sibling output artifact %q missing from plan or kept in the focused view", siblingArtifact)
	}
	kinds := map[string]string{}
	for _, vertex := range full.Vertices {
		kinds[vertex.Key] = vertex.Kind
	}
	for _, edge := range full.Edges {
		to, ok := kept[edge.To]
		if !ok || to.Kind == "route" || to.Kind == "output" {
			continue
		}
		if _, ok := kept[edge.From]; !ok {
			t.Fatalf("%s (%s) is kept but its dependency %s (%s) is not", edge.To, to.Kind, edge.From, kinds[edge.From])
		}
	}
}
