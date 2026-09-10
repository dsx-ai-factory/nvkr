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
	return &cli.App{
		Name:                 "nkdctl",
		Usage:                "define, run, and verify immutable release workflows",
		Version:              version + " (" + commit + ")",
		HideHelpCommand:      true,
		EnableBashCompletion: true,
		Commands: []*cli.Command{
			cmdDefinition(),
			cmdRun(),
			// The Definition -> Run -> Release lifecycle is the normal surface (ADR-17).
			// Low-level execution and evidence tools are hidden from the product workflow.
			cmdRecipe(), // Name "resolve" (alias "recipe")
			hideCommand(cmdBuild()),
			hideCommand(cmdBundle()),
			hideCommand(cmdAssemble()),
			hideCommand(cmdAttest()),
			hideCommand(cmdVerify()),
			hideCommand(cmdPublish()),
			cmdCatalog(),
			hideCommand(cmdActions()),
			cmdRelease(),
			hideCommand(cmdProfile()),
			hideCommand(cmdFactory()),
			// Execution helpers and focused diagnostics:
			cmdFetch(),
			cmdBakeOff(),
			cmdSBOM(),
			cmdDisk(),
			cmdAICR(),
			cmdExecNode(),
			cmdTransportPack(),
			cmdTransportUnpack(),
			cmdSandboxExec(),
		},
	}
}

func hideCommand(command *cli.Command) *cli.Command {
	command.Hidden = true
	return command
}
