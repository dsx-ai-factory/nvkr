// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestArtifactDiagnosticFlags(t *testing.T) {
	for _, args := range [][]string{
		{"--artifact", "unused"}, {"--plan", "unused"},
		{"--plan", "unused", "--artifact", "unused", "extra"},
		{"--plan", "unused", "--artifact", "unused", "--suite", "invalid"},
		{"--plan", "unused", "--artifact", "unused", "--timeout", "0s"},
		{"--plan", "unused", "--artifact", "unused", "--nvkr", "unused"},
	} {
		t.Run(args[len(args)-1], func(t *testing.T) {
			app := newApp()
			app.Writer, app.ErrWriter = io.Discard, io.Discard
			err := app.RunContext(t.Context(), append([]string{"nvkr", "run", "artifact-test"}, args...))
			if code := e.ExitCode(err); code != 2 {
				t.Fatalf("exit=%d: %v", code, err)
			}
		})
	}
}
