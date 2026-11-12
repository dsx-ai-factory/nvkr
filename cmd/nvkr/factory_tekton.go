// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/tektonrun"
	"github.com/urfave/cli/v2"
)

func cmdFactoryTekton() *cli.Command {
	command := &cli.Command{Name: "tekton", Hidden: true, Usage: "execute an authenticated generated Tekton task"}
	for _, operation := range []string{"prepare", "execute", "smoke", "qualify", "finalize"} {
		var flags []cli.Flag
		for _, name := range []string{"stage", "plan-parts", "execution", "run-name", "run-uid", "task-run", "task-uid", "outcome", "carrier-digest"} {
			flags = append(flags, &cli.StringFlag{Name: name, Required: true})
		}
		command.Subcommands = append(command.Subcommands, &cli.Command{Name: operation, Flags: flags, Action: func(c *cli.Context) error {
			if c.Args().Present() {
				return e.New(e.CodeUsage, "Tekton tasks accept only generated named arguments")
			}
			return tektonrun.Execute(c.Context, factory.TektonTaskOptions{Operation: operation, Stage: c.String("stage"), PlanParts: c.String("plan-parts"), Execution: c.String("execution"), RunName: c.String("run-name"), RunUID: c.String("run-uid"), TaskRun: c.String("task-run"), TaskUID: c.String("task-uid"), Outcome: c.String("outcome"), CarrierDigest: c.String("carrier-digest"), Stdout: c.App.Writer, Stderr: c.App.ErrWriter})
		}})
	}
	return command
}
