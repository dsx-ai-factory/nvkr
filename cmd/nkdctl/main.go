// Command nkdctl exposes the Definition -> Run -> Release lifecycle.
// Commands own flags, presentation, and coded exit behavior; pkg/nkd/lifecycle
// owns the public workflow and delegates to domain authority modules.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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
		fmt.Fprintln(os.Stderr, "nkdctl: "+err.Error())
		os.Exit(nkderr.ExitCode(err))
	}
}

func newApp() *cli.App {
	app := &cli.App{
		Name:                 "nkdctl",
		Usage:                "define, run, and verify immutable release workflows",
		Version:              version + " (" + commit + ")",
		HideHelpCommand:      true,
		EnableBashCompletion: true,
		OnUsageError:         codedUsageError,
		Action: func(c *cli.Context) error {
			if c.Args().Present() {
				return nkderr.New(nkderr.CodeUsage, "unknown command %q", c.Args().First())
			}
			return cli.ShowAppHelp(c)
		},
		Commands: []*cli.Command{
			cmdDefinition(),
			cmdRun(),
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
	return nkderr.Wrap(nkderr.CodeUsage, err, "invalid command usage")
}

func configureCommandUsage(command *cli.Command) {
	command.OnUsageError = codedUsageError
	if command.Action == nil && len(command.Subcommands) > 0 {
		command.Action = func(c *cli.Context) error {
			if c.Args().Present() {
				return nkderr.New(nkderr.CodeUsage, "unknown %s subcommand %q", c.Command.Name, c.Args().First())
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
