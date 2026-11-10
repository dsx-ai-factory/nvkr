// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

// definitionInitCommand prints a starter executable Definition. The lifecycle
// owner admits the selection; this adapter maps flags and prints the YAML.
func definitionInitCommand() *cli.Command {
	return &cli.Command{
		Name:  "init",
		Usage: "print a starter executable Definition for existing catalog components",
		Flags: append(commandPresentationFlags(),
			&cli.StringFlag{Name: "name", Required: true, Usage: "Definition and output name"},
			&cli.StringFlag{Name: "catalog", Required: true, Usage: "catalog directory (written into the Definition as an absolute path)"},
			&cli.StringFlag{Name: "platform", Required: true, Usage: "executable target, for example linux/arm64"},
			&cli.StringSliceFlag{Name: "component", Required: true, Usage: "catalog component to bundle (repeatable)"}),
		Action: func(c *cli.Context) error {
			if err := rejectUnusedArguments(c, "catalog"); err != nil {
				return err
			}
			view, err := selectCommandPresentation(c)
			if err != nil {
				return err
			}
			authored, err := lifecycle.InitDefinition(c.Context, lifecycle.DefinitionInit{
				Name: c.String("name"), Catalog: c.String("catalog"), Platform: c.String("platform"), Components: c.StringSlice("component"),
			})
			if err != nil {
				return showCommandResult(c, view, commandDisplay{Title: "Definition init", Note: "Definition was not written."}, err)
			}
			raw, err := lifecycle.RenderDefinition(authored)
			if err != nil {
				return err
			}
			if view.tui {
				output := authored.Outputs[0]
				return showCommandResult(c, view, commandDisplay{
					Title: "Definition init", Note: "Printed nothing; run without --ui=tui to redirect the YAML.",
					Fields: commandFields("Name", authored.Metadata.Name, "Catalog", authored.Catalog.Base, "Platform", output.Platform, "Components", strings.Join(output.Components, ", ")),
				}, nil)
			}
			_, err = os.Stdout.Write(raw)
			return err
		},
	}
}
