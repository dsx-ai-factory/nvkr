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
		Usage:       "run matching upstream CLI tests against a supplied executable (unqualified diagnostic)",
		Description: "contract/cli use Docker regardless of the Plan Environment. bootstrap uses the existing upstream rootful native Linux AMD64 host and fixture preparation.",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "plan"}, &cli.StringFlag{Name: "artifact"},
			&cli.StringFlag{Name: "output"}, &cli.StringFlag{Name: "suite", Value: "contract"},
			&cli.StringFlag{Name: "bootstrap-controller-image", Usage: "supporting controller image already in upstream containerd (bootstrap only)"},
			&cli.BoolFlag{Name: "keep"}, &cli.DurationFlag{Name: "timeout", Value: 30 * time.Minute, Usage: "per-container timeout; bootstrap defaults to a 45m execution budget on the upstream host"},
		},
		Action: func(c *cli.Context) error {
			if c.Args().Present() || c.Duration("timeout") <= 0 || c.String("plan") == "" || c.String("artifact") == "" {
				return e.New(e.CodeUsage, "artifact-test requires flags only and a positive timeout")
			}
			timeout := c.Duration("timeout")
			if c.String("suite") == "bootstrap" && !c.IsSet("timeout") {
				timeout = 45 * time.Minute
			}
			return factory.TestCLIArtifact(c.Context, factory.ArtifactTestOptions{
				Plan: c.String("plan"), Artifact: c.String("artifact"), Output: c.String("output"), Suite: c.String("suite"),
				ControllerImage: c.String("bootstrap-controller-image"), Keep: c.Bool("keep"), Timeout: timeout, Stdout: c.App.Writer, Stderr: c.App.ErrWriter,
			})
		},
	}
}
