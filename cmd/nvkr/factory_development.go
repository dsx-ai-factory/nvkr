// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/urfave/cli/v2"
)

func cmdFactoryCleanupDevelopment() *cli.Command {
	return &cli.Command{
		Name: "cleanup-development", Hidden: true,
		Usage: "inspect expired development registry packages; --apply deletes admitted packages",
		Flags: []cli.Flag{&cli.StringFlag{Name: "repository"}, &cli.BoolFlag{Name: "apply"}},
		Action: func(c *cli.Context) error {
			if c.String("repository") == "" || c.Args().Present() {
				return e.New(e.CodeUsage, "cleanup-development requires --repository owner/repo and no positional arguments")
			}
			report, err := development.Cleanup(c.Context, c.String("repository"), os.Getenv("NVKR_DEV_REGISTRY_CLEANUP"), c.Bool("apply"))
			if outputErr := json.NewEncoder(c.App.Writer).Encode(report); outputErr != nil && err == nil {
				return e.Wrap(e.CodeInternal, outputErr, "writing development cleanup report")
			}
			return err
		},
	}
}
