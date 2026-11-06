// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/urfave/cli/v2"
)

func TestToolchainMaintenanceCLIRejectsBeforeOutput(t *testing.T) {
	t.Setenv("GITHUB_ACTIONS", "")
	for _, tc := range []struct {
		name string
		args []string
		code e.Code
	}{
		{"unknown", []string{"--operation", "unknown"}, e.CodeUsage},
		{"positional", []string{"--operation", "describe", "extra"}, e.CodeUsage},
		{"unknown architecture", []string{"--operation", "describe", "--arch", "riscv64"}, e.CodeUsage},
		{"untrusted publication", []string{"--operation", "publish", "--run", "123"}, e.CodeEngineCapability},
		{"untrusted repin", []string{"--operation", "repin", "--run", "123"}, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			app := &cli.App{Writer: &out, ErrWriter: &out, Commands: []*cli.Command{cmdFactory()}}
			err := app.RunContext(t.Context(), append([]string{"nvkr", "factory", "toolchain-maintenance"}, tc.args...))
			if cliErrorCode(err) != tc.code || out.Len() != 0 {
				t.Fatalf("got %v / %q, want %s and no output", err, out.String(), tc.code)
			}
		})
	}
}
