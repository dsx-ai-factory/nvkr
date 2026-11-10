// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

// recordedAttempt writes one tutorial Run record under root, completed with an
// observation for every required output, or failed with a build failure.
func recordedAttempt(t *testing.T, root, attempt, status string) {
	t.Helper()
	prepared, err := lifecycle.PrepareFiles(t.Context(), "docs/examples/nvke-tutorial/definition.yaml", "docs/examples/nvke-tutorial/environment-local.yaml", "test", attempt)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(prepared.Description())
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	record["status"] = status
	switch status {
	case lifecycle.RunFailed:
		record["failure"] = map[string]any{"code": string(e.CodeBuildFailed), "message": "fixture failure"}
	case lifecycle.RunComplete:
		var observed []any
		for index, slot := range record["expected"].([]any) {
			slot := slot.(map[string]any)
			observed = append(observed, map[string]any{
				"name": slot["name"], "kind": slot["kind"], "buildDigest": slot["buildDigest"], "components": slot["components"],
				"digest": "sha256:" + strings.Repeat(string(rune('a'+index)), 64),
			})
		}
		record["observed"] = observed
	}
	raw, err = canonical.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, attempt)
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "lifecycle.json"), raw, 0o400); err != nil {
		t.Fatal(err)
	}
}

func runListCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	app := &cli.App{Writer: &out, Commands: []*cli.Command{cmdRun()}}
	err := app.RunContext(t.Context(), append([]string{"nvkr", "run", "list"}, args...))
	return out.String(), err
}

func attemptsTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		files[path] = string(raw)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestRunListCLIListsRecordedAttempts(t *testing.T) {
	lifecycleCommandRepo(t)
	work, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(work, ".nvkr", "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	recordedAttempt(t, filepath.Join(work, ".nvkr", "runs"), "list-failed", lifecycle.RunFailed)
	recordedAttempt(t, filepath.Join(work, ".nvkr", "runs"), "list-complete", lifecycle.RunComplete)
	// The issue's command: the default relative attempts root of a project.
	t.Chdir(work)
	root := filepath.Join(".nvkr", "runs")
	before := attemptsTree(t, root)

	output, err := runListCLI(t, "--attempts-root", root, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var list lifecycle.RunList
	if err := json.Unmarshal([]byte(output), &list); err != nil {
		t.Fatalf("output is not one JSON listing: %v\n%s", err, output)
	}
	if len(list.Runs) != 2 || len(list.Diagnostics) != 0 || list.Runs[0].Attempt != "list-complete" || list.Runs[1].Attempt != "list-failed" {
		t.Fatalf("wrong listing: %+v", list)
	}
	complete, failed := list.Runs[0], list.Runs[1]
	if complete.Status != lifecycle.RunComplete || complete.ObservedOutputs != complete.RequiredOutputs || complete.RequiredOutputs == 0 || complete.FailureCode != "" {
		t.Fatalf("wrong complete attempt: %+v", complete)
	}
	if failed.Status != lifecycle.RunFailed || failed.ObservedOutputs != 0 || failed.RequiredOutputs != complete.RequiredOutputs || failed.FailureCode != string(e.CodeBuildFailed) {
		t.Fatalf("wrong failed attempt: %+v", failed)
	}
	if _, err := lifecycle.InspectRun(t.Context(), complete.Record); err != nil {
		t.Fatalf("listed record locator is not inspectable: %v", err)
	}
	if strings.Contains(output, "fixture failure") {
		t.Fatal("listing exposed a raw failure message")
	}

	text, err := runListCLI(t, "--attempts-root", root)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"runs: 2\n", `"list-complete": complete`, `"list-failed": failed`, "failure " + string(e.CodeBuildFailed)} {
		if !strings.Contains(text, line) {
			t.Fatalf("text lacks %q:\n%s", line, text)
		}
	}
	if !maps.Equal(before, attemptsTree(t, root)) {
		t.Fatal("run list changed the attempts root")
	}
}

func TestRunListCLIFailsOnAPartialInventory(t *testing.T) {
	lifecycleCommandRepo(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	recordedAttempt(t, root, "listed", lifecycle.RunFailed)
	if err := os.Mkdir(filepath.Join(root, "reserved"), 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := runListCLI(t, "--attempts-root", root, "--json")
	assertLifecycleCommandCode(t, err, e.CodeVerifyFailed)
	var list lifecycle.RunList
	if jsonErr := json.Unmarshal([]byte(output), &list); jsonErr != nil || len(list.Runs) != 1 || len(list.Diagnostics) != 1 || list.Diagnostics[0].Entry != "reserved" {
		t.Fatalf("partial inventory is not reported with its diagnostic: %v %s", jsonErr, output)
	}
	if strings.Contains(strings.ToLower(output), "interrupted") {
		t.Fatalf("a missing record is called interrupted: %s", output)
	}
	for _, args := range [][]string{{"--attempts-root", root, "extra"}, {"--attempts-root", ""}} {
		output, err := runListCLI(t, args...)
		assertLifecycleCommandCode(t, err, e.CodeUsage)
		if output != "" {
			t.Fatalf("usage failure emitted output: %s", output)
		}
	}
}
