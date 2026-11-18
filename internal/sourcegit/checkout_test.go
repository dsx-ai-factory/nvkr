// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sourcegit

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestVerifyCheckoutIgnoresRunnerConfiguration(t *testing.T) {
	for _, mode := range []string{"plain", "runner-include", "dirty", "assume-unchanged", "skip-worktree", "wrong-head", "untracked", "inert-descriptor", "crlf", "crlf-attribute-only"} {
		t.Run(mode, func(t *testing.T) {
			root, commit := localFixtureRepository(t)
			if mode == "crlf" || mode == "crlf-attribute-only" {
				if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("input.txt text eol=crlf\n"), 0644); err != nil {
					t.Fatal(err)
				}
				localFixtureGit(t, root, "add", ".gitattributes")
				localFixtureGit(t, root, "-c", "commit.gpgsign=false", "commit", "-qm", "CRLF attributes")
				commit = localFixtureGit(t, root, "rev-parse", "HEAD")
				if mode == "crlf" {
					if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("pinned source\r\n"), 0644); err != nil {
						t.Fatal(err)
					}
				}
				// Git regards both worktrees as clean after conversion. Admission
				// must instead distinguish raw LF bytes from the CRLF variant.
				localFixtureGit(t, root, "diff", "--exit-code", "HEAD", "--", "input.txt")
			}
			if mode == "inert-descriptor" {
				if err := os.WriteFile(filepath.Join(root, ".gitmodules"), nil, 0644); err != nil {
					t.Fatal(err)
				}
				localFixtureGit(t, root, "add", ".gitmodules")
				localFixtureGit(t, root, "-c", "commit.gpgsign=false", "commit", "-qm", "inert descriptor")
				commit = localFixtureGit(t, root, "rev-parse", "HEAD")
			}
			config := filepath.Join(root, ".git", "config")
			if mode != "plain" {
				// Runner 19.4 writes a local include.path for external credential config.
				// A malformed included file proves inspection never loads its contents.
				included := filepath.Join(t.TempDir(), ".gitlab-runner.ext.conf")
				if err := os.WriteFile(included, []byte("[invalid\n"), 0600); err != nil {
					t.Fatal(err)
				}
				localFixtureGit(t, root, "config", "--local", "include.path", included)
			}
			before, err := os.ReadFile(config)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "dirty" || mode == "assume-unchanged" || mode == "skip-worktree" {
				if err := os.WriteFile(filepath.Join(root, "input.txt"), []byte("changed source\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			// Use the configuration-free view for index flag setup too; fixture Git
			// configuration includes are intentionally malformed in the source checkout.
			if mode == "assume-unchanged" || mode == "skip-worktree" {
				// git config --no-includes cannot control other commands. Temporarily
				// remove the fixture include only for setup, then restore the exact bytes.
				local := []byte("[core]\nrepositoryformatversion = 0\n")
				if err := os.WriteFile(config, local, 0600); err != nil {
					t.Fatal(err)
				}
				localFixtureGit(t, root, "update-index", "--"+mode, "input.txt")
				if err := os.WriteFile(config, before, 0600); err != nil {
					t.Fatal(err)
				}
			}
			paths := []string{"input.txt"}
			if mode == "wrong-head" {
				commit = strings.Repeat("f", 40)
			}
			if mode == "untracked" {
				paths = []string{"untracked.txt"}
				if err := os.WriteFile(filepath.Join(root, paths[0]), []byte("untracked"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			indexBefore, err := os.ReadFile(filepath.Join(root, ".git", "index"))
			if err != nil {
				t.Fatal(err)
			}
			err = VerifyCheckout(t.Context(), []string{"PATH=" + os.Getenv("PATH")}, root, commit, paths, 100)
			if (err == nil) != (mode == "plain" || mode == "runner-include" || mode == "inert-descriptor" || mode == "crlf-attribute-only") {
				t.Fatalf("VerifyCheckout(%s)=%v", mode, err)
			}
			if mode == "crlf" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeProvenanceMissing {
					t.Fatalf("CRLF mismatch returned %v", err)
				}
			}
			indexAfter, indexErr := os.ReadFile(filepath.Join(root, ".git", "index"))
			if indexErr != nil || !bytes.Equal(indexBefore, indexAfter) {
				t.Fatal("inspection changed checkout index")
			}
			after, readErr := os.ReadFile(config)
			if readErr != nil || !bytes.Equal(before, after) {
				t.Fatal("inspection changed checkout configuration")
			}
		})
	}
}
