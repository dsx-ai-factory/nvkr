// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
	"github.com/urfave/cli/v2"
)

func releaseExportCmd() *cli.Command {
	return releaseExportCmdWithOperation(lifecycle.ExportRelease)
}

type releaseExportOperation func(context.Context, string, lifecycle.InspectionOptions) ([]byte, error)

func releaseExportCmdWithOperation(export releaseExportOperation) *cli.Command {
	return &cli.Command{
		Name: "export", Usage: "export a versioned release description (metadata only; all outputs and platforms)",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "format", Value: "json", Usage: "description encoding: json"},
			&cli.StringFlag{Name: "reference", Required: true, Usage: "published release repository@sha256:digest"},
			&cli.StringFlag{Name: "layout", Usage: "inspect a cached publication layout with --reference"},
			&cli.DurationFlag{Name: "timeout", Value: 10 * time.Minute, Usage: "bound the entire export"},
			&cli.BoolFlag{Name: "anonymous", Usage: "disable ambient registry credentials"},
			&cli.BoolFlag{Name: "plain-http", Usage: "use HTTP for a local registry"},
		}, commandPresentationFlags()...),
		Action: func(c *cli.Context) (returnedErr error) {
			view, err := selectCommandPresentation(c)
			if err != nil {
				return err
			}
			if c.NArg() != 0 || c.String("format") != "json" || c.Duration("timeout") <= 0 {
				return e.New(e.CodeUsage, "release export takes flags only, format json and a positive timeout")
			}
			shown := false
			defer func() {
				if returnedErr != nil && view.tui && !shown {
					returnedErr = showCommandResult(c, view, releaseFailureDisplay("Release description export"), returnedErr)
				}
			}()
			raw, err := export(c.Context, c.String("reference"), lifecycle.InspectionOptions{
				LayoutPath: c.String("layout"), Timeout: c.Duration("timeout"),
				Registry: delivery.RegistryOptions{Anonymous: c.Bool("anonymous"), PlainHTTP: c.Bool("plain-http")},
			})
			if err != nil {
				return err
			}
			if view.tui {
				// Decode the owning description only for presentation. This neither
				// authenticates the bytes nor opens a second inspection operation.
				var description releaseview.Description
				if err := canonical.ValidateJSONDocument(raw); err != nil {
					return e.New(e.CodeInternal, "invalid release description for presentation")
				}
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&description); err != nil {
					return e.New(e.CodeInternal, "decoding release description for presentation")
				}
				shown = true
				return showCommandResult(c, view, releaseDescriptionDisplay(description), nil)
			}
			if _, err := c.App.Writer.Write(raw); err != nil {
				return e.Wrap(e.CodeUnavailable, err, "writing release description")
			}
			return nil
		},
	}
}
