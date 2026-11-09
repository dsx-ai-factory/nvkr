// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package planview

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// GraphOptions chooses linked descriptive graphs, never an executable input.
type GraphOptions struct {
	View   string
	Format string
	// Output, when set, focuses the graph on one required lifecycle output and
	// its upstream dependency closure.
	Output string
}

func (o GraphOptions) Validate() error {
	switch o.View {
	case "complete", "producers", "artifacts":
	default:
		return e.New(e.CodeUsage, "unknown graph view %q (use complete, producers, or artifacts)", o.View)
	}
	switch o.Format {
	case "mermaid", "dot", "json":
	default:
		return e.New(e.CodeUsage, "unknown graph format %q (use mermaid, dot, or json)", o.Format)
	}
	return nil
}

// Graph is a display projection. Keys preserve exact Plan references; renderer IDs
// are independently escaped and cannot become diagram directives.
type Graph struct {
	SchemaVersion string   `json:"schemaVersion"`
	PlanDigest    string   `json:"planDigest"`
	View          string   `json:"view"`
	Output        string   `json:"output,omitempty"`
	Authority     string   `json:"authority"`
	Vertices      []Vertex `json:"vertices"`
	Edges         []Edge   `json:"edges"`
}

type Vertex struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Reference string `json:"reference"`
	Build     string `json:"build,omitempty"`
}

type Edge struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
}

// Graph emits every vertex and edge in the chosen view. Complete joins all IR
// dependencies to operations by exact producer reference, then follows artifacts
// through evidence, packaging, publication, signing and verification.
func (d *Document) Graph(o GraphOptions) ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	if err := d.valid(); err != nil {
		return nil, err
	}
	graph, err := d.graphFor(o.View, o.Output)
	if err != nil {
		return nil, err
	}
	switch o.Format {
	case "json":
		raw, err := canonical.Marshal(graph)
		if err != nil {
			return nil, e.Wrap(e.CodeInternal, err, "serializing graph view")
		}
		return raw, nil
	case "dot":
		return []byte(graph.dot()), nil
	default:
		return []byte(graph.mermaid()), nil
	}
}

// GraphSnapshot returns the owning typed display projection without rendering or
// parsing a diagram. Its collections are newly allocated and grant no authority.
func (d *Document) GraphSnapshot(view string) (Graph, error) {
	return d.GraphSnapshotFor(GraphOptions{View: view, Format: "json"})
}

// GraphSnapshotFor is GraphSnapshot with the optional output focus applied.
func (d *Document) GraphSnapshotFor(o GraphOptions) (Graph, error) {
	if err := o.Validate(); err != nil {
		return Graph{}, err
	}
	if err := d.valid(); err != nil {
		return Graph{}, err
	}
	return d.graphFor(o.View, o.Output)
}

