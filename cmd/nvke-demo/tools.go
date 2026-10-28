// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/spf13/cobra"
)

func newToolsCommand(out io.Writer, actions demoActions) *cobra.Command {
	cfg := nvkedemo.ToolConfig{Jobs: 2}
	command := &cobra.Command{Use: "tools", Short: "Prepare catalog-selected native CLI dependencies through NVKR", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("config") {
				return nvkrerr.New(nvkrerr.CodeUsage, "tools selects its catalog from the native host; use its explicit flags")
			}
			if actions.tools == nil {
				return nvkrerr.New(nvkrerr.CodeEngineCapability, "native tool preparation unavailable")
			}
			plain, _ := cmd.Flags().GetBool("plain")
			verbose, _ := cmd.Flags().GetBool("verbose")
			return runBuildProgress(cmd.Context(), out, verbose, plain, func(ctx context.Context, progress io.Writer) error {
				cfg.Progress = progress
				directory, err := actions.tools(ctx, cfg)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintf(progress, "Native catalog tools verified and prepared: %s\n", directory)
				return err
			})
		}}
	command.Flags().StringVar(&cfg.Repository, "repo", "", "NVKR repository containing the native tool Definition")
	command.Flags().StringVar(&cfg.NVKR, "nvkr", "", "native NVKR executable")
	command.Flags().StringVar(&cfg.Go, "go", "go", "Go executable for NVKR utility helpers")
	command.Flags().StringVar(&cfg.Attempt, "attempt", "", "new immutable tool Run attempt")
	command.Flags().StringVar(&cfg.Out, "out", "", "new private directory for tool verification and extraction")
	command.Flags().IntVar(&cfg.Jobs, "jobs", 2, "parallel NVKR operations")
	for _, flag := range []string{"repo", "nvkr", "attempt", "out"} {
		_ = command.MarkFlagRequired(flag)
	}
	return command
}
