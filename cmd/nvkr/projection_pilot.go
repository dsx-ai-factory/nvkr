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

// selectProjectionPilot is the only CLI route to a projection selection.
// Tests trap it to prove generated bootstrap invocations never reach it.
var selectProjectionPilot = projectionPilotContext

// projectionPilotContext selects this binary's reference projection plugin
// over the named transport for top-level generation. It is a diagnostic pilot
// selector: generated commands never pass --projection-pilot, so no generated
// stage reaches the plugin.
func projectionPilotContext(ctx context.Context, transport string) (context.Context, error) {
	adapter := factory.ProjectionAdapter{Transport: factory.ProjectionTransport(transport), Release: factoryGeneratorVersion()}
	switch adapter.Transport {
	case factory.ProjectionInProcess:
	case factory.ProjectionOutOfProcess:
		self, err := os.Executable()
		if err != nil {
			return nil, e.Wrap(e.CodeEngineCapability, err, "locating the projection pilot adapter")
		}
		adapter.Path, adapter.Args = self, []string{projectionAdapterCommand}
	default:
		return nil, e.New(e.CodeUsage, "--projection-pilot must be %s or %s", factory.ProjectionInProcess, factory.ProjectionOutOfProcess)
	}
	return factory.WithProjectionAdapter(ctx, adapter), nil
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