// graphFor builds the view, then keeps only the selected output's upstream
// dependency closure. The Plan was admitted whole first, so an unselected
// branch cannot hide corruption, and the Plan and its digest never change.
// The closure follows incoming edges from the output, so it includes the
// artifact, its producing operations, their consumed artifacts (including other
// outputs' artifacts, such as an image a chart packages) and producers. Each
// retained artifact also keeps its output slots, but those are not followed
// further, so sibling outputs are not pulled back in. The selected output also
// keeps its publication path: the operation that consumes its artifact into the
// artifact routed by output-<name>, that artifact and the route. Other inputs of
// that operation are not followed.
func (d *Document) graphFor(view, output string) (Graph, error) {
	graph := d.graph(view)
	if output == "" {
		return graph, nil
	}
	start := "output/" + output
	complete := graph
	if view != "complete" {
		complete = d.graph("complete")
	}
	known := false
	for _, vertex := range complete.Vertices {
		if vertex.Key == start {
			known = true
			break
		}
	}
	if !known {
		return Graph{}, e.New(e.CodeUsage, "unknown output %q: the plan declares no such lifecycle output", output)
	}
	incoming := make(map[string][]string)
	outgoing := make(map[string][]string)
	kinds := make(map[string]string, len(complete.Vertices))
	for _, vertex := range complete.Vertices {
		kinds[vertex.Key] = vertex.Kind
	}
	for _, edge := range complete.Edges {
		incoming[edge.To] = append(incoming[edge.To], edge.From)
		outgoing[edge.From] = append(outgoing[edge.From], edge.To)
	}
	keep := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		key := queue[0]
		queue = queue[1:]
		for _, from := range incoming[key] {
			if !keep[from] {
				keep[from] = true
				queue = append(queue, from)
			}
		}
	}
	for key := range keep {
		if kinds[key] != "artifact" {
			continue
		}
		for _, to := range outgoing[key] {
			if kinds[to] == "output" {
				keep[to] = true
			}
		}
	}
	for _, vertex := range complete.Vertices {
		if vertex.Key == start {
			for _, op := range outgoing["artifact/"+vertex.Reference] {
				if kinds[op] != "operation" {
					continue
				}
				for _, published := range outgoing[op] {
					for _, route := range outgoing[published] {
						if kinds[published] == "artifact" && route == "route/output-"+output {
							keep[op], keep[published], keep[route] = true, true, true
						}
					}
				}
			}
		}
	}
	vertices := graph.Vertices[:0:0]
	for _, vertex := range graph.Vertices {
		if keep[vertex.Key] {
			vertices = append(vertices, vertex)
		}
	}
	edges := graph.Edges[:0:0]
	for _, edge := range graph.Edges {
		if keep[edge.From] && keep[edge.To] {
			edges = append(edges, edge)
		}
	}
	graph.Vertices, graph.Edges, graph.Output = vertices, edges, output
	return graph, nil
}

func (d *Document) graph(view string) Graph {
	graph := Graph{SchemaVersion: "nvkr.dev/plan-graph-view/v1", PlanDigest: d.digest, View: view, Authority: "description-only", Vertices: []Vertex{}, Edges: []Edge{}}
	addVertex := func(key, kind, label, reference, build string) {
		graph.Vertices = append(graph.Vertices, Vertex{Key: key, Kind: kind, Label: label, Reference: reference, Build: build})
	}
	addEdge := func(from, to, relation string) {
		graph.Edges = append(graph.Edges, Edge{From: from, To: to, Relation: relation})
	}
	if view != "artifacts" {
		for _, build := range d.plan.Builds {
			for _, node := range build.Graph.Nodes {
				ref := build.Name + "/" + node.ID
				addVertex("producer/"+ref, "producer", ref+" ("+node.Kind+")", node.ID, build.Name)
				for _, need := range node.Needs {
					addEdge("producer/"+build.Name+"/"+need, "producer/"+ref, "requires")
				}
			}
		}
	}
	if view != "producers" {
		for _, node := range d.plan.Graph.Nodes {
			addVertex("operation/"+node.ID, "operation", node.ID+" ("+node.Kind+")", node.ID, node.BuildTarget)
			if view == "complete" && node.IRNode != "" {
				addEdge("producer/"+node.BuildTarget+"/"+node.IRNode, "operation/"+node.ID, "instruction")
			}
			for _, input := range node.Inputs {
				addEdge("artifact/"+input, "operation/"+node.ID, "consumed by")
			}
			for _, output := range node.Outputs {
				addEdge("operation/"+node.ID, "artifact/"+output, "produces")
			}
		}
		for _, artifact := range d.plan.Graph.Artifacts {
			addVertex("artifact/"+artifact.ID, "artifact", artifact.ID+" ("+artifact.Kind+")", artifact.ID, artifact.BuildTarget)
		}
		for _, output := range d.plan.LifecycleOutputs {
			addVertex("output/"+output.Name, "output", output.Name+" (required "+output.Kind+")", output.ArtifactID, output.Name)
			addEdge("artifact/"+output.ArtifactID, "output/"+output.Name, "fulfills slot")
		}
		for _, route := range d.plan.Routes {
			target := d.plan.Targets[route.Target]
			addVertex("route/"+route.Name, "route", route.Name+" -> "+target.Ref, route.Target, "")
			addEdge("artifact/"+route.Input, "route/"+route.Name, "publication route")
		}
	}
	sort.Slice(graph.Vertices, func(i, j int) bool { return graph.Vertices[i].Key < graph.Vertices[j].Key })
	sort.Slice(graph.Edges, func(i, j int) bool {
		a, b := graph.Edges[i], graph.Edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.Relation < b.Relation
	})
	return graph
}

