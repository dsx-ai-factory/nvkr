// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/urfave/cli/v2"
)

const projectionAdapterCommand = "projection-adapter"

// projectionPilotContext selects this binary's hidden reference adapter for
// top-level generation. It is a diagnostic pilot selector: generated commands
// never pass --projection-pilot, so no generated stage launches the adapter.
func projectionPilotContext(ctx context.Context) (context.Context, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "locating the projection pilot adapter")
	}
	return factory.WithProjectionAdapter(ctx, factory.ProjectionAdapter{
		Path: self, Args: []string{projectionAdapterCommand}, Release: factoryGeneratorVersion()}), nil
}

func cmdProjectionAdapter() *cli.Command {
	return &cli.Command{Name: projectionAdapterCommand, Hidden: true,
		Usage: "serve one out-of-process GitHub projection pilot request over stdin and stdout",
		Action: func(c *cli.Context) error {
			if c.Args().Present() {
				return e.New(e.CodeUsage, "%s does not accept positional arguments", projectionAdapterCommand)
			}
			return factory.ServeProjectionAdapter(c.Context, factoryGeneratorVersion(), os.Stdin, os.Stdout)
		}}
}
