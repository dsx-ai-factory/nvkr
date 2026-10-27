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
	"path/filepath"
	"syscall"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	"github.com/dsx-ai-factory/nvkr/internal/qemutpm"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/spf13/cobra"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer cancel()
	var err error
	if filepath.Base(os.Args[0]) == qemutpm.LauncherName {
		err = qemutpm.Run(ctx, os.Args[1:])
	} else {
		err = execute(ctx, os.Args[1:], os.Stdout, os.Stderr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(nvkrerr.ExitCode(err))
}
func execute(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	return executeRoot(ctx, newCommand(out, diagnostics, demoActions{
		run: withOwnDemoExecutable(nvkedemo.DeployArtifacts),
		admit: func(ctx context.Context, cfg nvkedemo.Config) error {
			return nvkedemo.AdmitDemoDeployment(ctx, cfg)
		},
		build: nvkedemo.BuildArtifacts, tools: nvkedemo.PrepareTools,
		guest:   nvkedemo.RunNativeGuest,
		restore: nvkedemo.Restore, load: nvkedemo.LoadDemoFile,
		status: nvkedemo.RetainedStatus, cleanup: nvkedemo.CleanupRetained,
		owner: nvkedemo.ServeRetained,
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
	admit   func(context.Context, nvkedemo.Config) error
	run     func(context.Context, nvkedemo.Config) error
	build   func(context.Context, nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error)
	tools   func(context.Context, nvkedemo.ToolConfig) (string, error)
	guest   func(context.Context, nvkedemo.NativeGuestConfig) error
	restore func(context.Context, string, io.Writer) error
	load    func(context.Context, string) (nvkedemo.DemoFile, error)
	status  func(context.Context, string, io.Writer) error
	cleanup func(context.Context, string, io.Writer) error
	owner   func(context.Context, string) error
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
		Use: "nvke-demo", Short: "Build and deploy a local NVKE demo with native host tools",
		SilenceUsage: true, SilenceErrors: true, Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return nvkrerr.New(nvkrerr.CodeUsage, "select e2e, run, build, tools, guest, status, cleanup, restore or completion; use --help")
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
	root.AddCommand(newToolsCommand(out, actions))
	root.AddCommand(newGuestCommand(out, actions))
	root.AddCommand(newQualificationSetupCommand(out, actions, &config), newQualificationRegistryStopCommand())
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
		Use: "run", Short: "Deploy a native site and tenant QCOW guests from completed NVKR outputs", Args: cobra.NoArgs,
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
			if actions.admit != nil {
				if err := actions.admit(cmd.Context(), cfg); err != nil {
					return err
				}
			}
			plain, _ := cmd.Flags().GetBool("plain")
			recoveryCtx, err := progressRecoveryContext(cmd.Context(), cfg.Out)
			if err != nil {
				return err
			}
			return withTerminalSudo(recoveryCtx, out, cmd.ErrOrStderr(), func(session context.Context, refresh func(context.Context) error) error {
				return runProgress(session, out, plain, func(ctx context.Context, progress io.Writer) error {
					cfg.Progress = progress
					nativePresentation(progress, cfg.Conformance)
					return actions.run(ctx, cfg)
				}, refresh)
			})
		},
	}
	addRunFlags(command, &flags.Run, true)
	return command
}
func newBuildCommand(out io.Writer, actions demoActions, config *string) *cobra.Command {
	flags := nvkedemo.DemoFile{Version: 1, Build: nvkedemo.BuildFile{Jobs: 1}}
	command := &cobra.Command{
		Use: "build", Short: "Build demo artifacts and native tools through NVKR", Args: cobra.NoArgs,
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
				_, err = fmt.Fprintf(progress, "Artifact builds complete; nvkr: %s\nArtifacts Run: %s\nNative tools Run: %s\nDeployment is separate: nvke-demo run --artifacts-run <artifacts Run> --tools-run <native tools Run> --nvkr <nvkr> --out <fresh directory>\n", built.NVKR, built.Runs.OS, built.Runs.Tools)
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
		Use: "e2e", Short: "Build artifact and native tool Runs, then deploy their exact outputs", Args: cobra.NoArgs,
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
			if actions.admit != nil {
				if err := actions.admit(cmd.Context(), run); err != nil {
					return err
				}
			}
			if run.Walkthrough {
				if err := presentDemoNarration(cmd.Context(), out, build); err != nil {
					return err
				}
			}
			recoveryCtx, err := progressRecoveryContext(cmd.Context(), run.Out)
			if err != nil {
				return err
			}
			ceiling := 8 * time.Hour
			if run.Conformance {
				ceiling += 4 * time.Hour
			}
			budget, cancel := context.WithTimeout(recoveryCtx, ceiling)
			defer cancel()
			verbose, _ := cmd.Flags().GetBool("verbose")
			plain, _ := cmd.Flags().GetBool("plain")
			return withTerminalSudo(budget, out, cmd.ErrOrStderr(), func(session context.Context, refresh func(context.Context) error) error {
				return runBuildProgress(session, out, verbose, plain, func(ctx context.Context, progress io.Writer) error {
					build.Progress, run.Progress = progress, progress
					nativePresentation(progress, run.Conformance)
					built, err := actions.build(ctx, build)
					if err != nil {
						return err
					}
					run.NVKR = built.NVKR
					run.Runs = built.Runs
					return actions.run(ctx, run)
				}, refresh)
			})
		},
	}
	addBuildFlags(command, &flags.Build)
	addRunFlags(command, &flags.Run, false)
	return command
}
func addBuildFlags(command *cobra.Command, cfg *nvkedemo.BuildFile) {
	flags := command.Flags()
	flags.BoolVar(&cfg.QualifyLocal, "qualify-local", false, "automatically prepare local M3 trust, qualify outputs and stop the owned registry")
	flags.BoolVar(&cfg.Prepared, "prepared", false, "use an existing prepare-only bootstrap; requires a matching qualification policy and no existing Run")
	flags.StringVar(&cfg.Repo, "repo", "", "native host repository path")
	flags.StringVar(&cfg.Definition, "definition", "", "NVKR Definition inside repo; requires environment and nvkr")
	flags.StringVar(&cfg.Environment, "environment", "", "local NVKR Environment inside repo")
	flags.StringVar(&cfg.NVKR, "nvkr", "", "native NVKR executable for explicitly selected build inputs")
	flags.StringVar(&cfg.NVKEClone, "nvke-clone", "", "optional local source Git clone")
	flags.StringVar(&cfg.Attempt, "attempt", "", "fresh name for the combined artifact Run")
	flags.StringVar(&cfg.QualificationPolicy, "qualification-policy", "", "admit the prepared Plan against a local M3 policy before building; does not sign or qualify outputs")
	flags.IntVar(&cfg.Jobs, "jobs", 1, "maximum concurrent dependency-ready NVKR operations")
}
func addRunFlags(command *cobra.Command, cfg *nvkedemo.RunFile, existing bool) {
	flags := command.Flags()
	flags.StringVar(&cfg.Accelerator, "accelerator", "", "native guest accelerator: Mac hvf or explicit tcg for the selected 64K image; Linux kvm (default)")
	flags.BoolVar(&cfg.Conformance, "conformance", false, "run Kubernetes conformance against this disposable tenant before cleanup (native Mac or Linux ARM64)")
	flags.BoolVar(&cfg.Walkthrough, "walkthrough", false, "show bounded read-only site, tenant and existing workload observations before cleanup")
	flags.BoolVar(&cfg.Cleanup, "cleanup", false, "delete owned VMs after acceptance; otherwise return after checks and retain until explicit cleanup")
	checkoutHelp := "legacy source locator; deployment uses the NVKR installer archive"
	if existing {
		checkoutHelp = "optional source checkout; native deployment uses the NVKR installer archive"
	}
	flags.StringVar(&cfg.NVKECheckout, "nvke-checkout", "", checkoutHelp)
	flags.StringVar(&cfg.Out, "out", "", "fresh private demo output directory")
	flags.StringVar(&cfg.Network, "network", "ovn", "networking: ovn (NVIDIA default) or explicit upstream-ovn")
	flags.IntVar(&cfg.TimeoutSeconds, "timeout", nvkedemo.DefaultTenantTimeoutSeconds, fmt.Sprintf("tenant provisioning budget in seconds (%d..%d)", nvkedemo.MinTenantTimeoutSeconds, nvkedemo.MaxTenantTimeoutSeconds))
	if !existing {
		return
	}
	flags.StringVar(&cfg.Repo, "repo", "", "repository containing the captured local artifact Run; required with --conformance")
	flags.StringVar(&cfg.NVKR, "nvkr", "", "native nvkr executable")
	flags.StringVar(&cfg.Runs.Artifacts, "artifacts-run", "", "completed artifact Run supplying disk, components, providers, site and profiles")
	flags.StringVar(&cfg.Runs.HostTools, "host-tools-run", "", "completed native host tool Run; defaults to tools-run")
	flags.StringVar(&cfg.Runs.Runtime, "runtime-run", "", "completed runtime-image Run; defaults to components-run")
	flags.StringVar(&cfg.Runs.OS, "os-run", "", "completed OS attempt")
	flags.StringVar(&cfg.Runs.Components, "components-run", "", "completed components attempt")
	flags.StringVar(&cfg.Runs.Providers, "provider-run", "", "completed provider attempt (components Run is valid)")
	flags.StringVar(&cfg.Runs.Site, "site-run", "", "completed site attempt")
	flags.StringVar(&cfg.Runs.Tools, "tools-run", "", "completed native host/site/installer tool Run")
	flags.StringVar(&cfg.Runs.Profiles, "profiles-run", "", "completed profiles attempt")
	flags.StringVar(&cfg.Runs.Simulator, "simulator-run", "", "legacy simulator Run locator")
	_ = flags.MarkHidden("simulator-run")
	_ = flags.MarkHidden("nvke-checkout")
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
		{"definition", &selected.Build.Definition, flags.Build.Definition}, {"environment", &selected.Build.Environment, flags.Build.Environment},
		{"accelerator", &selected.Run.Accelerator, flags.Run.Accelerator}, {"nvke-checkout", &selected.Run.NVKECheckout, flags.Run.NVKECheckout}, {"nvkr", &selected.Run.NVKR, flags.Run.NVKR}, {"out", &selected.Run.Out, flags.Run.Out}, {"network", &selected.Run.Network, flags.Run.Network},
		{"artifacts-run", &selected.Run.Runs.Artifacts, flags.Run.Runs.Artifacts}, {"host-tools-run", &selected.Run.Runs.HostTools, flags.Run.Runs.HostTools}, {"runtime-run", &selected.Run.Runs.Runtime, flags.Run.Runs.Runtime}, {"os-run", &selected.Run.Runs.OS, flags.Run.Runs.OS}, {"components-run", &selected.Run.Runs.Components, flags.Run.Runs.Components}, {"provider-run", &selected.Run.Runs.Providers, flags.Run.Runs.Providers},
		{"site-run", &selected.Run.Runs.Site, flags.Run.Runs.Site}, {"tools-run", &selected.Run.Runs.Tools, flags.Run.Runs.Tools}, {"profiles-run", &selected.Run.Runs.Profiles, flags.Run.Runs.Profiles},
		{"simulator-run", &selected.Run.Runs.Simulator, flags.Run.Runs.Simulator}, {"work", &selected.Restore.Work, flags.Restore.Work},
	} {
		if field.name == "nvkr" && (command.Name() == "build" || command.Name() == "e2e") {
			continue
		}
		if field.name == "repo" && command.Name() == "run" {
			continue
		}
		if command.Flags().Changed(field.name) {
			*field.destination = field.value
		}
	}
	if command.Flags().Changed("nvkr") && (command.Name() == "build" || command.Name() == "e2e") {
		selected.Build.NVKR = flags.Build.NVKR
	}
	if command.Flags().Changed("repo") && command.Name() == "run" {
		selected.Run.Repo = flags.Run.Repo
	}
	if command.Flags().Changed("conformance") {
		selected.Run.Conformance = flags.Run.Conformance
	}
	if command.Flags().Changed("jobs") {
		selected.Build.Jobs = flags.Build.Jobs
	}
	if command.Flags().Changed("qualify-local") {
		selected.Build.QualifyLocal = flags.Build.QualifyLocal
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
