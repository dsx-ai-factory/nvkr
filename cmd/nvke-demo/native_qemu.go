// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/nativeqemu"
	"github.com/spf13/cobra"
)

func launcherArguments(executable string, args []string) []string {
	if filepath.Base(executable) == nativeqemu.LauncherName {
		return append([]string{"native-qemu"}, args...)
	}
	return args
}

func newNativeQEMUCommand() *cobra.Command {
	return &cobra.Command{
		Use: "native-qemu", Hidden: true, DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return nativeqemu.Run(cmd.Context(), args)
		},
	}
}
