// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

// definitionInitCommand prints a starter Definition. The lifecycle
// owner admits the selection; this adapter maps flags and prints the YAML.
func definitionInitCommand() *cli.Command {
	return &cli.Command{
		Name:  "init",
		Usage: "print a starter Definition for existing catalog components",
		Flags: append(commandPresentationFlags(),
			// Not cli's Required flag: that check prints help to stdout, which a
			// redirect would write into the Definition file. The owner refuses a
			// missing flag with a coded error instead.
			&cli.StringFlag{Name: "environment", Usage: "validate against this Environment (default: build-only local policy)"},
			&cli.StringFlag{Name: "root", Value: ".", Usage: "checkout input root for the starter and subsequent run"},
			&cli.StringFlag{Name: "destination", Usage: "intended saved file inside root; YAML is printed to stdout (required)"},
			&cli.StringFlag{Name: "format", Value: "executable", Usage: "executable, oci, apt-repository, helm-oci, bundle-oci, disk-image, or debian-package"},
			&cli.StringFlag{Name: "apt-distribution", Usage: "distribution for apt-repository"},
			&cli.StringFlag{Name: "apt-component", Usage: "component for apt-repository"},
			&cli.StringFlag{Name: "name", Usage: "Definition and output name (required)"},
			&cli.StringFlag{Name: "catalog", Usage: "catalog directory, relative to the current directory and inside root (required)"},
			&cli.StringFlag{Name: "platform", Usage: "output target, for example linux/arm64 (required)"},
			&cli.StringSliceFlag{Name: "component", Usage: "catalog component to bundle; repeat for more (required)"}),
		Action: func(c *cli.Context) error {
			if c.Args().Present() {
				return e.New(e.CodeUsage, "definition init accepts flags only; unexpected argument %q (use --name, --catalog, --platform and --component)", c.Args().First())
			}
			view, err := selectCommandPresentation(c)
			if err != nil {
				return err
			}
			authored, err := lifecycle.InitDefinition(c.Context, lifecycle.DefinitionInit{
				Environment: c.String("environment"), Root: c.String("root"), Destination: c.String("destination"), Format: c.String("format"), APTDistribution: c.String("apt-distribution"), APTComponent: c.String("apt-component"),
				Name: c.String("name"), Catalog: c.String("catalog"), Platform: c.String("platform"), Components: c.StringSlice("component"),
			})
			if err != nil {
				return showCommandResult(c, view, commandDisplay{Title: "Definition init", Note: "Definition was not written."}, err)
			}
			raw, err := lifecycle.RenderDefinition(authored)
			if err != nil {
				return e.Wrap(e.CodeInternal, err, "rendering the admitted Definition")
			}
			if view.tui {
				output := authored.Outputs[0]
				return showCommandResult(c, view, commandDisplay{
					Title: "Definition init", Note: "Printed nothing; run with --ui=plain to print the YAML.",
					Fields: commandFields("Name", authored.Metadata.Name, "Destination", c.String("destination"), "Format", output.Format, "Catalog", authored.Catalog.Base, "Platform", output.Platform, "Components", strings.Join(output.Components, ", ")),
				}, nil)
			}
			if _, err := os.Stdout.Write(raw); err != nil {
				return e.Wrap(e.CodeInternal, err, "writing the Definition to stdout")
			}
			return nil
		},
	}
}
