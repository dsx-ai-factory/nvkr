// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/spf13/cobra"
)

func newQualificationSetupCommand(out io.Writer, actions demoActions, config *string) *cobra.Command {
	var destination string
	command := &cobra.Command{Use: "qualification-setup", Short: "Prepare local demo keys, pinned cosign, policy and a disk-backed registry for an existing prepare-only attempt", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if *config == "" || destination == "" {
				return e.New(e.CodeUsage, "qualification-setup requires --config and a fresh --out directory")
			}
			file, err := actions.load(cmd.Context(), *config)
			if err != nil {
				return err
			}
			path, err := nvkedemo.SetupQualification(cmd.Context(), file, destination, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(out, "Local qualification setup ready: %s\nUse this configuration with nvke-demo build or e2e. The registry remains running for independent read-back; qualification-registry-stop --out stops it without deleting evidence or keys.\n", path)
			return err
		}}
	command.Flags().StringVar(&destination, "out", "", "fresh private setup directory on native disk; parent must exist")
	return command
}

func newQualificationRegistryStopCommand() *cobra.Command {
	var destination string
	command := &cobra.Command{Use: "qualification-registry-stop", Short: "Stop the setup-owned registry while retaining its data, keys and evidence", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if destination == "" {
				return e.New(e.CodeUsage, "qualification-registry-stop requires --out")
			}
			return nvkedemo.StopQualificationRegistry(cmd.Context(), destination)
		}}
	command.Flags().StringVar(&destination, "out", "", "existing qualification setup directory")
	return command
}
