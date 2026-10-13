// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/urfave/cli/v2"
)

func cmdResolve() *cli.Command {
	return &cli.Command{
		Name:  "resolve",
		Usage: "resolve selected producers and print the pinned build graph",
		Flags: append(append(selectionFlags(), commandPresentationFlags()...),
			&cli.StringFlag{Name: "output", Aliases: []string{"o"}, Value: "ir",
				Usage: "ir | canonical | nodes | digest | components"},
		),
		Action: func(c *cli.Context) error {
			view, err := selectCommandPresentation(c)
			if err != nil {
				return err
			}
			g, digest, err := resolveGraph(c)
			if err != nil {
				return showCommandResult(c, view, commandDisplay{Title: "Resolved producers", Note: "Resolution failed; no execution."}, err)
			}
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
				var comps []string
				for _, n := range g.Nodes {
					if n.Kind == ir.KindBuild {
						comps = append(comps, fmt.Sprintf("%-26s %-13s %s", n.Component, n.OutputType, n.Toolchain))
					}
				}
				sort.Strings(comps)
				fmt.Printf("buildDigest: %s\n\n", digest)
				for _, l := range comps {
					fmt.Println(l)
				}
			default:
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
