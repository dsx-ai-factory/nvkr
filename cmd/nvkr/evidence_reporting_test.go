// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/urfave/cli/v2"
)

func TestEvidenceReportCLIAdmissionAndCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		extras []string
		want   e.Code
	}{
		{"unknown format", t.Context(), []string{"--format", "invalid"}, e.CodeUsage},
		{"unpaired TestGrid target", t.Context(), []string{"--testgrid-target", "/not-read"}, e.CodeUsage},
		{"canceled", canceled, nil, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "report.json")
			args := []string{"nvkr", "report", "--inputs", "/not-read", "--cosign", "/not-run", "--cosign-digest", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "--out", out}
			args = append(args, tc.extras...)
			app := &cli.App{Writer: io.Discard, ErrWriter: io.Discard, Commands: []*cli.Command{cmdEvidenceReport()}}
			err := app.RunContext(tc.ctx, args)
			assertLifecycleCommandCode(t, err, tc.want)
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatal("CLI reporting admission touched output")
			}
		})
	}
}
