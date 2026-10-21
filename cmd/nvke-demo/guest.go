// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/spf13/cobra"
)

func newGuestCommand(out io.Writer, actions demoActions) *cobra.Command {
	accelerator := "hvf"
	if runtime.GOOS == "linux" {
		accelerator = "kvm"
	}
	cfg := nvkedemo.NativeGuestConfig{Accelerator: accelerator, Timeout: 10 * time.Minute}
	command := &cobra.Command{Use: "guest", Short: "Check one native QCOW guest and local HTTP delivery, then delete it", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("config") {
				return nvkrerr.New(nvkrerr.CodeUsage, "guest requires explicit local disk and tools flags")
			}
			if actions.guest == nil {
				return nvkrerr.New(nvkrerr.CodeEngineCapability, "native guest check unavailable")
			}
			plain, _ := cmd.Flags().GetBool("plain")
			return runNativeProgress(cmd.Context(), runtime.GOOS, cfg.Out, out, cmd.ErrOrStderr(), plain, func(ctx context.Context, progress io.Writer) error {
				cfg.Progress = progress
				var err error
				cfg.DemoExecutable, err = os.Executable()
				if err != nil {
					return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "select native network probe executable")
				}
				return actions.guest(ctx, cfg)
			}, withTerminalSudo)
		}}
	command.Flags().StringVar(&cfg.Image, "image", "", "existing local standalone QCOW2 disk")
	command.Flags().StringVar(&cfg.Tools, "tools", "", "directory containing NVKR-extracted native NICo tools")
	command.Flags().StringVar(&cfg.Out, "out", "", "fresh private guest attempt directory")
	command.Flags().StringVar(&cfg.Accelerator, "accelerator", accelerator, "native QEMU accelerator: Mac hvf or explicit tcg for 64K images; Linux kvm")
	command.Flags().DurationVar(&cfg.Timeout, "timeout", 10*time.Minute, "bounded cloud-init readiness wait (1..30 minutes)")
	for _, flag := range []string{"image", "tools", "out"} {
		_ = command.MarkFlagRequired(flag)
	}
	return command
}
