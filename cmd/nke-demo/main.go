// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// nke-demo is a downstream local diagnostic command. NKD operations execute
// nkdctl, and NKE owns installation/provisioning; this CLI only maps flags.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/spf13/cobra"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	err := execute(ctx, os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(nkderr.ExitCode(err))
}
func execute(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	return executeRoot(ctx, newCommand(out, diagnostics, demoActions{
		run: nkedemo.Run, build: nkedemo.BuildArtifacts, prepare: nkedemo.PrepareInstaller, restore: nkedemo.Restore, load: nkedemo.LoadDemoFile,
	}), args)
}

// Actions stay typed library calls; injection lets command tests prove help,
// completion and invalid input do not reach configuration I/O or runtime work.
type demoActions struct {
	run     func(context.Context, nkedemo.Config) error
	build   func(context.Context, nkedemo.BuildConfig) (nkedemo.BuiltInputs, error)
	prepare func(context.Context, nkedemo.BuildConfig, nkedemo.Config) (nkedemo.Config, error)
	restore func(context.Context, string, io.Writer) error
	load    func(context.Context, string) (nkedemo.DemoFile, error)
}

func executeRoot(ctx context.Context, root *cobra.Command, args []string) error {
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	var coded *nkderr.Error
	if err != nil && !errors.As(err, &coded) {
		return nkderr.Wrap(nkderr.CodeUsage, err, "local demo command")
	}
	return err
}
func newCommand(out, diagnostics io.Writer, actions demoActions) *cobra.Command {
	var config string
	root := &cobra.Command{
		Use: "nke-demo", Short: "Build and deploy a trusted local ARM64 NKE diagnostic",
		SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return nkderr.New(nkderr.CodeUsage, "select e2e, run, build, restore or completion; use --help")
		},
	}
	root.SetOut(out)
	root.SetErr(diagnostics)
	root.PersistentFlags().StringVar(&config, "config", "", "explicit version-1 local-demo YAML file (changed flags override it)")
	root.PersistentFlags().Bool("verbose", false, "stream raw public build output in addition to stage boundaries")
	root.AddCommand(newRunCommand(out, actions, &config), newBuildCommand(out, actions, &config), newRestoreCommand(out, actions, &config), newE2ECommand(out, actions, &config))
	root.InitDefaultCompletionCmd()
	for _, command := range root.Commands() {
		if command.Name() == "completion" {
			// Cobra owns generation. A runnable group also validates its NoArgs
			// contract instead of silently ignoring an unknown shell name.
			command.RunE = func(cmd *cobra.Command, _ []string) error { return cmd.Help() }
		}
	}
	return root
}

