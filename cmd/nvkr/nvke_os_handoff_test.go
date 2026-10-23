// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Execute the real workflow transfer against a shallow Git checkout. The
// canonical repository override serves the one exact pinned source commit.
func TestOSWorkflowTransfersExactSourceCommit(t *testing.T) {
	workflow := osValidationWorkflow(t)
	var packageStep, unpackStep osWorkflowStep
	for _, step := range workflow["fetch-nvke-source"].Steps {
		if step.Name == "Authenticate and package credential-free source" {
			packageStep = step
		}
	}
	for _, step := range workflow["validate-full"].Steps {
		if step.Name == "Authenticate and unpack pinned NVKE source" {
			unpackStep = step
		}
	}
	if packageStep.Run == "" || unpackStep.Run == "" {
		t.Fatal("source package or unpack step is missing")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	git := func(directory string, args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir, cmd.WaitDelay = directory, 5*time.Second
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git fixture operation: %v: %s", err, output)
		}
		return strings.TrimSpace(string(output))
	}
	origin := t.TempDir()
	git(origin, "init", "-q")
	git(origin, "config", "user.name", "Workflow Fixture")
	git(origin, "config", "user.email", "workflow@example.invalid")
	if err := os.WriteFile(filepath.Join(origin, "version"), []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(origin, "add", "version")
	git(origin, "-c", "commit.gpgsign=false", "commit", "-qm", "old fixture")
	if err := os.WriteFile(filepath.Join(origin, "version"), []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	git(origin, "add", "version")
	git(origin, "-c", "commit.gpgsign=false", "commit", "-qm", "release fixture")
	primary, primaryTree := git(origin, "rev-parse", "HEAD"), git(origin, "rev-parse", "HEAD^{tree}")
	git(origin, "branch", "primary")
	for _, scenario := range []string{"exact source", "wrong tree"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".trusted", "nvke")
			git(root, "clone", "--quiet", "--depth=1", "--branch=primary", "file://"+origin, path)
			git(path, "checkout", "--quiet", "--detach")
			if git(path, "rev-list", "--count", "HEAD") != "1" {
				t.Fatal("source fixture must be shallow")
			}
			wantTree := primaryTree
			if scenario == "wrong tree" {
				wantTree = strings.Repeat("f", 40)
			}
			cmd := exec.CommandContext(t.Context(), "bash", "-euc", packageStep.Run)
			cmd.Dir, cmd.WaitDelay = root, 5*time.Second
			cmd.Env = append(os.Environ(), "NVKE_COMMIT="+primary, "NVKE_TREE="+wantTree,
				"GITHUB_OUTPUT="+filepath.Join(root, "outputs"))
			output, err := cmd.CombinedOutput()
			if scenario != "exact source" {
				if err == nil {
					t.Fatal("substituted source tree was transferred")
				}
				if _, err := os.Stat(filepath.Join(root, ".handoff")); !os.IsNotExist(err) {
					t.Fatal("substituted source performed transfer work")
				}
				return
			}
			if err != nil {
				t.Fatalf("exact source transfer: %v: %s", err, output)
			}
			handoff := filepath.Join(root, ".handoff", "nvke")
			if git(handoff, "rev-parse", "HEAD") != primary {
				t.Fatal("source handoff lost the exact pin")
			}
			outputs, err := os.ReadFile(filepath.Join(root, "outputs"))
			if err != nil {
				t.Fatal(err)
			}
			digest, ok := strings.CutPrefix(strings.TrimSpace(string(outputs)), "archive_digest=")
			if !ok || len(digest) != 64 {
				t.Fatal("source package did not report its archive digest")
			}
			archive, err := os.ReadFile(filepath.Join(root, ".handoff", "nvke-source.tar"))
			if err != nil {
				t.Fatal(err)
			}
			consumer := t.TempDir()
			if err := os.Mkdir(filepath.Join(consumer, ".trusted-transfer"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(consumer, ".trusted-transfer", "nvke-source.tar"), archive, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd = exec.CommandContext(t.Context(), "bash", "-euc", unpackStep.Run)
			cmd.Dir, cmd.WaitDelay = consumer, 5*time.Second
			cmd.Env = append(os.Environ(), "NVKE_COMMIT="+primary, "NVKE_TREE="+primaryTree,
				"SOURCE_ARCHIVE_DIGEST="+digest)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("exact source unpack: %v: %s", err, output)
			}
			receiver := t.TempDir()
			git(receiver, "init", "-q")
			git(receiver, "fetch", "--quiet", "--depth=1", "file://"+filepath.Join(consumer, ".trusted", "nvke"), primary)
			if git(receiver, "rev-parse", "FETCH_HEAD") != primary {
				t.Fatal("canonical transport cannot fetch the exact selected commit")
			}
		})
	}
}
