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
// recipe or a widened or misspelled pathspec fails here. Each case runs again
// with Git variables naming a caller repository, which must stay unread and unchanged.
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
	for _, callerRepository := range []bool{false, true} {
		for _, tt := range tests {
			name := tt.name
			if callerRepository {
				name += " with caller Git variables"
			}
			t.Run(name, func(t *testing.T) {
				if callerRepository {
					// A caller repository that tracks every retired path: a leaked variable
					// makes the clean cases fail or changes the caller's index.
					caller := t.TempDir()
					runIn(t, caller, "git", "init", "-q")
					retired := []string{"docs/adr/reconciliation.md", "definitions/nvke.yaml"}
					for _, name := range retired {
						writeFile(t, filepath.Join(caller, name))
					}
					runIn(t, caller, "git", append([]string{"add", "--"}, retired...)...)
					index := filepath.Join(caller, ".git", "index")
					before := readFile(t, index)
					t.Setenv("GIT_DIR", filepath.Join(caller, ".git"))
					t.Setenv("GIT_WORK_TREE", caller)
					t.Setenv("GIT_INDEX_FILE", index)
					t.Cleanup(func() {
						if !bytes.Equal(readFile(t, index), before) {
							t.Error("the caller's Git index changed")
						}
					})
				}
				checkRetiredPathCase(t, makefile, tt.target, tt.tracked, tt.untracked, tt.wantError)
			})
		}
	}
}

func checkRetiredPathCase(t *testing.T, makefile, target string, tracked, untracked []string, wantError string) {
	t.Helper()
	repo := t.TempDir()
	runIn(t, repo, "git", "init", "-q")
	for _, name := range append(append([]string{}, tracked...), untracked...) {
		writeFile(t, filepath.Join(repo, name))
	}
	if len(tracked) > 0 {
		runIn(t, repo, "git", append([]string{"add", "--"}, tracked...)...)
	}
	stderr, err := makeTarget(t, makefile, repo, target)
	if wantError == "" {
		if err != nil {
			t.Fatalf("%s failed: %v\n%s", target, err, stderr)
		}
		return
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("%s = %v, want a failing exit\n%s", target, err, stderr)
	}
	if !strings.Contains(stderr, wantError) || !strings.Contains(stderr, tracked[0]) {
		t.Fatalf("%s stderr lacks %q or the tracked path:\n%s", target, wantError, stderr)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func makeTarget(t *testing.T, makefile, dir, target string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "make", "-s", "-f", makefile, "-C", dir, "--no-print-directory", target)
	// An enclosing make (for example `make test`) must not pass its flags down.
	command.Env = scratchGitEnv(command.Environ(), "MAKEFLAGS=", "MFLAGS=", "MAKELEVEL=")
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
	command.Env = scratchGitEnv(command.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := ownedcmd.Run(ctx, command); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output.Bytes())
	}
}

// scratchGitEnv drops inherited variables that would point Git at the caller's
// repository or index (for example from a hook), then appends overrides.
func scratchGitEnv(inherited []string, overrides ...string) []string {
	env := make([]string, 0, len(inherited)+len(overrides))
	for _, value := range inherited {
		key, _, _ := strings.Cut(value, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_SHALLOW_FILE", "GIT_PREFIX":
			continue
		}
		env = append(env, value)
	}
	return append(env, overrides...)
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
