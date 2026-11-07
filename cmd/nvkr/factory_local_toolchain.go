// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/urfave/cli/v2"
)

func cmdLocalToolchainPreparation() *cli.Command {
	return &cli.Command{Name: "local-toolchain-scopes", Hidden: true, Usage: "compile or verify exact local toolchain substitutions", Flags: []cli.Flag{
		&cli.StringFlag{Name: "plan", Required: true},
		&cli.StringFlag{Name: "bindings", Required: true},
		&cli.StringFlag{Name: "arch", Required: true},
		&cli.StringFlag{Name: "image"},
		&cli.StringFlag{Name: "selected-plan"},
	}, Action: func(c *cli.Context) error {
		if c.Args().Present() {
			return e.New(e.CodeUsage, "local-toolchain-scopes accepts no positional arguments")
		}
		scopes, err := factory.DescribeLocalToolchainPreparation(c.Context, c.String("plan"), c.String("bindings"), c.String("arch"), c.String("image"), c.String("selected-plan"))
		if err != nil {
			return err
		}
		if err := json.NewEncoder(c.App.Writer).Encode(scopes); err != nil {
			return e.Wrap(e.CodeInternal, err, "write local toolchain scopes")
		}
		return nil
	}}
}
