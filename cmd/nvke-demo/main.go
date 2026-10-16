// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// nvke-demo is a downstream local diagnostic command. NVKR operations execute
// nvkr, and NVKE owns installation/provisioning; this CLI only maps flags.
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

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/spf13/cobra"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	err := execute(ctx, os.Args[1:], os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(nvkrerr.ExitCode(err))
}
func execute(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	var retained *nvkedemo.RetainedSession
	return executeRoot(ctx, newCommand(out, diagnostics, demoActions{
		run: withOwnDemoExecutable(func(ctx context.Context, cfg nvkedemo.Config) error {
			if cfg.Cleanup {
				return nvkedemo.Run(ctx, cfg)
			}
			var err error
			retained, err = nvkedemo.Retain(ctx, cfg)
			return err
		}), build: nvkedemo.BuildArtifacts, prepare: nvkedemo.PrepareInstaller, restore: nvkedemo.Restore, load: nvkedemo.LoadDemoFile,
		retention: nvkedemo.CheckRetentionCapability, status: nvkedemo.RetainedStatus, cleanup: nvkedemo.CleanupRetained, owner: nvkedemo.ServeRetained,
		after: func(ctx context.Context, out io.Writer) error {
			if retained == nil {
				return nil
			}
			path, err := os.Executable()
			if err != nil {
				return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "select executable for retained access instructions")
			}
			bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			return retained.WriteInstructions(bounded, path, out)
		},
	}), args)
}

// Both lifetime modes select this CLI's executable before entering the owning
// runtime module. The module still independently admits the selected path.
func withOwnDemoExecutable(action func(context.Context, nvkedemo.Config) error) func(context.Context, nvkedemo.Config) error {
	return func(ctx context.Context, cfg nvkedemo.Config) error {
		path, err := os.Executable()
		if err != nil {
			return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "select native demo network probe executable")
		}
		cfg.DemoExecutable = path
		return action(ctx, cfg)
	}
}

// Actions stay typed library calls; injection lets command tests prove help,
// completion and invalid input do not reach configuration I/O or runtime work.
type demoActions struct {
	run       func(context.Context, nvkedemo.Config) error
	build     func(context.Context, nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error)
	prepare   func(context.Context, nvkedemo.BuildConfig, nvkedemo.Config) (nvkedemo.Config, error)
	restore   func(context.Context, string, io.Writer) error
	load      func(context.Context, string) (nvkedemo.DemoFile, error)
	retention func(context.Context) error
	status    func(context.Context, string, io.Writer) error
	cleanup   func(context.Context, string, io.Writer) error
	owner     func(context.Context, string) error
	after     func(context.Context, io.Writer) error
}

