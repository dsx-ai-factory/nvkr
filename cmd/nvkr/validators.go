// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/urfave/cli/v2"
)

func cmdValidators() *cli.Command {
	discovery := func(describe bool) *cli.Command {
		name := "list"
		if describe {
			name = "describe"
		}
		usage, argsUsage := "list provisional validators without execution", " "
		if describe {
			usage, argsUsage = "describe one exact validator ID without execution", "VALIDATOR-ID"
		}
		return &cli.Command{Name: name, Usage: usage, ArgsUsage: argsUsage, Flags: []cli.Flag{&cli.StringFlag{Name: "catalog", Required: true, Usage: "path to the versioned validator catalog index"}}, Action: func(c *cli.Context) error {
			if (!describe && c.Args().Present()) || (describe && c.Args().Len() != 1) {
				return e.New(e.CodeUsage, "invalid validator discovery arguments")
			}
			sdk := client.New()
			var result any
			if describe {
				description, err := sdk.DescribeValidator(c.Context, c.String("catalog"), c.Args().First())
				if err != nil {
					return err
				}
				result = description
			} else {
				descriptions, err := sdk.ListValidators(c.Context, c.String("catalog"))
				if err != nil {
					return err
				}
				result = descriptions
			}
			return writeValidatorJSON(c, result)
		}}
	}
	return &cli.Command{Name: "validators", Hidden: true, Usage: "discover and run provisional imported CLI diagnostics", Subcommands: []*cli.Command{
		discovery(false), discovery(true),
		{Name: "run", Usage: "execute an explicit versioned local-plan profile (unqualified)", Flags: []cli.Flag{&cli.StringFlag{Name: "profile", Required: true, Usage: "path to the versioned local-plan validation profile"}, &cli.BoolFlag{Name: "keep", Usage: "retain diagnostic workspaces after execution"}, &cli.StringFlag{Name: "workspace-root", Usage: "parent directory for private diagnostic workspaces"}}, Action: func(c *cli.Context) error {
			if c.Args().Present() {
				return e.New(e.CodeUsage, "validators run accepts flags only")
			}
			invocation, err := client.New().PrepareValidatorProfile(c.Context, c.String("profile"), client.ValidatorRuntime{Keep: c.Bool("keep"), WorkspaceRoot: c.String("workspace-root"), Stdout: c.App.Writer, Stderr: c.App.ErrWriter})
			if err != nil {
				return err
			}
			return invocation.Run(c.Context)
		}},
	}}
}
func writeValidatorJSON(c *cli.Context, value any) error {
	raw, err := canonical.Marshal(value)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding validator metadata")
	}
	if _, err := fmt.Fprintln(c.App.Writer, string(raw)); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing validator metadata")
	}
	return nil
}
