// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	client "github.com/dsx-ai-factory/nvkr/client/v1"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
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

// resolveBuildResult retains the SDK's resolved producers, compiled graph and
// digest together so command adapters do not compile the same inputs again.
func resolveBuildResult(c *cli.Context) (*client.Resolution, error) {
	if c.String("data") == "" {
		return nil, e.New(e.CodeUsage, "build resolution requires --data <catalog-directory>")
	}
	return client.New(client.WithProvider(providerFor(c))).ResolveBuild(c.Context, client.ResolveRequest{
		Components: c.StringSlice("component"), Platform: c.String("platform"),
	})
}

// resolveBuild projects the producer description and identity for callers that
// do not consume the compiled graph, including the qualified verifier seam.
func resolveBuild(c *cli.Context) (*buildgraph.Resolved, string, error) {
	resolution, err := resolveBuildResult(c)
	if err != nil {
		return nil, "", err
	}
	return resolution.Resolved, resolution.BuildDigest, nil
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
