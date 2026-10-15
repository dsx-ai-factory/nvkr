// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sourcegit

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
