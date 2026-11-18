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

// shippedValidatorCatalog resolves against the working directory, like other repository-relative defaults.
const shippedValidatorCatalog = "inputs/testing/validator-catalog"

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
		return &cli.Command{Name: name, Usage: usage, ArgsUsage: argsUsage, Flags: []cli.Flag{&cli.StringFlag{Name: "validator-catalog", Value: shippedValidatorCatalog, Usage: "validator catalog directory; a complete replacement for the shipped catalog"}}, Action: func(c *cli.Context) error {
			if (!describe && c.Args().Present()) || (describe && c.Args().Len() != 1) {
				return e.New(e.CodeUsage, "invalid validator discovery arguments")
			}
			if c.String("validator-catalog") == "" {
				return e.New(e.CodeUsage, "validators %s requires a non-empty --validator-catalog", name)
			}
			sdk := client.New()
			var result any
			if describe {
				description, err := sdk.DescribeValidator(c.Context, c.String("validator-catalog"), c.Args().First())
				if err != nil {
					return err
				}
				result = description
			} else {
				descriptions, err := sdk.ListValidators(c.Context, c.String("validator-catalog"))
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
		{Name: "run", Usage: "execute an explicit versioned local-plan profile (unqualified)", Flags: []cli.Flag{&cli.StringFlag{Name: "profile", Usage: "path to the versioned local-plan validation profile (required)"}, &cli.BoolFlag{Name: "keep", Usage: "retain diagnostic workspaces after execution"}, &cli.StringFlag{Name: "workspace-root", Usage: "parent directory for private diagnostic workspaces"}}, Action: func(c *cli.Context) error {
			if c.Args().Present() {
				return e.New(e.CodeUsage, "validators run accepts flags only")
			}
			if c.String("profile") == "" {
				return e.New(e.CodeUsage, "validators run requires --profile")
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
