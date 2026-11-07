// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/urfave/cli/v2"
)

func cmdRegistryCheck() *cli.Command {
	return &cli.Command{
		Name: "registry-check", Hidden: true,
		Usage: "report observed registry API/authentication without probe writes",
		Flags: []cli.Flag{&cli.StringFlag{Name: "repository"}, &cli.BoolFlag{Name: "anonymous"}, &cli.BoolFlag{Name: "plain-http"}},
		Action: func(c *cli.Context) error {
			if c.String("repository") == "" || c.Args().Present() {
				return e.New(e.CodeUsage, "registry-check requires --repository and no positional arguments")
			}
			report, err := delivery.CheckRegistry(c.Context, c.String("repository"), delivery.RegistryOptions{
				Anonymous: c.Bool("anonymous"), PlainHTTP: c.Bool("plain-http"),
			})
			if outputErr := json.NewEncoder(c.App.Writer).Encode(report); outputErr != nil {
				return e.Wrap(e.CodeInternal, outputErr, "writing registry diagnostic report")
			}
			return err
		},
	}
}
