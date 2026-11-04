// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
)

// TestRetiredPathChecksRejectOnlyTrackedRetiredPaths runs each retired-path
// recipe from the real Makefile against a scratch Git repository, so a no-op
// recipe or a widened or misspelled pathspec fails here.
func TestRetiredPathChecksRejectOnlyTrackedRetiredPaths(t *testing.T) {
	makefile, err := filepath.Abs("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		target    string
		tracked   []string
		untracked []string
		wantError string // empty: the check passes
	}{
		{name: "ledger clean", target: "reconciliation-location-check"},
		{name: "ledger split files", target: "reconciliation-location-check",
			tracked: []string{"docs/adr/reconciliation/README.md", "docs/adr/reconciliation/adr-04.md"}},
		{name: "ledger untracked", target: "reconciliation-location-check",
			untracked: []string{"docs/adr/reconciliation.md"}},
		{name: "ledger tracked", target: "reconciliation-location-check",
			tracked:   []string{"docs/adr/reconciliation.md"},
			wantError: "the reconciliation ledger is split under docs/adr/reconciliation/"},
		{name: "definitions clean", target: "definitions-location-check",
			tracked: []string{"inputs/definitions/nvke/core.yaml"}},
		{name: "definitions tracked", target: "definitions-location-check",
			tracked:   []string{"definitions/nvke.yaml"},
			wantError: "authored Definitions belong under inputs/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := t.TempDir()
			runIn(t, repo, "git", "init", "-q")
			for _, name := range append(append([]string{}, tt.tracked...), tt.untracked...) {
				writeFile(t, filepath.Join(repo, name))
			}
			if len(tt.tracked) > 0 {
				runIn(t, repo, "git", append([]string{"add", "--"}, tt.tracked...)...)
			}
			stderr, err := makeTarget(t, makefile, repo, tt.target)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("%s failed: %v\n%s", tt.target, err, stderr)
				}
				return
			}
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) {
				t.Fatalf("%s = %v, want a failing exit\n%s", tt.target, err, stderr)
			}
			if !strings.Contains(stderr, tt.wantError) || !strings.Contains(stderr, tt.tracked[0]) {
				t.Fatalf("%s stderr lacks %q or the tracked path:\n%s", tt.target, tt.wantError, stderr)
			}
		})
	}
}

func makeTarget(t *testing.T, makefile, dir, target string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "make", "-s", "-f", makefile, "-C", dir, "--no-print-directory", target)
	// An enclosing make (for example `make test`) must not pass its flags down.
	command.Env = append(command.Environ(), "MAKEFLAGS=", "MFLAGS=", "MAKELEVEL=")
	var stderr bytes.Buffer
	command.Stdout, command.Stderr = &stderr, &stderr
	err := ownedcmd.Run(ctx, command)
	return stderr.String(), err
}

func runIn(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Env = append(command.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := ownedcmd.Run(ctx, command); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output.Bytes())
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
