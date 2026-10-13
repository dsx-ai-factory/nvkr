// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

func releaseExportCmd() *cli.Command {
	return &cli.Command{
		Name: "export", Usage: "export a versioned release description (metadata only; all outputs and platforms)",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "format", Value: "json", Usage: "description encoding: json"},
			&cli.StringFlag{Name: "reference", Required: true, Usage: "published release repository@sha256:digest"},
			&cli.StringFlag{Name: "layout", Usage: "inspect a cached publication layout with --reference"},
			&cli.DurationFlag{Name: "timeout", Value: 10 * time.Minute, Usage: "bound the entire export"},
			&cli.BoolFlag{Name: "anonymous", Usage: "disable ambient registry credentials"},
			&cli.BoolFlag{Name: "plain-http", Usage: "use HTTP for a local registry"},
		},
		Action: func(c *cli.Context) error {
			if c.NArg() != 0 || c.String("format") != "json" || c.Duration("timeout") <= 0 {
				return e.New(e.CodeUsage, "release export takes flags only, format json and a positive timeout")
			}
			raw, err := lifecycle.ExportRelease(c.Context, c.String("reference"), lifecycle.InspectionOptions{
				LayoutPath: c.String("layout"), Timeout: c.Duration("timeout"),
				Registry: delivery.RegistryOptions{Anonymous: c.Bool("anonymous"), PlainHTTP: c.Bool("plain-http")},
			})
			if err != nil {
				return err
			}
			if _, err := c.App.Writer.Write(raw); err != nil {
				return e.Wrap(e.CodeUnavailable, err, "writing release description")
			}
			return nil
		},
	}
}