func newRunCommand(out io.Writer, actions demoActions, config *string) *cobra.Command {
	flags := nkedemo.DemoFile{Version: 1, Run: nkedemo.RunFile{TimeoutSeconds: 3600}}
	command := &cobra.Command{
		Use: "run", Short: "Deploy exact completed Run outputs and test a real QCOW2 tenant", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateTimeoutFlag(cmd, flags.Run.TimeoutSeconds); err != nil {
				return err
			}
			selected, err := configuredFile(cmd, *config, flags, actions)
			if err != nil {
				return err
			}
			cfg, err := selected.RunConfig(out)
			if err != nil {
				return err
			}
			return runProgress(cmd.Context(), out, func(ctx context.Context, progress io.Writer) error {
				cfg.Progress = progress
				return actions.run(ctx, cfg)
			})
		},
	}
	addRunFlags(command, &flags.Run, true)
	return command
}
func newBuildCommand(out io.Writer, actions demoActions, config *string) *cobra.Command {
	flags := nkedemo.DemoFile{Version: 1, Build: nkedemo.BuildFile{Jobs: 1}}
	command := &cobra.Command{
		Use: "build", Short: "Build all ARM64 demo artifacts in one NKD Definition/Run", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selected, err := configuredFile(cmd, *config, flags, actions)
			if err != nil {
				return err
			}
			cfg, err := selected.BuildConfig(out)
			if err != nil {
				return err
			}
			verbose, _ := cmd.Flags().GetBool("verbose")
			return runBuildProgress(cmd.Context(), out, verbose, func(ctx context.Context, progress io.Writer) error {
				cfg.Progress = progress
				built, err := actions.build(ctx, cfg)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(progress, "Artifact builds complete; nkdctl: %s\nDeployment is separate: nke-demo run --help\n", built.NKDCTL)
				return err
			})
		},
	}
	addBuildFlags(command, &flags.Build)
	return command
}
func newRestoreCommand(out io.Writer, actions demoActions, config *string) *cobra.Command {
	flags := nkedemo.DemoFile{Version: 1}
	command := &cobra.Command{
		Use: "restore", Short: "Restore only recorded host-network controls after a crash", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			selected, err := configuredFile(cmd, *config, flags, actions)
			if err != nil {
				return err
			}
			work, err := selected.RestoreWork()
			if err != nil {
				return err
			}
			budget, cancel := context.WithTimeout(cmd.Context(), 8*time.Minute)
			defer cancel()
			return runProgress(budget, out, func(ctx context.Context, progress io.Writer) error {
				if err := actions.restore(ctx, work, progress); err != nil {
					return err
				}
				_, err := fmt.Fprintln(progress, "PASS: recorded local network controls restored; no tenant, VM or management deletion was attempted.")
				return err
			})
		},
	}
	command.Flags().StringVar(&flags.Restore.Work, "work", "", "original private Go demo workspace")
	return command
}
func newE2ECommand(out io.Writer, actions demoActions, config *string) *cobra.Command {
	flags := nkedemo.DemoFile{Version: 1, Build: nkedemo.BuildFile{Jobs: 1}, Run: nkedemo.RunFile{TimeoutSeconds: 3600}}
	command := &cobra.Command{
		Use: "e2e", Short: "Build one complete artifact Run from scratch, then deploy its exact outputs", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateTimeoutFlag(cmd, flags.Run.TimeoutSeconds); err != nil {
				return err
			}
			selected, err := configuredFile(cmd, *config, flags, actions)
			if err != nil {
				return err
			}
			build, run, err := selected.E2EConfig(out)
			if err != nil {
				return err
			}
			budget, cancel := context.WithTimeout(cmd.Context(), 8*time.Hour)
			defer cancel()
			verbose, _ := cmd.Flags().GetBool("verbose")
			return runBuildProgress(budget, out, verbose, func(ctx context.Context, progress io.Writer) error {
				build.Progress, run.Progress = progress, progress
				run, err = actions.prepare(ctx, build, run)
				if err != nil {
					return err
				}
				built, err := actions.build(ctx, build)
				if err != nil {
					return err
				}
				run.NKDCTL = built.NKDCTL
				run.Runs = built.Runs
				return actions.run(ctx, run)
			})
		},
	}
	addBuildFlags(command, &flags.Build)
	addRunFlags(command, &flags.Run, false)
	return command
}
func addBuildFlags(command *cobra.Command, cfg *nkedemo.BuildFile) {
	flags := command.Flags()
	flags.StringVar(&cfg.Repo, "repo", "", "native Linux repository path")
	flags.StringVar(&cfg.NKEClone, "nke-clone", "", "optional local source Git clone")
	flags.StringVar(&cfg.Attempt, "attempt", "", "fresh name for the combined artifact Run")
	flags.IntVar(&cfg.Jobs, "jobs", 1, "maximum concurrent dependency-ready NKD operations")
}
func addRunFlags(command *cobra.Command, cfg *nkedemo.RunFile, existing bool) {
	flags := command.Flags()
	checkoutHelp := "optional clean pinned installer checkout; e2e prepares it when omitted"
	if existing {
		checkoutHelp = "required clean pinned NKE installer checkout"
	}
	flags.StringVar(&cfg.NKECheckout, "nke-checkout", "", checkoutHelp)
	flags.StringVar(&cfg.Out, "out", "", "fresh private demo output directory")
	flags.IntVar(&cfg.TimeoutSeconds, "timeout", 3600, "tenant provisioning budget in seconds (300..3600)")
	if !existing {
		return
	}
	flags.StringVar(&cfg.NKDCTL, "nkdctl", "", "native nkdctl executable")
	flags.StringVar(&cfg.Runs.OS, "os-run", "", "completed OS attempt")
	flags.StringVar(&cfg.Runs.Components, "components-run", "", "completed components attempt")
	flags.StringVar(&cfg.Runs.Providers, "provider-run", "", "completed provider attempt (components Run is valid)")
	flags.StringVar(&cfg.Runs.Site, "site-run", "", "completed site attempt")
	flags.StringVar(&cfg.Runs.Tools, "tools-run", "", "completed installer-tools attempt")
	flags.StringVar(&cfg.Runs.Profiles, "profiles-run", "", "completed profiles attempt")
	flags.StringVar(&cfg.Runs.Simulator, "simulator-run", "", "completed simulator attempt")
}
func validateTimeoutFlag(command *cobra.Command, seconds int) error {
	if command.Flags().Changed("timeout") && (seconds < 300 || seconds > 3600) {
		return nkderr.New(nkderr.CodePreflightFailed, "tenant provisioning budget must be 300..3600 seconds")
	}
	return nil
}