func executeRoot(ctx context.Context, root *cobra.Command, args []string) error {
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	var coded *nvkrerr.Error
	if err != nil && !errors.As(err, &coded) {
		return nvkrerr.Wrap(nvkrerr.CodeUsage, err, "local demo command")
	}
	return err
}
func newCommand(out, diagnostics io.Writer, actions demoActions) *cobra.Command {
	var config string
	root := &cobra.Command{
		Use: "nvke-demo", Short: "Build and deploy a trusted local ARM64 NVKE diagnostic",
		SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return nvkrerr.New(nvkrerr.CodeUsage, "select e2e, run, build, status, cleanup, restore or completion; use --help")
		},
	}
	root.SetOut(out)
	root.SetErr(diagnostics)
	root.PersistentFlags().StringVar(&config, "config", "", "explicit version-1 local-demo YAML file (changed flags override it)")
	root.PersistentFlags().Bool("verbose", false, "stream raw public build output in addition to stage boundaries")
	root.PersistentFlags().Bool("plain", false, "use scrolling plain progress instead of the interactive terminal dashboard")
	root.AddCommand(&cobra.Command{
		Use: "network-port-probe", Hidden: true, Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("config") || cmd.Flags().Changed("verbose") || cmd.Flags().Changed("plain") {
				return nvkrerr.New(nvkrerr.CodeUsage, "network port probe accepts no configuration or flags")
			}
			return nvkedemo.ProbeNetworkPrivilegedPorts(cmd.Context())
		},
	})
	root.AddCommand(newRunCommand(out, actions, &config), newBuildCommand(out, actions, &config), newRestoreCommand(out, actions, &config), newE2ECommand(out, actions, &config))
	root.AddCommand(newSessionCommand("status", out, actions), newSessionCommand("cleanup", out, actions), newSessionCommand("retained-owner", out, actions))
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
	flags := nvkedemo.DemoFile{Version: 1, Run: nvkedemo.RunFile{TimeoutSeconds: nvkedemo.DefaultTenantTimeoutSeconds}}
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
			if !cfg.Cleanup && actions.retention != nil {
				if err := actions.retention(cmd.Context()); err != nil {
					return err
				}
			}
			plain, _ := cmd.Flags().GetBool("plain")
			recoveryCtx, err := progressRecoveryContext(cmd.Context(), cfg.Out)
			if err != nil {
				return err
			}
			result := withTerminalSudo(recoveryCtx, out, cmd.ErrOrStderr(), func(session context.Context, refresh func(context.Context) error) error {
				return runProgress(session, out, plain, func(ctx context.Context, progress io.Writer) error {
					cfg.Progress = progress
					return actions.run(ctx, cfg)
				}, refresh)
			})
			return finishRetainedInstructions(cmd.Context(), actions, out, result)
		},
	}
	addRunFlags(command, &flags.Run, true)
	return command
}
func newBuildCommand(out io.Writer, actions demoActions, config *string) *cobra.Command {
	flags := nvkedemo.DemoFile{Version: 1, Build: nvkedemo.BuildFile{Jobs: 1}}
	command := &cobra.Command{
		Use: "build", Short: "Build all ARM64 demo artifacts in one NVKR Definition/Run", Args: cobra.NoArgs,
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
			plain, _ := cmd.Flags().GetBool("plain")
			return runBuildProgress(cmd.Context(), out, verbose, plain, func(ctx context.Context, progress io.Writer) error {
				cfg.Progress = progress
				built, err := actions.build(ctx, cfg)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(progress, "Artifact builds complete; nvkr: %s\nDeployment is separate: nvke-demo run --help\n", built.NVKR)
				return err
			})
		},
	}
	addBuildFlags(command, &flags.Build)
	return command
}
func newRestoreCommand(out io.Writer, actions demoActions, config *string) *cobra.Command {
	flags := nvkedemo.DemoFile{Version: 1}
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
			recoveryCtx, err := progressRecoveryContext(cmd.Context(), work)
			if err != nil {
				return err
			}
			budget, cancel := context.WithTimeout(recoveryCtx, 8*time.Minute)
			defer cancel()
			plain, _ := cmd.Flags().GetBool("plain")
			return withTerminalSudo(budget, out, cmd.ErrOrStderr(), func(session context.Context, refresh func(context.Context) error) error {
				return runProgress(session, out, plain, func(ctx context.Context, progress io.Writer) error {
					if err := actions.restore(ctx, work, progress); err != nil {
						return err
					}
					_, err := fmt.Fprintln(progress, "PASS: recorded local network controls restored; no tenant, VM or management deletion was attempted.")
					return err
				}, refresh)
			})
		},
	}
	command.Flags().StringVar(&flags.Restore.Work, "work", "", "original private Go demo workspace")
	return command
}
func newE2ECommand(out io.Writer, actions demoActions, config *string) *cobra.Command {
	flags := nvkedemo.DemoFile{Version: 1, Build: nvkedemo.BuildFile{Jobs: 1}, Run: nvkedemo.RunFile{TimeoutSeconds: nvkedemo.DefaultTenantTimeoutSeconds}}
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
			if run.Walkthrough {
				if err := presentDemoNarration(cmd.Context(), out, build); err != nil {
					return err
				}
			}
			if !run.Cleanup && actions.retention != nil {
				if err := actions.retention(cmd.Context()); err != nil {
					return err
				}
			}
			recoveryCtx, err := progressRecoveryContext(cmd.Context(), run.Out)
			if err != nil {
				return err
			}
			budget, cancel := context.WithTimeout(recoveryCtx, 8*time.Hour)
			defer cancel()
			verbose, _ := cmd.Flags().GetBool("verbose")
			plain, _ := cmd.Flags().GetBool("plain")
			result := withTerminalSudo(budget, out, cmd.ErrOrStderr(), func(session context.Context, refresh func(context.Context) error) error {
				return runBuildProgress(session, out, verbose, plain, func(ctx context.Context, progress io.Writer) error {
					build.Progress, run.Progress = progress, progress
					run, err = actions.prepare(ctx, build, run)
					if err != nil {
						return err
					}
					built, err := actions.build(ctx, build)
					if err != nil {
						return err
					}
					run.NVKR = built.NVKR
					run.Runs = built.Runs
					return actions.run(ctx, run)
				}, refresh)
			})
			return finishRetainedInstructions(cmd.Context(), actions, out, result)
		},
	}
	addBuildFlags(command, &flags.Build)
	addRunFlags(command, &flags.Run, false)
	return command
}
func addBuildFlags(command *cobra.Command, cfg *nvkedemo.BuildFile) {
	flags := command.Flags()
	flags.BoolVar(&cfg.Prepared, "prepared", false, "use an existing prepare-only bootstrap; requires a matching qualification policy and no existing Run")
	flags.StringVar(&cfg.Repo, "repo", "", "native Linux repository path")
	flags.StringVar(&cfg.NVKEClone, "nvke-clone", "", "optional local source Git clone")
	flags.StringVar(&cfg.Attempt, "attempt", "", "fresh name for the combined artifact Run")
	flags.StringVar(&cfg.QualificationPolicy, "qualification-policy", "", "admit the prepared Plan against a local M3 policy before building; does not sign or qualify outputs")
	flags.IntVar(&cfg.Jobs, "jobs", 1, "maximum concurrent dependency-ready NVKR operations")
}
func addRunFlags(command *cobra.Command, cfg *nvkedemo.RunFile, existing bool) {
	flags := command.Flags()
	flags.BoolVar(&cfg.Walkthrough, "walkthrough", false, "show bounded read-only site, tenant and existing workload observations before cleanup")
	flags.BoolVar(&cfg.Cleanup, "cleanup", false, "delete the tenant and join/restore owned resources before returning (default: retain environment)")
	checkoutHelp := "optional clean pinned installer checkout; e2e prepares it when omitted"
	if existing {
		checkoutHelp = "required clean pinned NVKE installer checkout"
	}
	flags.StringVar(&cfg.NVKECheckout, "nvke-checkout", "", checkoutHelp)
	flags.StringVar(&cfg.Out, "out", "", "fresh private demo output directory")
	flags.StringVar(&cfg.Network, "network", "ovn", "networking: ovn (NVIDIA default), upstream-ovn, or kind-calico (explicit diagnostics)")
	flags.IntVar(&cfg.TimeoutSeconds, "timeout", nvkedemo.DefaultTenantTimeoutSeconds, fmt.Sprintf("tenant provisioning budget in seconds (%d..%d)", nvkedemo.MinTenantTimeoutSeconds, nvkedemo.MaxTenantTimeoutSeconds))
	if !existing {
		return
	}
	flags.StringVar(&cfg.NVKR, "nvkr", "", "native nvkr executable")
	flags.StringVar(&cfg.Runs.OS, "os-run", "", "completed OS attempt")
	flags.StringVar(&cfg.Runs.Components, "components-run", "", "completed components attempt")
	flags.StringVar(&cfg.Runs.Providers, "provider-run", "", "completed provider attempt (components Run is valid)")
	flags.StringVar(&cfg.Runs.Site, "site-run", "", "completed site attempt")
	flags.StringVar(&cfg.Runs.Tools, "tools-run", "", "completed installer-tools attempt")
	flags.StringVar(&cfg.Runs.Profiles, "profiles-run", "", "completed profiles attempt")
	flags.StringVar(&cfg.Runs.Simulator, "simulator-run", "", "completed simulator attempt")
}
func validateTimeoutFlag(command *cobra.Command, seconds int) error {
	if command.Flags().Changed("timeout") && (seconds < nvkedemo.MinTenantTimeoutSeconds || seconds > nvkedemo.MaxTenantTimeoutSeconds) {
		return nvkrerr.New(nvkrerr.CodeUsage, "tenant provisioning budget must be %d..%d seconds", nvkedemo.MinTenantTimeoutSeconds, nvkedemo.MaxTenantTimeoutSeconds)
	}
	return nil
}

