// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/spf13/cobra"
)

func newNativeCommand(out io.Writer, actions demoActions) *cobra.Command {
	accelerator := "hvf"
	if runtime.GOOS == "linux" {
		accelerator = "kvm"
	}
	cfg := nvkedemo.NativeConfig{Accelerator: accelerator}
	command := &cobra.Command{Use: "native", Short: "Run one site and two tenant QCOW guests using native host tools and local HTTP", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cmd.Flags().Changed("config") {
				return nvkrerr.New(nvkrerr.CodeUsage, "native requires explicit local image and Run selections")
			}
			if actions.native == nil {
				return nvkrerr.New(nvkrerr.CodeEngineCapability, "native demo runtime unavailable")
			}
			plain, _ := cmd.Flags().GetBool("plain")
			return runNativeProgress(cmd.Context(), runtime.GOOS, filepath.Join(cfg.Out, "runtime"), out, cmd.ErrOrStderr(), plain, func(ctx context.Context, progress io.Writer) error {
				cfg.Progress = progress
				var err error
				cfg.DemoExecutable, err = os.Executable()
				if err != nil {
					return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "select native network probe executable")
				}
				return actions.native(ctx, cfg)
			}, withTerminalSudo)
		}}
	flags := command.Flags()
	flags.StringVar(&cfg.Image, "image", "", "local standalone NVKE QCOW2; remains local")
	flags.StringVar(&cfg.Out, "out", "", "fresh private native demo attempt")
	flags.StringVar(&cfg.Accelerator, "accelerator", accelerator, "native QEMU accelerator: Mac hvf or explicit tcg for 64K images; Linux kvm")
	flags.StringVar(&cfg.Network, "network", "ovn", "matched selected disk/site/tenant network: ovn (NVIDIA) or explicit upstream-ovn")
	flags.StringVar(&cfg.Assets.NVKR, "nvkr", "", "native NVKR executable")
	flags.StringVar(&cfg.Assets.ToolsRun, "tools-run", "", "completed host/site tool Run")
	flags.StringVar(&cfg.Assets.HostToolsRun, "host-tools-run", "", "completed host tool Run; defaults to tools-run")
	flags.StringVar(&cfg.Assets.RuntimeRun, "runtime-run", "", "completed shared runtime-image Run; import locally before site bootstrap")
	flags.StringVar(&cfg.Assets.BootstrapRun, "bootstrap-run", "", "completed controller and provisioned-provider Run")
	flags.StringVar(&cfg.Assets.DeploymentRun, "deployment-run", "", "completed profiles and CAPNICo Run")
	flags.BoolVar(&cfg.Cleanup, "cleanup", false, "delete owned VMs after acceptance; otherwise retain until Ctrl-C")
	for _, flag := range []string{"image", "out", "nvkr", "tools-run", "bootstrap-run", "deployment-run", "runtime-run"} {
		_ = command.MarkFlagRequired(flag)
	}
	return command
}
