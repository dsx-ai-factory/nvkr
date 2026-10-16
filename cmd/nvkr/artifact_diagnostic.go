// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/urfave/cli/v2"
)

func cmdArtifactTest() *cli.Command {
	return &cli.Command{Name: "artifact-test", Hidden: true,
		Usage:       "run matching upstream CLI tests against a supplied executable (Docker-only, unqualified diagnostic)",
		Description: "Uses Docker regardless of the runtime selected by the Plan Environment.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "plan"}, &cli.StringFlag{Name: "artifact"},
			&cli.StringFlag{Name: "output"}, &cli.StringFlag{Name: "suite", Value: "contract"},
			&cli.BoolFlag{Name: "keep"}, &cli.DurationFlag{Name: "timeout", Value: 30 * time.Minute, Usage: "per-container timeout"},
		},
		Action: func(c *cli.Context) error {
			if c.Args().Present() || c.Duration("timeout") <= 0 || c.String("plan") == "" || c.String("artifact") == "" {
				return e.New(e.CodeUsage, "artifact-test requires flags only and a positive timeout")
			}
			return factory.TestCLIArtifact(c.Context, factory.ArtifactTestOptions{
				Plan: c.String("plan"), Artifact: c.String("artifact"), Output: c.String("output"), Suite: c.String("suite"),
				Keep: c.Bool("keep"), Timeout: c.Duration("timeout"), Stdout: c.App.Writer, Stderr: c.App.ErrWriter,
			})
		},
	}
}
