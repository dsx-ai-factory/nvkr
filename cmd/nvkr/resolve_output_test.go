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

// resolveOutputFormats is the list the usage error must name. It is kept separate from
// the validator on purpose: adding a format means updating both, and this test fails
// if the error text stops naming one of them.
var resolveOutputFormats = []string{"ir", "canonical", "nodes", "digest", "components"}

// resolveOutputQueryArgs selects a producer from the kubernetes-layered test catalog,
// so a valid format would resolve successfully.
func resolveOutputQueryArgs() []string {
	return []string{
		"--component", "kubectl", "--platform", "linux/amd64",
		"--data", filepath.Join("..", "..", "testdata", "catalogs", "kubernetes-layered"),
	}
}

// assertResolveOutputUsageError checks the full fail-closed contract: a CodeUsage error
// that names every accepted format, and nothing written to stdout.
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
			// The --output=value form is needed so the empty value reaches the flag.
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
	// The terminal is reported as eligible and the viewer fails the test if it is ever
	// opened, so an invalid format must be rejected before the TUI branch is taken.
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
