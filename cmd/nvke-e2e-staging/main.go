// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// nvke-e2e-staging prepares NVKE suite inputs through existing NVKR owners.
// It does not execute qualification or allocate runtime resources.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/dsx-ai-factory/nvkr/internal/nvkee2estaging"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/spf13/cobra"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	err := execute(ctx, os.Args[1:], os.Stdout, os.Stderr, nvkee2estaging.Prepare)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(e.ExitCode(err))
}

type preparer func(context.Context, nvkee2estaging.Options) (nvkee2estaging.Report, error)

func execute(ctx context.Context, args []string, out, diagnostics io.Writer, prepare preparer) error {
	var options nvkee2estaging.Options
	options.Diagnostics = diagnostics
	var stage bool
	cmd := &cobra.Command{
		Use: "nvke-e2e-staging", Short: "Stage inputs for the externally owned NVKE Go suite",
		Args: cobra.NoArgs, SilenceUsage: true, SilenceErrors: true,
		RunE: func(command *cobra.Command, _ []string) error {
			if options.VerifyTimeout <= 0 {
				return e.New(e.CodeUsage, "--verify-timeout must be a positive duration")
			}
			if options.PlanOnly == stage || options.CompileOnly && !stage {
				return e.New(e.CodeUsage, "select exactly one of --plan-only or --stage; --compile-only requires --stage")
			}
			report, err := prepare(command.Context(), options)
			if err != nil {
				return err
			}
			if err := json.NewEncoder(out).Encode(report); err != nil {
				return e.Wrap(e.CodeInternal, err, "writing preparation description")
			}
			return nil
		},
	}
	cmd.SetOut(out)
	cmd.SetErr(diagnostics)
	flags := cmd.Flags()
	flags.BoolVar(&options.PlanOnly, "plan-only", false, "check input mappings without source or publication access")
	flags.BoolVar(&stage, "stage", false, "verify and stage inputs through the existing Go owners")
	flags.BoolVar(&options.CompileOnly, "compile-only", false, "compile the pinned suite after staging without executing it")
	flags.DurationVar(&options.VerifyTimeout, "verify-timeout", nvkee2estaging.DefaultVerifyTimeout, "timeout for complete publication verification and export")
	for _, flag := range []struct {
		name, help string
		value      *string
	}{
		{"run", "completed producing Run JSON", &options.Run},
		{"plan", "matching complete Plan", &options.Plan},
		{"definition", "producing Definition", &options.Definition},
		{"environment", "producing Environment", &options.Environment},
		{"inputs", "explicit NVKE suite-role mapping JSON", &options.Inputs},
		{"out", "fresh private preparation directory", &options.Out},
		{"repository", "producing owner/repository", &options.Repository},
		{"run-id", "producing GitHub Run ID", &options.RunID},
		{"run-attempt", "producing Run attempt", &options.RunAttempt},
		{"reference", "immutable published artifact reference", &options.Reference},
		{"source-node", "exact restored Plan fetch node", &options.SourceNode},
		{"stream-name", "complete maintained stream identity", &options.StreamName},
		{"gpu", "declared GPU capability", &options.GPU},
		{"k8s", "declared Kubernetes version", &options.K8s},
		{"arch", "declared native architecture", &options.Arch},
		{"ubuntu", "declared Ubuntu version", &options.Ubuntu},
		{"versions", "operator-supplied expected NVKE suite version flags", &options.Versions},
	} {
		flags.StringVar(flag.value, flag.name, "", flag.help)
		if err := cmd.MarkFlagRequired(flag.name); err != nil {
			return e.Wrap(e.CodeInternal, err, "configuring required preparation flag")
		}
	}
	flags.StringVar(&options.GitHubServerURL, "github-server-url", "https://github.com", "explicit producing GitHub HTTPS origin")
	flags.StringVar(&options.Cosign, "cosign", "", "pinned verifier executable")
	flags.StringVar(&options.CosignDigest, "cosign-digest", "", "exact verifier byte digest")
	flags.StringVar(&options.Identity, "identity", "", "approved producing signer identity expression")
	flags.StringVar(&options.Issuer, "issuer", "", "approved signer issuer")
	flags.StringVar(&options.MinimumDiskEvidence, "minimum-disk-evidence", "", "explicit minimum disk evidence policy")
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(ctx)
	var coded *e.Error
	if err != nil && !errors.As(err, &coded) {
		return e.Wrap(e.CodeUsage, err, "preparation command")
	}
	return err
}
