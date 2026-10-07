package planview

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestGraphsCoverEveryDeclaredNodeEdgeAndJoin(t *testing.T) {
	plan := maintainedPlan(t)
	doc := inspectPlan(t, plan)
	for _, view := range []string{"complete", "producers", "artifacts"} {
		t.Run(view, func(t *testing.T) {
			raw, err := doc.Graph(GraphOptions{View: view, Format: "json"})
			if err != nil {
				t.Fatal(err)
			}
			var graph Graph
			if err := json.Unmarshal(raw, &graph); err != nil {
				t.Fatal(err)
			}
			if graph.PlanDigest != doc.digest || graph.Authority != "description-only" {
				t.Fatal("graph lost descriptive subject")
			}
			vertices := map[string]Vertex{}
			for _, v := range graph.Vertices {
				if _, exists := vertices[v.Key]; exists {
					t.Fatalf("duplicate %s", v.Key)
				}
				vertices[v.Key] = v
			}
			edges := map[Edge]bool{}
			for _, edge := range graph.Edges {
				if _, ok := vertices[edge.From]; !ok {
					t.Fatalf("missing source %s", edge.From)
				}
				if _, ok := vertices[edge.To]; !ok {
					t.Fatalf("missing destination %s", edge.To)
				}
				if edges[edge] {
					t.Fatalf("duplicate edge %+v", edge)
				}
				edges[edge] = true
			}
			vertexCount, edgeCount := 0, 0
			requireEdge := func(from, to, relation string) {
				t.Helper()
				edgeCount++
				if !edges[Edge{From: from, To: to, Relation: relation}] {
					t.Fatalf("missing %s -> %s (%s)", from, to, relation)
				}
			}
			if view != "artifacts" {
				for _, build := range plan.Builds {
					for _, node := range build.Graph.Nodes {
						vertexCount++
						if _, ok := vertices["producer/"+build.Name+"/"+node.ID]; !ok {
							t.Fatalf("missing instruction %s", node.ID)
						}
						for _, need := range node.Needs {
							requireEdge("producer/"+build.Name+"/"+need, "producer/"+build.Name+"/"+node.ID, "requires")
						}
					}
				}
			}
			if view != "producers" {
				vertexCount += len(plan.Graph.Nodes) + len(plan.Graph.Artifacts) + len(plan.LifecycleOutputs) + len(plan.Routes)
				for _, node := range plan.Graph.Nodes {
					for _, input := range node.Inputs {
						requireEdge("artifact/"+input, "operation/"+node.ID, "consumed by")
					}
					for _, output := range node.Outputs {
						requireEdge("operation/"+node.ID, "artifact/"+output, "produces")
					}
					if view == "complete" && node.IRNode != "" {
						requireEdge("producer/"+node.BuildTarget+"/"+node.IRNode, "operation/"+node.ID, "instruction")
					}
				}
				for _, output := range plan.LifecycleOutputs {
					requireEdge("artifact/"+output.ArtifactID, "output/"+output.Name, "fulfills slot")
				}
				for _, route := range plan.Routes {
					requireEdge("artifact/"+route.Input, "route/"+route.Name, "publication route")
				}
				// Real maintained Helm binding has a dataflow edge from the CWE package.
				found := false
				for _, edge := range graph.Edges {
					if edge.From == "artifact/package/cwe" && strings.Contains(edge.To, "controller-chart") {
						found = true
					}
				}
				if !found {
					t.Fatal("chart/image dependency is missing")
				}
			}
			if len(vertices) != vertexCount || len(edges) != edgeCount {
				t.Fatalf("unexplained graph: vertices=%d/%d edges=%d/%d", len(vertices), vertexCount, len(edges), edgeCount)
			}
			for _, format := range []string{"dot", "mermaid", "json"} {
				first, err := doc.Graph(GraphOptions{View: view, Format: format})
				if err != nil {
					t.Fatal(err)
				}
				second, err := doc.Graph(GraphOptions{View: view, Format: format})
				if err != nil || !bytes.Equal(first, second) {
					t.Fatalf("unstable %s: %v", format, err)
				}
			}
		})
	}
}

func TestDiagramEscapingCannotInjectSyntax(t *testing.T) {
	graph := Graph{View: "complete", PlanDigest: "sha256:test", Vertices: []Vertex{{Key: "arbitrary", Kind: "route", Label: "evil\"]\nclick v0 \"https://evil.invalid\" <script>&;#\\"}}}
	mermaid := graph.mermaid()
	if strings.Contains(mermaid, "\nclick") || strings.Contains(mermaid, "<script>") || strings.Contains(mermaid, "[\"evil\"]") {
		t.Fatalf("Mermaid injection: %s", mermaid)
	}
	if !strings.Contains(mermaid, "#34;") || !strings.Contains(mermaid, "#10;") || !strings.Contains(mermaid, "#60;") {
		t.Fatalf("missing entity escapes: %s", mermaid)
	}
	dot := graph.dot()
	if strings.Contains(dot, "\nclick") || strings.Count(dot, "label=") != 2 {
		t.Fatalf("DOT injection: %s", dot)
	}
}
