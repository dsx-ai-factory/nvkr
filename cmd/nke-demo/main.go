// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// nke-demo is a downstream local diagnostic command. NKD operations execute
// nkdctl, and NKE owns installation/provisioning; this CLI only maps flags.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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
	err := executeCommand(ctx, args, out, diagnostics)
	if err == flag.ErrHelp {
		return nil
	}
	return err
}
func executeCommand(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	if len(args) == 0 {
		return nkderr.New(nkderr.CodeUsage, "usage: nke-demo run|build|restore [flags]")
	}
	flags := flag.NewFlagSet("nke-demo "+args[0], flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	switch args[0] {
	case "run":
		cfg := nkedemo.Config{Progress: out}
		seconds := 3600
		flags.StringVar(&cfg.NKECheckout, "nke-checkout", "", "clean pinned NKE installer checkout")
		flags.StringVar(&cfg.NKDCTL, "nkdctl", "", "native nkdctl executable")
		flags.StringVar(&cfg.Runs.OS, "os-run", "", "completed OS attempt")
		flags.StringVar(&cfg.Runs.Components, "components-run", "", "completed components attempt")
		flags.StringVar(&cfg.Runs.Providers, "provider-run", "", "completed provider attempt (components Run is valid)")
		flags.StringVar(&cfg.Runs.Site, "site-run", "", "completed site attempt")
		flags.StringVar(&cfg.Runs.Tools, "tools-run", "", "completed installer-tools attempt")
		flags.StringVar(&cfg.Runs.Profiles, "profiles-run", "", "completed profiles attempt")
		flags.StringVar(&cfg.Runs.Simulator, "simulator-run", "", "completed simulator attempt")
		flags.StringVar(&cfg.Out, "out", "", "fresh private demo output directory")
		flags.IntVar(&seconds, "timeout", 3600, "tenant provisioning budget in seconds (300..3600)")
		if err := parse(flags, args[1:]); err != nil {
			return err
		}
		cfg.Timeout = time.Duration(seconds) * time.Second
		return nkedemo.Run(ctx, cfg)
	case "restore":
		var work string
		flags.StringVar(&work, "work", "", "original private Go demo workspace")
		if err := parse(flags, args[1:]); err != nil {
			return err
		}
		if work == "" {
			return nkderr.New(nkderr.CodeUsage, "--work required")
		}
		budget, cancel := context.WithTimeout(ctx, 8*time.Minute)
		defer cancel()
		if err := nkedemo.Restore(budget, work, out); err != nil {
			return err
		}
		fmt.Fprintln(out, "PASS: recorded local network controls restored; no tenant, VM or management deletion was attempted.")
		return nil
	case "build":
		cfg := nkedemo.BuildConfig{Progress: out}
		flags.StringVar(&cfg.Repository, "repo", "", "native Linux repository path")
		flags.StringVar(&cfg.NKEClone, "nke-clone", "", "optional local source Git clone")
		flags.StringVar(&cfg.AttemptPrefix, "attempt", "", "fresh prefix for six artifact Runs")
		flags.IntVar(&cfg.Jobs, "jobs", 1, "parallel jobs within each lane")
		if err := parse(flags, args[1:]); err != nil {
			return err
		}
		built, err := nkedemo.BuildArtifacts(ctx, cfg)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Artifact builds complete; nkdctl: %s\nDeployment is separate: nke-demo run --help\n", built.NKDCTL)
		return nil
	default:
		return nkderr.New(nkderr.CodeUsage, "unknown local demo command %q", args[0])
	}
}
func parse(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return err
		}
		return nkderr.Wrap(nkderr.CodeUsage, err, "local demo flags")
	}
	if flags.NArg() != 0 {
		return nkderr.New(nkderr.CodeUsage, "unexpected positional arguments")
	}
	return nil
}