// scope marks a graph that is a filtered view of the original Plan.
func (g Graph) scope() string {
	if g.Output == "" {
		return ""
	}
	return " filtered to output " + g.Output
}

func (g Graph) dot() string {
	var out strings.Builder
	out.WriteString("digraph plan {\n  rankdir=LR;\n")
	fmt.Fprintf(&out, "  label=%s;\n", dotLabel("Planned "+g.View+" graph"+g.scope()+"; description only; "+g.PlanDigest))
	indices := make(map[string]string, len(g.Vertices))
	for i, vertex := range g.Vertices {
		id := fmt.Sprintf("v%d", i)
		indices[vertex.Key] = id
		shape := "box"
		if vertex.Kind == "artifact" {
			shape = "ellipse"
		}
		if vertex.Kind == "output" {
			shape = "hexagon"
		}
		fmt.Fprintf(&out, "  %s [label=%s,shape=%s];\n", id, dotLabel(vertex.Label), shape)
	}
	for _, edge := range g.Edges {
		fmt.Fprintf(&out, "  %s -> %s [label=%s];\n", indices[edge.From], indices[edge.To], dotLabel(edge.Relation))
	}
	out.WriteString("}\n")
	return out.String()
}

// dotLabel quotes a DOT escString, whose escape rules differ from Go strings.
// Newlines split rendered lines; other nonprinting runes are shown as literal
// Unicode escapes. Their doubled backslashes prevent DOT substitutions or a
// dropped escape prefix while keeping the source safe to print in a terminal.
func dotLabel(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, char := range value {
		switch char {
		case '\\', '"':
			out.WriteByte('\\')
			out.WriteRune(char)
		case '\n':
			out.WriteString(`\n`)
		default:
			if strconv.IsPrint(char) {
				out.WriteRune(char)
			} else if char <= 0xffff {
				fmt.Fprintf(&out, `\\u%04x`, char)
			} else {
				fmt.Fprintf(&out, `\\U%08x`, char)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

func (g Graph) mermaid() string {
	var out strings.Builder
	fmt.Fprintf(&out, "flowchart LR\n  %%%% Planned %s graph%s; description only; %s\n", g.View, mermaidLabel(g.scope()), g.PlanDigest)
	indices := make(map[string]string, len(g.Vertices))
	for i, vertex := range g.Vertices {
		id := fmt.Sprintf("v%d", i)
		indices[vertex.Key] = id
		fmt.Fprintf(&out, "  %s[\"%s\"]:::%s\n", id, mermaidLabel(vertex.Label), vertex.Kind)
	}
	for _, edge := range g.Edges {
		fmt.Fprintf(&out, "  %s -->|%s| %s\n", indices[edge.From], mermaidLabel(edge.Relation), indices[edge.To])
	}
	out.WriteString("  classDef producer fill:#e8f0fe,stroke:#1a73e8\n  classDef operation fill:#e6f4ea,stroke:#188038\n  classDef artifact fill:#fef7e0,stroke:#b06000\n  classDef output fill:#f3e8fd,stroke:#9334e6\n  classDef route fill:#fce8e6,stroke:#c5221f\n")
	return out.String()
}

func mermaidLabel(value string) string {
	var out strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(" ._/-", r) {
			out.WriteRune(r)
		} else {
			fmt.Fprintf(&out, "#%d;", r)
		}
	}
	return out.String()
}