// Only an explicit file is loaded. Neither ambient environment nor an implicit
// search directory supplies configuration; unchanged flag defaults cannot
// overwrite authored YAML values.
func configuredFile(command *cobra.Command, path string, flags nkedemo.DemoFile, actions demoActions) (nkedemo.DemoFile, error) {
	if path == "" {
		if command.Flag("config").Changed {
			return nkedemo.DemoFile{}, nkderr.New(nkderr.CodeUsage, "--config requires a nonempty path")
		}
		return flags, nil
	}
	selected, err := actions.load(command.Context(), path)
	if err != nil {
		return nkedemo.DemoFile{}, err
	}
	for _, field := range []struct {
		name        string
		destination *string
		value       string
	}{
		{"repo", &selected.Build.Repo, flags.Build.Repo}, {"nke-clone", &selected.Build.NKEClone, flags.Build.NKEClone}, {"attempt", &selected.Build.Attempt, flags.Build.Attempt},
		{"nke-checkout", &selected.Run.NKECheckout, flags.Run.NKECheckout}, {"nkdctl", &selected.Run.NKDCTL, flags.Run.NKDCTL}, {"out", &selected.Run.Out, flags.Run.Out},
		{"os-run", &selected.Run.Runs.OS, flags.Run.Runs.OS}, {"components-run", &selected.Run.Runs.Components, flags.Run.Runs.Components}, {"provider-run", &selected.Run.Runs.Providers, flags.Run.Runs.Providers},
		{"site-run", &selected.Run.Runs.Site, flags.Run.Runs.Site}, {"tools-run", &selected.Run.Runs.Tools, flags.Run.Runs.Tools}, {"profiles-run", &selected.Run.Runs.Profiles, flags.Run.Runs.Profiles},
		{"simulator-run", &selected.Run.Runs.Simulator, flags.Run.Runs.Simulator}, {"work", &selected.Restore.Work, flags.Restore.Work},
	} {
		if command.Flags().Changed(field.name) {
			*field.destination = field.value
		}
	}
	if command.Flags().Changed("jobs") {
		selected.Build.Jobs = flags.Build.Jobs
	}
	if command.Flags().Changed("timeout") {
		selected.Run.TimeoutSeconds = flags.Run.TimeoutSeconds
	}
	return selected, nil
}
