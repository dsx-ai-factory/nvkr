// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	client "github.com/dsx-ai-factory/nvkr/client/v1"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/urfave/cli/v2"
)

func selectionFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "data", Usage: "explicit catalog directory"},
		&cli.StringSliceFlag{Name: "component", Usage: "exact component ID to build (repeatable)"},
		&cli.StringFlag{Name: "platform", Usage: "target platform: linux/amd64 | linux/arm64"},
	}
}

func providerFor(c *cli.Context) catalog.DataProvider {
	if d := c.String("data"); d != "" {
		return catalog.Dir(d)
	}
	return nil
}

// resolveBuild selects exact catalog producers and compiles their pinned graph.
func resolveBuild(c *cli.Context) (*buildgraph.Resolved, string, error) {
	if c.String("data") == "" {
		return nil, "", e.New(e.CodeUsage, "build resolution requires --data <catalog-directory>")
	}
	resolution, err := client.New(client.WithProvider(providerFor(c))).ResolveBuild(c.Context, client.ResolveRequest{
		Components: c.StringSlice("component"), Platform: c.String("platform"),
	})
	if err != nil {
		return nil, "", err
	}
	return resolution.Resolved, resolution.BuildDigest, nil
}

// resolveGraph is the shared front half of every command: load + validate the
// catalog, select producers, compile the IR, and compute buildDigest.
func resolveGraph(c *cli.Context) (*ir.Graph, string, error) {
	res, _, err := resolveBuild(c)
	if err != nil {
		return nil, "", err
	}
	g, err := buildgraph.Compile(res)
	if err != nil {
		return nil, "", err
	}
	digest, err := g.Digest()
	if err != nil {
		return nil, "", err
	}
	return g, digest, nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
