// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/urfave/cli/v2"
)

func cmdRegistryCheck() *cli.Command {
	return &cli.Command{
		Name: "registry-check", Hidden: true,
		Usage: "check registry API/authentication with NVKR_REGISTRY_USER/NVKR_CREDENTIAL, ambient credentials, or --anonymous; no probe writes",
		Flags: []cli.Flag{&cli.StringFlag{Name: "repository"}, &cli.BoolFlag{Name: "anonymous"}, &cli.BoolFlag{Name: "plain-http"}},
		Action: func(c *cli.Context) error {
			if c.String("repository") == "" || c.Args().Present() {
				return e.New(e.CodeUsage, "registry-check requires --repository and no positional arguments")
			}
			options := delivery.RegistryOptions{
				Anonymous: c.Bool("anonymous"), PlainHTTP: c.Bool("plain-http"),
			}
			if !options.Anonymous {
				options.Username, options.Password = os.Getenv("NVKR_REGISTRY_USER"), os.Getenv("NVKR_CREDENTIAL")
			}
			report, err := delivery.CheckRegistry(c.Context, c.String("repository"), options)
			var coded *e.Error
			if errors.As(err, &coded) && coded.Code == e.CodeUsage {
				return err
			}
			if outputErr := json.NewEncoder(c.App.Writer).Encode(report); outputErr != nil {
				if err != nil {
					return e.Wrap(runErrorCode(err), errors.Join(err, outputErr), "writing registry diagnostic report also failed")
				}
				return e.Wrap(e.CodeInternal, outputErr, "writing registry diagnostic report")
			}
			return err
		},
	}
}
