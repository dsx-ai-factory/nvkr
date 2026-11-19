// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sourcegit

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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

func TestCheckCheckoutCredentials(t *testing.T) {
	const jobToken = "SYNTHETIC_JOB_TOKEN_12345"
	const readerToken = "SYNTHETIC_READER_TOKEN_67890"
	for _, scenario := range []struct {
		name string
		code e.Code
	}{
		{"native-helper", ""},
		{"direct-url", e.CodeEngineCapability},
		{"included-token", e.CodeEngineCapability},
		{"escaped-token", e.CodeEngineCapability},
		{"direct-comment", e.CodeEngineCapability},
		{"malformed-config", e.CodeEngineCapability},
		{"malformed-include", e.CodeEngineCapability},
		{"oversized-config", e.CodeEngineCapability},
		{"oversized-include", e.CodeEngineCapability},
		{"symlink-config", e.CodeEngineCapability},
		{"canceled", e.CodeTimeout},
		{"nil-context", e.CodeUsage},
		{"relative-root", e.CodeUsage},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			root, _ := localFixtureRepository(t)
			config := filepath.Join(root, ".git", "config")
			included := filepath.Join(root, ".git", "runner.include")
			marker := filepath.Join(t.TempDir(), "helper-must-not-run")
			helper := "!f() { printf '%s' \"${CI_JOB_TOKEN}\"; touch '" + marker + "'; }; f"
			includeBytes := []byte("[credential]\nhelper = " + strconv.Quote(helper) + "\n")
			if err := os.WriteFile(included, includeBytes, 0600); err != nil {
				t.Fatal("could not prepare included fixture")
			}
			localFixtureGit(t, root, "config", "--local", "include.path", included)
			configBytes, err := os.ReadFile(config)
			if err != nil {
				t.Fatal("could not read fixture configuration")
			}
			switch scenario.name {
			case "direct-url":
				configBytes = append(configBytes, []byte("\n[remote \"origin\"]\nurl = https://gitlab-ci-token:"+jobToken+"@gitlab.example.test/project.git\n")...)
			case "included-token":
				includeBytes = append(includeBytes, []byte("\n[http]\nextraHeader = "+readerToken+"\n")...)
			case "escaped-token":
				includeBytes = append(includeBytes, []byte("\n[credential]\nusername = \"SYNTHETIC_JOB_\\\nTOKEN_12345\"\n")...)
			case "direct-comment":
				configBytes = append(configBytes, []byte("\n# "+jobToken+"\n")...)
			case "malformed-config":
				configBytes = []byte("[invalid-" + jobToken + "\n")
			case "malformed-include":
				includeBytes = []byte("[invalid-" + readerToken + "\n")
			case "oversized-config":
				configBytes = append(configBytes, []byte("\n#"+strings.Repeat("x", 64<<10))...)
			case "oversized-include":
				includeBytes = append(includeBytes, []byte("\n[credential]\nusername = "+strings.Repeat("x", 64<<10)+"\n")...)
			}
			if err := os.WriteFile(config, configBytes, 0600); err != nil {
				t.Fatal("could not prepare fixture configuration")
			}
			if err := os.WriteFile(included, includeBytes, 0600); err != nil {
				t.Fatal("could not prepare included fixture")
			}
			if scenario.name == "symlink-config" {
				target := filepath.Join(t.TempDir(), "config")
				if err := os.Rename(config, target); err != nil {
					t.Fatal("could not move fixture configuration")
				}
				if err := os.Symlink(target, config); err != nil {
					t.Fatal("could not prepare symlink fixture")
				}
			}
			indexPath := filepath.Join(root, ".git", "index")
			indexBefore, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal("could not read fixture index")
			}
			// Ambient credential configuration is outside the admitted query.
			environment := []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=" + included, "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=http.extraHeader", "GIT_CONFIG_VALUE_0=" + readerToken}
			ctx := t.Context()
			switch scenario.name {
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil-context":
				ctx = nil
			}
			directory := root
			if scenario.name == "relative-root" {
				directory = "."
			}
			err = CheckCheckoutCredentials(ctx, environment, directory, "", jobToken, readerToken)
			var coded *e.Error
			if scenario.code == "" {
				if err != nil {
					t.Fatalf("clean native helper was refused: %v", err)
				}
			} else if !errors.As(err, &coded) || coded.Code != scenario.code {
				t.Fatal("credential inspection did not return its expected coded refusal")
			}
			if err != nil {
				for _, private := range []string{jobToken, readerToken, root, included, marker} {
					if strings.Contains(err.Error(), private) {
						t.Fatal("credential inspection disclosed private configuration")
					}
				}
			}
			if scenario.name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatal("credential inspection lost caller cancellation")
			}
			for path, expected := range map[string][]byte{config: configBytes, included: includeBytes, indexPath: indexBefore} {
				actual, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(actual, expected) {
					t.Fatal("credential inspection mutated checkout metadata")
				}
			}
			if _, err := os.Lstat(marker); !os.IsNotExist(err) {
				t.Fatal("credential inspection executed a credential helper")
			}
		})
	}
}
