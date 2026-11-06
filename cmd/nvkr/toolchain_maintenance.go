// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/toolchainmaintenance"
	"github.com/urfave/cli/v2"
)

func cmdToolchainMaintenance() *cli.Command {
	return &cli.Command{
		Name: "toolchain-maintenance", Hidden: true,
		Usage: "internal native candidate image maintenance adapter",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "operation", Required: true},
			&cli.StringFlag{Name: "root", Value: "."},
			&cli.StringFlag{Name: "run"},
			&cli.StringFlag{Name: "directory"},
			&cli.StringFlag{Name: "arch"},
			&cli.StringFlag{Name: "source"},
		},
		Action: func(c *cli.Context) error {
			if c.Args().Present() {
				return e.New(e.CodeUsage, "toolchain maintenance accepts no positional arguments")
			}
			var result any
			var err error
			switch c.String("operation") {
			case "describe":
				result, err = toolchainmaintenance.Describe(c.Context, c.String("directory"), c.String("arch"), c.String("source"))
			case "admit":
				result, err = toolchainmaintenance.Admit(c.Context, c.String("root"), c.String("run"))
			case "publish":
				err = toolchainmaintenance.Publish(c.Context, c.String("root"), c.String("run"), c.String("directory"))
			case "verify":
				result, err = toolchainmaintenance.Verify(c.Context, c.String("root"), c.String("run"))
			case "repin":
				result, err = toolchainmaintenance.Repin(c.Context, c.String("root"), c.String("run"))
			default:
				return e.New(e.CodeUsage, "unknown toolchain maintenance operation")
			}
			if err != nil {
				return err
			}
			if result == nil {
				return nil
			}
			if err := json.NewEncoder(c.App.Writer).Encode(result); err != nil {
				return e.Wrap(e.CodeInternal, err, "write maintenance result")
			}
			return nil
		},
	}
}
