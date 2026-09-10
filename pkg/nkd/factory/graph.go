package factory

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func graphJSON(graph ArtifactGraph) ([]byte, error) {
	if err := graph.Validate(); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(graph)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func graphTable(graph ArtifactGraph) (string, error) {
	if err := graph.Validate(); err != nil {
		return "", err
	}
	var out bytes.Buffer
	fmt.Fprintln(&out, "NODE\tKIND\tNETWORK\tINPUT ARTIFACTS\tOUTPUT ARTIFACTS")
	for _, node := range graph.Nodes {
		fmt.Fprintf(&out, "%s\t%s\t%s\t%s\t%s\n", node.ID, node.Kind, node.Network,
			orDash(strings.Join(node.Inputs, ",")), strings.Join(node.Outputs, ","))
	}
	return out.String(), nil
}

func graphDOT(graph ArtifactGraph) (string, error) {
	if err := graph.Validate(); err != nil {
		return "", err
	}
	artifacts := make(map[string]Artifact, len(graph.Artifacts))
	for _, artifact := range graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	var out bytes.Buffer
	fmt.Fprintln(&out, "digraph nkd_factory {")
	fmt.Fprintln(&out, "  rankdir=LR;")
	fmt.Fprintln(&out, "  node [shape=box,fontname=Helvetica];")
	for _, node := range graph.Nodes {
		fmt.Fprintf(&out, "  %s [label=%s];\n", dotID(node.ID), dotQuote(node.ID+"\\n"+node.Kind))
	}
	for _, node := range graph.Nodes {
		for _, input := range node.Inputs {
			artifact := artifacts[input]
			fmt.Fprintf(&out, "  %s -> %s [label=%s];\n", dotID(artifact.Producer), dotID(node.ID), dotQuote(input))
		}
	}
	fmt.Fprintln(&out, "}")
	return out.String(), nil
}

func graphMermaid(graph ArtifactGraph) (string, error) {
	if err := graph.Validate(); err != nil {
		return "", err
	}
	artifacts := make(map[string]Artifact, len(graph.Artifacts))
	for _, artifact := range graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	var out bytes.Buffer
	fmt.Fprintln(&out, "flowchart LR")
	for _, node := range graph.Nodes {
		fmt.Fprintf(&out, "  %s[\"%s<br/>%s\"]\n", mermaidID(node.ID), mermaidText(node.ID), mermaidText(node.Kind))
	}
	for _, node := range graph.Nodes {
		for _, input := range node.Inputs {
			artifact := artifacts[input]
			fmt.Fprintf(&out, "  %s -->|%s| %s\n", mermaidID(artifact.Producer), mermaidText(input), mermaidID(node.ID))
		}
	}
	return out.String(), nil
}

func RenderGraph(graph ArtifactGraph, format string) ([]byte, error) {
	switch format {
	case "json":
		return graphJSON(graph)
	case "table":
		value, err := graphTable(graph)
		return []byte(value), err
	case "dot":
		value, err := graphDOT(graph)
		return []byte(value), err
	case "mermaid":
		value, err := graphMermaid(graph)
		return []byte(value), err
	default:
		return nil, e.New(e.CodeUsage, "unknown factory graph format %q (table|json|dot|mermaid)", format)
	}
}

func dotID(value string) string { return "n_" + shortStableName(value) }

func dotQuote(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	return "\"" + value + "\""
}

func mermaidID(value string) string { return "n_" + shortStableName(value) }

func mermaidText(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;")
	return replacer.Replace(value)
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
