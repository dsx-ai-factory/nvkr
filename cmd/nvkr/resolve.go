// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"sort"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/urfave/cli/v2"
)

// validateResolveOutput admits only the formats listed in the --output flag's usage
// text. Any other value is a usage error, so an unknown value can never fall through
// to the default renderer in the output switch below.
func validateResolveOutput(format string) error {
	switch format {
	case "ir", "canonical", "nodes", "digest", "components":
		return nil
	}
	return e.New(e.CodeUsage, "unknown resolve output %q (use ir, canonical, nodes, digest, or components)", format)
}

func cmdResolve() *cli.Command {
	return &cli.Command{
		Name:  "resolve",
		Usage: "resolve selected producers and print the pinned build graph",
		Flags: append(append(selectionFlags(), commandPresentationFlags()...),
			&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Value: "ir",
				Usage: "ir | canonical | nodes | digest | components"},
		),
		Action: func(c *cli.Context) error {
			// Admit the output format before resolution and before the TUI branch, so a
			// misspelling fails closed instead of silently rendering the default view.
			if err := validateResolveOutput(c.String("output")); err != nil {
				return err
			}
			view, err := selectCommandPresentation(c)
			if err != nil {
				return err
			}
			resolution, err := resolveBuildResult(c)
			if err != nil {
				return showCommandResult(c, view, commandDisplay{Title: "Resolved producers", Note: "Resolution failed; no execution."}, err)
			}
			g, digest := resolution.Graph, resolution.BuildDigest
			if view.tui {
				return showCommandResult(c, view, resolutionDisplay(g, digest), nil)
			}
			switch c.String("output") {
			case "digest":
				fmt.Println(digest)
			case "canonical":
				// ADR-01 §4: the CANONICAL form is what is hashed and stored on the wire
				// (file bytes == hashed bytes) — the RFC-8785 JCS content-identity view.
				// The default `ir` (YAML) output is the display-only --pretty view.
				view, err := g.DigestView()
				if err != nil {
					return err
				}
				b, err := canonical.Marshal(view)
				if err != nil {
					return err
				}
				os.Stdout.Write(b)
				fmt.Println()
			case "nodes":
				// Full canonical execution graph consumed by `diff --against` and
				// provider runners. Unlike `canonical`, this intentionally retains
				// transport locators and execution requirements and is not buildDigest
				// identity material.
				b, err := g.CanonicalJSON()
				if err != nil {
					return err
				}
				os.Stdout.Write(b)
				fmt.Println()
			case "components":
				rows, err := componentRows(resolution)
				if err != nil {
					return err
				}
				fmt.Printf("buildDigest: %s\n\n", digest)
				for _, row := range rows {
					fmt.Println(row)
				}
			default:
				// Only "ir" reaches this branch: validateResolveOutput rejects every other value.
				b, err := g.MarshalDeterministic()
				if err != nil {
					return err
				}
				os.Stdout.Write(b)
				fmt.Printf("\n# buildDigest: %s\n", digest)
			}
			return nil
		},
	}
}

func componentRows(resolution *client.Resolution) ([]string, error) {
	producers := make(map[string]ir.Node, len(resolution.Resolved.Components))
	for _, node := range resolution.Graph.Nodes {
		if node.Kind == ir.KindBuild || node.Kind == ir.KindPull {
			producers[node.Component] = node
		}
	}
	rows := make([]string, 0, len(resolution.Resolved.Components))
	for _, component := range resolution.Resolved.Components {
		node, ok := producers[component.Name]
		if !ok {
			return nil, e.New(e.CodeInternal, "selected component %q has no build or pull node", component.Name)
		}
		mode, pins := catalog.ProducerModeSource, "toolchain="+node.Toolchain
		switch {
		case node.Kind == ir.KindPull:
			mode, pins = catalog.ProducerModeAcquisition, "digest="+node.Digest
			if node.ImageParentDigest != "" {
				pins += " parent=" + node.ImageParentDigest
			}
			pins += archivePin(node)
		case node.BuilderType == ir.BuilderPinnedTool:
			mode = catalog.ProducerModePinnedTool
			pins = "tool=" + node.Tool + " executable=" + node.Digest + archivePin(node) + " toolchain=" + node.Toolchain
		}
		rows = append(rows, fmt.Sprintf("%-26s %-13s %-11s %s", component.Name, node.OutputType, mode, pins))
	}
	sort.Strings(rows)
	return rows, nil
}

func archivePin(node ir.Node) string {
	if archive := node.ExecutableArchive; archive != nil && archive.Digest != node.Digest {
		return " archive=" + archive.Digest
	}
	return ""
}
