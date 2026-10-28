// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/urfave/cli/v2"
)

func cmdArtifactTestInputs() *cli.Command {
	return &cli.Command{
		Name: "artifact-test-inputs", Hidden: true,
		Usage: "describe validated CLI test inputs; grants no artifact or Run authority",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "plan", Required: true},
			&cli.StringFlag{Name: "output", Usage: "standalone CLI output name"},
		},
		Action: func(c *cli.Context) error {
			if c.Args().Present() {
				return e.New(e.CodeUsage, "artifact-test-inputs accepts flags only")
			}
			inputs, err := factory.LoadCLIArtifactTestInputs(c.Context, c.String("plan"), c.String("output"))
			if err != nil {
				return err
			}
			raw, err := canonical.Marshal(inputs)
			if err != nil {
				return e.Wrap(e.CodeInternal, err, "encoding CLI test inputs")
			}
			return writePlanView(c.Context, append(raw, '\n'))
		},
	}
}
