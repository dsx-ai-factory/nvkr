// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Command nvkr exposes the Definition -> Run -> Release lifecycle.
// Commands own flags, presentation, and coded exit behavior; pkg/nvkr/lifecycle
// owns the public workflow and delegates to domain authority modules.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/urfave/cli/v2"
)

// version and commit are stamped at build time via -ldflags (see the Makefile).
// They default to dev values for `go run` / `go build` without the Makefile.
var (
	version = "dev"
	commit  = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newApp().RunContext(ctx, os.Args); err != nil {
		// The final diagnostic must not replace the selected exit status when
		// its log reader has closed. No command work remains at this point.
		signal.Ignore(syscall.SIGPIPE)
		fmt.Fprintln(os.Stderr, "nvkr: "+err.Error())
		os.Exit(factory.GitHubStepExitCode(err))
	}
}

func newApp() *cli.App {
	var workCancel context.CancelFunc
	app := &cli.App{
		Name:                 "nvkr",
		Usage:                "define, run, and verify immutable release workflows",
		Version:              version + " (" + commit + ")",
		HideHelpCommand:      true,
		EnableBashCompletion: true,
		OnUsageError:         codedUsageError,
		// A downstream process owner passes scheduling data separately from
		// authored inputs. WithDeadline preserves any earlier caller deadline.
		Flags: []cli.Flag{&cli.StringFlag{Name: "work-deadline", Hidden: true, EnvVars: []string{"NVKR_WORK_DEADLINE"}}},
		Before: func(c *cli.Context) error {
			value := c.String("work-deadline")
			if value == "" {
				return nil
			}
			deadline, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return nvkrerr.Wrap(nvkrerr.CodeUsage, err, "work deadline must be RFC3339")
			}
			if !deadline.After(time.Now()) {
				return nvkrerr.New(nvkrerr.CodeTimeout, "work deadline has expired")
			}
			c.Context, workCancel = context.WithDeadline(c.Context, deadline)
			if err := c.Err(); err != nil {
				return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "work canceled before command execution")
			}
			return nil
		},
		After: func(_ *cli.Context) error {
			if workCancel != nil {
				workCancel()
			}
			return nil
		},
		Action: func(c *cli.Context) error {
			if c.Args().Present() {
				return nvkrerr.New(nvkrerr.CodeUsage, "unknown command %q", c.Args().First())
			}
			return cli.ShowAppHelp(c)
		},
		Commands: []*cli.Command{
			cmdDefinition(),
			cmdRun(),
			cmdValidate(),
			cmdValidators(),
			// The Definition -> Run -> Release lifecycle is the normal surface (ADR-17).
			// Low-level execution and evidence tools are hidden from the product workflow.
			cmdResolve(),
			hideCommand(cmdBuild()),
			hideCommand(cmdAssemble()),
			hideCommand(cmdAttest()),
			hideCommand(cmdVerify()),
			hideCommand(cmdPublish()),
			cmdCatalog(),
			cmdRelease(),
			hideCommand(cmdProfile()),
			hideCommand(cmdFactory()),
			// Execution helpers and focused diagnostics:
			cmdFetch(),
			cmdBakeOff(),
			cmdSBOM(),
			cmdDisk(),
			cmdExecNode(),
			cmdTransportPack(),
			cmdTransportUnpack(),
			cmdSandboxExec(),
		},
	}
	for _, command := range app.Commands {
		configureCommandUsage(command)
	}
	return app
}

func codedUsageError(_ *cli.Context, err error, _ bool) error {
	return nvkrerr.Wrap(nvkrerr.CodeUsage, err, "invalid command usage")
}

func configureCommandUsage(command *cli.Command) {
	command.OnUsageError = codedUsageError
	if command.Action == nil && len(command.Subcommands) > 0 {
		command.Action = func(c *cli.Context) error {
			if c.Args().Present() {
				return nvkrerr.New(nvkrerr.CodeUsage, "unknown %s subcommand %q", c.Command.Name, c.Args().First())
			}
			return cli.ShowSubcommandHelp(c)
		}
	}
	for _, child := range command.Subcommands {
		configureCommandUsage(child)
	}
}

func hideCommand(command *cli.Command) *cli.Command {
	command.Hidden = true
	return command
}
