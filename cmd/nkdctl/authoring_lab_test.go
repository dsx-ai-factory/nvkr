// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// TestAuthoringLabCLIProcess runs the real CLI adapter in a subprocess so the
// executable documentation exercises argv, output and coded exit contracts.
func TestAuthoringLabCLIProcess(t *testing.T) {
	if os.Getenv("NKD_AUTHORING_TEST_HELPER") != "1" {
		return
	}
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 {
		t.Fatal("helper invocation has no argument separator")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	err := newApp().RunContext(ctx, append([]string{"nkdctl"}, os.Args[separator+1:]...))
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(e.ExitCode(err))
}

func TestAuthoringLabReplacementJourney(t *testing.T) {
	for _, inherited := range []bool{false, true} {
		name := "clean"
		if inherited {
			name = "inherited repository overrides"
		}
		t.Run(name, func(t *testing.T) {
			runAuthoringLabReplacementJourney(t, inherited)
		})
	}
}

func runAuthoringLabReplacementJourney(t *testing.T, inherited bool) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("authoring lab requires Bash: ", err)
	}
	fixture, err := filepath.Abs(filepath.Join("..", "..", "docs", "examples", "authoring-lab", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	wrapper := filepath.Join(root, "nkdctl with spaces")
	const wrapperScript = `#!/bin/sh
for key in GIT_DIR GIT_WORK_TREE GIT_COMMON_DIR GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES GIT_PREFIX; do
  if env | grep -q "^$key="; then
    printf 'inherited Git override reached CLI: %s\n' "$key" >&2
    exit 1
  fi
done
exec "$NKD_AUTHORING_TEST_EXE" -test.run='^TestAuthoringLabCLIProcess$' -- "$@"
`
	if err := os.WriteFile(wrapper, []byte(wrapperScript), 0o700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "lab with spaces")
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, fixture, "--nkdctl", wrapper, "--work", work)
	cmd.Env = append(os.Environ(), "NKD_AUTHORING_TEST_HELPER=1", "NKD_AUTHORING_TEST_EXE="+executable)
	if inherited {
		caller := filepath.Join(root, "caller with spaces")
		overrides := authoringLabCallerRepository(t, caller)
		before := authoringLabSnapshot(t, caller)
		t.Cleanup(func() {
			if after := authoringLabSnapshot(t, caller); !maps.Equal(before, after) {
				t.Error("authoring lab modified the caller repository, index, objects, or worktree")
			}
		})
		cmd.Env = append(cmd.Env, overrides...)
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("authoring journey: %v\n%s", err, output)
	}
	var report struct {
		Scope     string `json:"scope"`
		Scenarios map[string]struct {
			ChangedOutputs []string `json:"changedOutputs"`
		} `json:"scenarios"`
	}
	raw, err := os.ReadFile(filepath.Join(work, "identity-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatal(err)
	}
	if len(report.Scenarios) != 4 || !strings.Contains(report.Scope, "no artifact execution") {
		t.Fatalf("incomplete or misleading authoring report: %+v", report)
	}
	for _, tc := range []struct {
		name string
		want []string
	}{
		{"source-pin", []string{"cli-amd64", "cli-arm64", "controller", "controller-chart"}},
		{"whole-producer", []string{"diagnostics", "tools"}},
		{"output-catalog", []string{"tools"}},
		{"output-selection", []string{"diagnostics"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := report.Scenarios[tc.name].ChangedOutputs; !slices.Equal(got, tc.want) {
				t.Fatalf("changed outputs = %v; want %v", got, tc.want)
			}
		})
	}
	// The script must refuse reusing a workspace instead of deleting or silently
	// modifying retained inputs and the evidence of the preceding journey.
	cmd = exec.CommandContext(ctx, bash, fixture, "--nkdctl", wrapper, "--work", work)
	output, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "workspace already exists:") {
		t.Fatalf("existing workspace was not rejected: %v\n%s", err, output)
	}
	after, err := os.ReadFile(filepath.Join(work, "identity-report.json"))
	if err != nil || string(after) != string(raw) {
		t.Fatal("refused rerun modified the identity report")
	}
}

func TestAuthoringLabMissingPrerequisitePreservesWorkspace(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := filepath.Abs(filepath.Join("..", "..", "docs", "examples", "authoring-lab", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	// Git is present, but jq is absent. The script must fail during prerequisite
	// admission, before creating the requested workspace or invoking the CLI.
	if err := os.Symlink(git, filepath.Join(root, "git")); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "must not be created")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, fixture, "--nkdctl", bash, "--work", work)
	cmd.Env = append(os.Environ(), "PATH="+root)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "missing required command: jq") {
		t.Fatalf("missing prerequisite was not rejected: %v\n%s", err, output)
	}
	if _, err := os.Lstat(work); !os.IsNotExist(err) {
		t.Fatalf("workspace created before prerequisite admission: %v", err)
	}
}

// authoringLabCallerRepository supplies real foreign Git paths, with staged and
// unstaged changes that the lab must preserve along with all repository metadata.
func authoringLabCallerRepository(t *testing.T, root string) []string {
	t.Helper()
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	var cleanEnv []string
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cleanEnv = append(cleanEnv, entry)
		}
	}
	cleanEnv = append(cleanEnv, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	git := func(args ...string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root,
			"-c", "commit.gpgsign=false", "-c", "user.name=Caller Fixture",
			"-c", "user.email=caller@example.invalid"}, args...)...)
		cmd.Env = cleanEnv
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("create caller repository: %v\n%s", err, output)
		}
	}
	git("init", "-q", "--template=", "--object-format=sha1", "--initial-branch=main")
	file := filepath.Join(root, "keep.txt")
	for i, contents := range []string{"committed\n", "staged\n", "unstaged\n"} {
		if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			git("add", "keep.txt")
		}
		if i == 0 {
			git("commit", "-q", "-m", "caller fixture")
		}
	}
	gitDir := filepath.Join(root, ".git")
	return []string{
		"GIT_DIR=" + gitDir,
		"GIT_WORK_TREE=" + root,
		"GIT_COMMON_DIR=" + gitDir,
		"GIT_INDEX_FILE=" + filepath.Join(gitDir, "index"),
		"GIT_OBJECT_DIRECTORY=" + filepath.Join(gitDir, "objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=" + filepath.Join(gitDir, "objects"),
		"GIT_PREFIX=caller-prefix/",
	}
}

func authoringLabSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			files[path] = "directory"
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = string(contents)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return files
}