// Only an explicit file is loaded. Neither ambient environment nor an implicit
// search directory supplies configuration; unchanged flag defaults cannot
// overwrite authored YAML values.
func configuredFile(command *cobra.Command, path string, flags nvkedemo.DemoFile, actions demoActions) (nvkedemo.DemoFile, error) {
	if command.Flags().Changed("qualification-policy") && flags.Build.QualificationPolicy == "" {
		return nvkedemo.DemoFile{}, nvkrerr.New(nvkrerr.CodeUsage, "--qualification-policy requires a nonempty path")
	}
	if path == "" {
		if command.Flag("config").Changed {
			return nvkedemo.DemoFile{}, nvkrerr.New(nvkrerr.CodeUsage, "--config requires a nonempty path")
		}
		return flags, nil
	}
	selected, err := actions.load(command.Context(), path)
	if err != nil {
		return nvkedemo.DemoFile{}, err
	}
	for _, field := range []struct {
		name        string
		destination *string
		value       string
	}{
		{"repo", &selected.Build.Repo, flags.Build.Repo}, {"nvke-clone", &selected.Build.NVKEClone, flags.Build.NVKEClone}, {"attempt", &selected.Build.Attempt, flags.Build.Attempt},
		{"qualification-policy", &selected.Build.QualificationPolicy, flags.Build.QualificationPolicy},
		{"nvke-checkout", &selected.Run.NVKECheckout, flags.Run.NVKECheckout}, {"nvkr", &selected.Run.NVKR, flags.Run.NVKR}, {"out", &selected.Run.Out, flags.Run.Out}, {"network", &selected.Run.Network, flags.Run.Network},
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
	if command.Flags().Changed("prepared") {
		selected.Build.Prepared = flags.Build.Prepared
	}
	if command.Flags().Changed("timeout") {
		selected.Run.TimeoutSeconds = flags.Run.TimeoutSeconds
	}
	if command.Flags().Changed("walkthrough") {
		selected.Run.Walkthrough = flags.Run.Walkthrough
	}
	if command.Flags().Changed("cleanup") {
		selected.Run.Cleanup = flags.Run.Cleanup
	}
	return selected, nil
}

func finishRetainedInstructions(ctx context.Context, actions demoActions, out io.Writer, result error) error {
	if actions.after != nil {
		// The progress presenter and sudo refresher have already returned and
		// restored terminal ownership. Full commands must not enter a log ring.
		return errors.Join(result, actions.after(ctx, out))
	}
	return result
}

func newSessionCommand(name string, out io.Writer, actions demoActions) *cobra.Command {
	var session string
	command := &cobra.Command{Use: name, Args: cobra.NoArgs, Hidden: name == "retained-owner"}
	switch name {
	case "status":
		command.Short = "Observe the exact retained demo owner; does not delete resources"
	case "cleanup":
		command.Short = "Explicitly delete/join the retained demo and restore owned host networking"
	case "retained-owner":
		command.Short = "Private service-owner child entry"
	}
	command.Flags().StringVar(&session, "session", "", "original private retained session directory printed after run")
	command.RunE = func(cmd *cobra.Command, _ []string) error {
		if session == "" || cmd.Flags().Changed("config") || cmd.Flags().Changed("verbose") || name == "retained-owner" && cmd.Flags().Changed("plain") {
			return nvkrerr.New(nvkrerr.CodeUsage, "retained command requires only its original --session selector")
		}
		if name == "retained-owner" {
			if actions.owner == nil {
				return nvkrerr.New(nvkrerr.CodeEngineCapability, "retained owner entry unavailable")
			}
			return actions.owner(cmd.Context(), session)
		}
		ceiling := 30 * time.Minute
		if name == "cleanup" {
			ceiling = nvkedemo.RetainedCleanupTimeout
		}
		budget, cancel := context.WithTimeout(cmd.Context(), ceiling)
		defer cancel()
		if name == "status" {
			if actions.status == nil {
				return nvkrerr.New(nvkrerr.CodeEngineCapability, "retained status unavailable")
			}
			return actions.status(budget, session, out)
		}
		if actions.cleanup == nil {
			return nvkrerr.New(nvkrerr.CodeEngineCapability, "retained cleanup unavailable")
		}
		plain, _ := cmd.Flags().GetBool("plain")
		return withTerminalSudo(budget, out, cmd.ErrOrStderr(), func(ctx context.Context, refresh func(context.Context) error) error {
			return runProgress(ctx, out, plain, func(ctx context.Context, progress io.Writer) error {
				return actions.cleanup(ctx, session, progress)
			}, refresh)
		})
	}
	return command
}
