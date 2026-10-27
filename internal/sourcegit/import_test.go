// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sourcegit

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestImportLocalExactSourceAndRoutingIsolation(t *testing.T) {
	source, commit := localFixtureRepository(t)
	tree := localFixtureGit(t, source, "rev-parse", "HEAD^{tree}")
	caller, callerCommit := localFixtureRepository(t)
	config := localFixtureGit(t, caller, "config", "--local", "--list")
	before, err := os.ReadFile(filepath.Join(caller, "input.txt"))
	if err != nil {
		t.Fatal(err)
	}
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "GIT_DIR=" + filepath.Join(caller, ".git"), "GIT_WORK_TREE=" + caller,
		"GIT_INDEX_FILE=" + filepath.Join(caller, ".git", "index")}
	for _, test := range []struct {
		name, revision, tree string
		fails                bool
	}{
		{"exact", commit, tree, false}, {"wrong-tree", commit, strings.Repeat("a", 40), true},
		{"missing-commit", strings.Repeat("b", 40), tree, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "space:comma,quote\"")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "nvke")
			err := ImportLocal(t.Context(), environment, destination, source, test.revision, test.tree, io.Discard)
			if (err != nil) != test.fails {
				t.Fatalf("import: %v", err)
			}
			if test.fails && errorCode(err) != e.CodeFetchFailed {
				t.Fatalf("source failure code: %v", err)
			}
			if !test.fails {
				if actual := localFixtureGit(t, destination, "rev-parse", "HEAD"); actual != commit {
					t.Fatal(actual)
				}
				if _, err := os.Stat(filepath.Join(destination, ".git", "shallow")); err != nil {
					t.Fatal(err)
				}
			} else if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatalf("failed import published source: %v", err)
			}
			if actual := localFixtureGit(t, caller, "rev-parse", "HEAD"); actual != callerCommit {
				t.Fatal("caller HEAD changed")
			}
			if actual := localFixtureGit(t, caller, "config", "--local", "--list"); actual != config {
				t.Fatal("caller configuration changed")
			}
			after, err := os.ReadFile(filepath.Join(caller, "input.txt"))
			if err != nil || string(after) != string(before) {
				t.Fatal("caller bytes changed", err)
			}
		})
	}
}

func TestImportLocalPreservesAdmissionCode(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "nvke")
	err := ImportLocal(t.Context(), []string{"PATH="}, destination, "unused", "unused", "unused", io.Discard)
	if errorCode(err) != e.CodeEngineCapability {
		t.Fatalf("source admission recoded: %v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("refused admission published source", err)
	}
}

func TestImportLocalRejectsDirtyMaterialization(t *testing.T) {
	source, commit := localFixtureRepository(t)
	tree := localFixtureGit(t, source, "rev-parse", "HEAD^{tree}")
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	wrapper := t.TempDir()
	// Delegate every command to real Git, changing materialized bytes only when
	// the import reaches its final cleanliness query (after revision checks).
	script := "#!/bin/sh\nprevious= directory=\nfor argument do\n" +
		"  if [ \"$previous\" = -C ]; then directory=$argument; fi\n" +
		"  if [ \"$argument\" = status ]; then printf 'modified\\n' >> \"$directory/input.txt\"; fi\n" +
		"  previous=$argument\ndone\nexec '" + strings.ReplaceAll(git, "'", "'\"'\"'") + "' \"$@\"\n"
	if err := os.WriteFile(filepath.Join(wrapper, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	destination := filepath.Join(root, "nvke")
	err = ImportLocal(t.Context(), []string{"PATH=" + wrapper + string(os.PathListSeparator) + os.Getenv("PATH")}, destination, source, commit, tree, io.Discard)
	if errorCode(err) != e.CodeFetchFailed || !strings.Contains(err.Error(), "source is modified") {
		t.Fatalf("dirty materialization accepted or wrong failure: %v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatal("dirty import published checkout", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatal("dirty import left scratch or output", entries, err)
	}
}

func TestImportLocalWorkspaceOnSeparateFilesystem(t *testing.T) {
	root, err := os.MkdirTemp("/dev/shm", "nvkr-import-")
	if err != nil {
		t.Skip("separate writable filesystem unavailable:", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(probe, filepath.Join(root, "probe")); !errors.Is(err, syscall.EXDEV) {
		t.Skip("fixture does not cross filesystems:", err)
	}
	source, commit := localFixtureRepository(t)
	tree := localFixtureGit(t, source, "rev-parse", "HEAD^{tree}")
	for _, name := range []string{"ordinary", "space:comma,quote\""} {
		t.Run(name, func(t *testing.T) {
			workspace := filepath.Join(root, name)
			if err := os.Mkdir(workspace, 0o700); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(workspace, "nvke")
			if err := ImportLocal(t.Context(), []string{"PATH=" + os.Getenv("PATH")}, destination, source, commit, tree, io.Discard); err != nil {
				t.Fatal(err)
			}
			if actual := localFixtureGit(t, destination, "rev-parse", "HEAD"); actual != commit {
				t.Fatal(actual)
			}
		})
	}
}

func TestImportLocalRepositoryIsRemoteAddPositional(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }
	for _, repository := range []string{"https://example.invalid/source", "-review@host:repository"} {
		t.Run(repository, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "origin.txt")
			// Run real Git through init/remote-add, then record the stored URL
			// and refuse fetch locally. No network or SSH client is invoked.
			script := "#!/bin/sh\nprevious= directory=\nfor argument do\n" +
				"  if [ \"$previous\" = -C ]; then directory=$argument; fi\n" +
				"  if [ \"$argument\" = fetch ]; then\n" +
				"    " + quote(git) + " -C \"$directory\" config --get remote.origin.url > " + quote(marker) + " || exit 98\n" +
				"    exit 42\n  fi\n  previous=$argument\ndone\nexec " + quote(git) + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(root, "git"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(root, "nvke")
			err := ImportLocal(t.Context(), []string{"PATH=" + root + string(os.PathListSeparator) + os.Getenv("PATH")}, destination, repository, strings.Repeat("a", 40), strings.Repeat("b", 40), io.Discard)
			var exit *exec.ExitError
			if errorCode(err) != e.CodeFetchFailed || !errors.As(err, &exit) || exit.ExitCode() != 42 {
				t.Fatalf("repository became an option or passed the fetch guard: %v", err)
			}
			stored, readErr := os.ReadFile(marker)
			if readErr != nil || strings.TrimSpace(string(stored)) != repository {
				t.Fatalf("remote URL changed: %q %v", stored, readErr)
			}
			if _, err := os.Stat(destination); !os.IsNotExist(err) {
				t.Fatal("refused fetch published source", err)
			}
		})
	}
}
