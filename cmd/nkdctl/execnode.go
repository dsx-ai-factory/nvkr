// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/urfave/cli/v2"
)

// cmdExecNode executes one instruction from an exact admitted IR bundle for
// qualified graph diagnostics and low-level helper callers. --launch delegates
// isolation to the shared container launcher. Maintained Runs use the complete
// Plan dispatcher instead. Shared mechanics do not establish reproduction.
func cmdExecNode() *cli.Command {
	return &cli.Command{
		Name:      "exec-node",
		Hidden:    true, // internal graph helper; public Runs use factory execute-node
		Usage:     "execute one IR node (the node-execution helper mode; --launch orchestrates the container itself)",
		ArgsUsage: "<node.json | nodes.json>",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "node", Usage: "select this node id from a full IR graph file (nodes.json)"},
			&cli.StringFlag{Name: "nodes-digest", Required: true, Usage: "exact sha256 of the authorized nodes.json execution document"},
			&cli.StringFlag{Name: "source-root", Usage: "where sources are checked out"},
			&cli.StringFlag{Name: "result", Usage: "atomically write the producer's ArtifactResult JSON for cross-job verification"},
			&cli.BoolFlag{Name: "launch", Usage: "ADR-18 §8: orchestrate the pinned toolchain container here (--network=none for builds) instead of assuming an engine-provided container"},
			&cli.StringFlag{Name: "runtime", Aliases: []string{"docker"}, Value: "docker", Usage: "with --launch: container runtime binary (docker | podman)"},
			&cli.BoolFlag{Name: "dry-run", Usage: "print the plan instead of executing"},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() != 1 {
				return e.New(e.CodeUsage, "exec-node takes exactly one <node.json|nodes.json>")
			}
			raw, err := readBoundedRegularInput(c.Context, c.Args().First(), maxRegularInputBytes)
			if err != nil {
				return e.Wrap(e.CodeInvalidCatalog, err, "reading node file")
			}
			if want := c.String("nodes-digest"); want != "" && pipeline.FileDigest(raw) != want {
				return e.New(e.CodePipelineDrift, "nodes.json digest mismatch: got %s want %s", pipeline.FileDigest(raw), want)
			}
			var n ir.Node
			if id := c.String("node"); id != "" {
				// Select one node from the full committed IR graph (the ADR-18 model: one
				// pinned nodes.json, jobs pick their node by id).
				var g ir.Graph
				if err := canonical.UnmarshalExact(raw, &g); err != nil {
					return e.Wrap(e.CodeInvalidCatalog, err, "parsing IR graph")
				}
				if err := g.Validate(); err != nil {
					return err
				}
				found := false
				for _, gn := range g.Nodes {
					if gn.ID == id {
						n, found = gn, true
						break
					}
				}
				if !found {
					return e.New(e.CodeInvalidCatalog, "node %q not found in %s", id, c.Args().First())
				}
			} else if err := canonical.UnmarshalExact(raw, &n); err != nil {
				return e.Wrap(e.CodeInvalidCatalog, err, "parsing node JSON")
			}
			res, err := pipeline.Local{}.ExecNode(n, pipeline.RunOptions{
				Ctx: c.Context, SourceRoot: c.String("source-root"),
				Launch: c.Bool("launch"), Runtime: c.String("runtime"),
				DryRun: c.Bool("dry-run"), Stdout: os.Stdout, Stderr: os.Stderr,
			})
			if err != nil {
				return err
			}
			if res != nil {
				if path := c.String("result"); path != "" {
					b, err := canonical.Marshal(res)
					if err != nil {
						return e.Wrap(e.CodeInternal, err, "serializing exec-node result")
					}
					if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
						return e.Wrap(e.CodeInternal, err, "creating exec-node result directory")
					}
					if err := writeAtomic(c.Context, path, b, 0o644); err != nil {
						return err
					}
				}
				fmt.Printf("exec-node %s: %s = %s\n", n.Kind, res.Component, res.SHA256)
			}
			return nil
		},
	}
}
