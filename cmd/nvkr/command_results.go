// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/planview"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
)

func commandFields(pairs ...string) []progress.Field {
	fields := make([]progress.Field, 0, len(pairs)/2)
	for index := 0; index+1 < len(pairs); index += 2 {
		fields = append(fields, progress.Field{Label: pairs[index], Value: pairs[index+1]})
	}
	return fields
}

func definitionDisplay(definition lifecycle.Definition, digest string) commandDisplay {
	display := commandDisplay{Title: "Definition", Note: "Validated declaration; no execution or release acceptance.",
		Fields: commandFields("Name", definition.Metadata.Name, "Definition digest", digest)}
	for _, output := range definition.Outputs {
		platforms := append([]string(nil), output.Platforms...)
		if output.Platform != "" {
			platforms = append(platforms, output.Platform)
		}
		sort.Strings(platforms)
		display.Items = append(display.Items, progress.Item{ID: output.Name, Name: output.Name, Kind: output.Format, State: "declared",
			Fields: commandFields("Platforms", strings.Join(platforms, ", "), "Components", strings.Join(output.Components, ", "))})
	}
	return display
}

func resolutionDisplay(graph *ir.Graph, digest string) commandDisplay {
	display := commandDisplay{Title: "Resolved producers", Note: "Pinned graph description; no execution or observed output bytes.", Fields: commandFields("Build digest", digest)}
	for _, node := range graph.Nodes {
		display.Items = append(display.Items, progress.Item{ID: node.ID, Name: node.ID, Kind: node.Kind, State: "resolved", Dependencies: node.Needs,
			Fields: commandFields("Component", node.Component, "Platform", node.Platform, "Output type", node.OutputType, "Source commit", node.SourceCommit, "Content digest", node.Digest)})
	}
	return display
}

func graphDisplay(graph planview.Graph) commandDisplay {
	display := commandDisplay{Title: "Planned outputs", Note: "Plan loaded; execution and verification were not performed.",
		Fields: commandFields("Plan digest", graph.PlanDigest, "View", graph.View, "Links", fmt.Sprint(len(graph.Edges)))}
	if graph.View == "producers" {
		display.Title = "Planned producers"
	}
	dependencies := make(map[string][]string)
	for _, edge := range graph.Edges {
		dependencies[edge.To] = append(dependencies[edge.To], edge.From)
	}
	for _, vertex := range graph.Vertices {
		// Labels may contain publication transport locations. Exact logical keys,
		// references and build names suffice for safe dependency navigation.
		name := vertex.Reference
		if vertex.Kind == "producer" && vertex.Build != "" {
			name = vertex.Build + " / " + vertex.Reference
		}
		if vertex.Kind == "output" {
			name = vertex.Build
		}
		display.Items = append(display.Items, progress.Item{ID: vertex.Key, Name: name, Kind: vertex.Kind, State: "planned", Primary: vertex.Kind == "output", Dependencies: dependencies[vertex.Key],
			Fields: commandFields("Reference", vertex.Reference, "Build", vertex.Build)})
	}
	return display
}

func comparisonDisplay(comparison *planview.Comparison) commandDisplay {
	display := commandDisplay{Title: "Plan comparison", Note: "Logical input changes; no observed output-byte or release acceptance claim. Raw changed values are omitted.",
		Fields: commandFields("Before", comparison.Before, "After", comparison.After, "Changed builds", fmt.Sprint(len(comparison.Builds)), "Unchanged builds", fmt.Sprint(comparison.UnchangedBuilds), "Changed fields", fmt.Sprint(len(comparison.Changes)))}
	for _, build := range comparison.Builds {
		state := "changed"
		if build.Before == "" {
			state = "added"
		} else if build.After == "" {
			state = "removed"
		}
		display.Items = append(display.Items, progress.Item{ID: "build/" + build.Name, Name: build.Name, Kind: "build", State: state, Fields: commandFields("Before digest", build.Before, "After digest", build.After)})
	}
	categories := make(map[string]int)
	for _, change := range comparison.Changes {
		categories[change.Category]++
	}
	var names []string
	for category := range categories {
		names = append(names, category)
	}
	sort.Strings(names)
	for _, category := range names {
		display.Items = append(display.Items, progress.Item{ID: "category/" + category, Name: category, Kind: "changed fields", State: "changed", Fields: commandFields("Count", fmt.Sprint(categories[category]))})
	}
	return display
}
