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
	var retained *nkedemo.RetainedSession
	return executeRoot(ctx, newCommand(out, diagnostics, demoActions{
		run: func(ctx context.Context, cfg nkedemo.Config) error {
			path, err := os.Executable()
			if err != nil {
				return nkderr.Wrap(nkderr.CodeEngineCapability, err, "select native demo network probe executable")
			}
			cfg.DemoExecutable = path
			if cfg.Cleanup {
				return nkedemo.Run(ctx, cfg)
			}
			retained, err = nkedemo.Retain(ctx, cfg)
			return err
		}, build: nkedemo.BuildArtifacts, prepare: nkedemo.PrepareInstaller, restore: nkedemo.Restore, load: nkedemo.LoadDemoFile,
		retention: nkedemo.CheckRetentionCapability, status: nkedemo.RetainedStatus, cleanup: nkedemo.CleanupRetained, owner: nkedemo.ServeRetained,
		after: func(ctx context.Context, out io.Writer) error {
			if retained == nil {
				return nil
			}
			path, err := os.Executable()
			if err != nil {
				return nkderr.Wrap(nkderr.CodePreflightFailed, err, "select executable for retained access instructions")
			}
			bounded, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			return retained.WriteInstructions(bounded, path, out)
		},
	}), args)
}

// Actions stay typed library calls; injection lets command tests prove help,
// completion and invalid input do not reach configuration I/O or runtime work.
type demoActions struct {
	run       func(context.Context, nkedemo.Config) error
	build     func(context.Context, nkedemo.BuildConfig) (nkedemo.BuiltInputs, error)
	prepare   func(context.Context, nkedemo.BuildConfig, nkedemo.Config) (nkedemo.Config, error)
	restore   func(context.Context, string, io.Writer) error
	load      func(context.Context, string) (nkedemo.DemoFile, error)
	retention func(context.Context) error
	status    func(context.Context, string, io.Writer) error
	cleanup   func(context.Context, string, io.Writer) error
	owner     func(context.Context, string) error
	after     func(context.Context, io.Writer) error
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
			return nkderr.New(nkderr.CodeUsage, "select e2e, run, build, status, cleanup, restore or completion; use --help")
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
				return nkderr.New(nkderr.CodeUsage, "network port probe accepts no configuration or flags")
			}
			return nkedemo.ProbeNetworkPrivilegedPorts(cmd.Context())
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
	flags := nkedemo.DemoFile{Version: 1, Run: nkedemo.RunFile{TimeoutSeconds: nkedemo.DefaultTenantTimeoutSeconds}}
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
			plain, _ := cmd.Flags().GetBool("plain")
			return runBuildProgress(cmd.Context(), out, verbose, plain, func(ctx context.Context, progress io.Writer) error {
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
	flags := nkedemo.DemoFile{Version: 1, Build: nkedemo.BuildFile{Jobs: 1}, Run: nkedemo.RunFile{TimeoutSeconds: nkedemo.DefaultTenantTimeoutSeconds}}
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
					run.NKDCTL = built.NKDCTL
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
func addBuildFlags(command *cobra.Command, cfg *nkedemo.BuildFile) {
	flags := command.Flags()
	flags.StringVar(&cfg.Repo, "repo", "", "native Linux repository path")
	flags.StringVar(&cfg.NKEClone, "nke-clone", "", "optional local source Git clone")
	flags.StringVar(&cfg.Attempt, "attempt", "", "fresh name for the combined artifact Run")
	flags.IntVar(&cfg.Jobs, "jobs", 1, "maximum concurrent dependency-ready NKD operations")
}
func addRunFlags(command *cobra.Command, cfg *nkedemo.RunFile, existing bool) {
	flags := command.Flags()
	flags.BoolVar(&cfg.Cleanup, "cleanup", false, "delete the tenant and join/restore owned resources before returning (default: retain environment)")
	checkoutHelp := "optional clean pinned installer checkout; e2e prepares it when omitted"
	if existing {
		checkoutHelp = "required clean pinned NKE installer checkout"
	}
	flags.StringVar(&cfg.NKECheckout, "nke-checkout", "", checkoutHelp)
	flags.StringVar(&cfg.Out, "out", "", "fresh private demo output directory")
	flags.StringVar(&cfg.Network, "network", "ovn", "networking: ovn (NVIDIA default), upstream-ovn, or kind-calico (explicit diagnostics)")
	flags.IntVar(&cfg.TimeoutSeconds, "timeout", nkedemo.DefaultTenantTimeoutSeconds, fmt.Sprintf("tenant provisioning budget in seconds (%d..%d)", nkedemo.MinTenantTimeoutSeconds, nkedemo.MaxTenantTimeoutSeconds))
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
	if command.Flags().Changed("timeout") && (seconds < nkedemo.MinTenantTimeoutSeconds || seconds > nkedemo.MaxTenantTimeoutSeconds) {
		return nkderr.New(nkderr.CodeUsage, "tenant provisioning budget must be %d..%d seconds", nkedemo.MinTenantTimeoutSeconds, nkedemo.MaxTenantTimeoutSeconds)
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
		{"nke-checkout", &selected.Run.NKECheckout, flags.Run.NKECheckout}, {"nkdctl", &selected.Run.NKDCTL, flags.Run.NKDCTL}, {"out", &selected.Run.Out, flags.Run.Out}, {"network", &selected.Run.Network, flags.Run.Network},
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
			return nkderr.New(nkderr.CodeUsage, "retained command requires only its original --session selector")
		}
		if name == "retained-owner" {
			if actions.owner == nil {
				return nkderr.New(nkderr.CodeEngineCapability, "retained owner entry unavailable")
			}
			return actions.owner(cmd.Context(), session)
		}
		budget, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
		defer cancel()
		if name == "status" {
			if actions.status == nil {
				return nkderr.New(nkderr.CodeEngineCapability, "retained status unavailable")
			}
			return actions.status(budget, session, out)
		}
		if actions.cleanup == nil {
			return nkderr.New(nkderr.CodeEngineCapability, "retained cleanup unavailable")
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
