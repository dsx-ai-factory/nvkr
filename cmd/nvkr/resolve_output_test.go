// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

var resolveOutputFormats = []string{"ir", "canonical", "nodes", "digest", "components"}

func resolveOutputQueryArgs() []string {
	return []string{
		"--component", "kubectl", "--platform", "linux/amd64",
		"--data", filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered"),
	}
}

func assertResolveOutputUsageError(t *testing.T, err error, got string) {
	t.Helper()
	assertLifecycleCommandCode(t, err, e.CodeUsage)
	if got != "" {
		t.Fatalf("rejected output format emitted bytes: %q", got)
	}
	for _, format := range resolveOutputFormats {
		if !strings.Contains(err.Error(), format) {
			t.Fatalf("error %q does not name the accepted format %q", err, format)
		}
	}
}

func TestResolveOutputRejectsUnknownFormats(t *testing.T) {
	// "jsno" is the issue's typo, "" is an explicit empty value, and "Digest"
	// checks that the formats are case-sensitive.
	for _, format := range []string{"jsno", "", "Digest"} {
		t.Run("output="+format, func(t *testing.T) {
			args := append([]string{"nvkr", "resolve"}, resolveOutputQueryArgs()...)
			out, err := runCLIWithCapturedStdout(t, newApp(), append(args, "--output="+format)...)
			assertResolveOutputUsageError(t, err, string(out))
		})
	}
}

func TestResolveOutputFormatErrorWinsOverMissingCatalog(t *testing.T) {
	// An invalid format must be admitted before any catalog I/O, so the format
	// error is reported even when the catalog path does not exist.
	out, err := runCLIWithCapturedStdout(t, newApp(),
		"nvkr", "resolve", "--data", "missing", "--output", "jsno")
	assertResolveOutputUsageError(t, err, string(out))
}

func TestResolveOutputInvalidFormatCannotTakeTheTUIBranch(t *testing.T) {
	app := newApp()
	app.Metadata = map[string]interface{}{commandPresentationServicesKey: runPresentationServices{
		eligible: func(io.Writer) bool { return true },
		show: func(context.Context, runpresenter.Options, runSnapshotSource, func(context.Context) error) runpresenter.Result {
			t.Fatal("invalid output format reached the viewer")
			return runpresenter.Result{}
		},
	}}
	args := append([]string{"nvkr", "resolve"}, resolveOutputQueryArgs()...)
	out, err := runCLIWithCapturedStdout(t, app, append(args, "--output", "jsno", "--ui=tui")...)
	assertResolveOutputUsageError(t, err, string(out))
}
