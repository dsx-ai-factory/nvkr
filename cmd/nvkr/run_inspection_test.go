// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

func TestRunInspectionCLIIsReadOnlyAndSafe(t *testing.T) {
	lifecycleCommandRepo(t)
	prepared, err := lifecycle.PrepareFiles(t.Context(), "definitions/nvke-v1/definition.yaml", "definitions/nvke-v1/environment-local.yaml", "test", "inspection-test")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(root, "lifecycle.json")
	run := prepared.Description()
	raw, err := canonical.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"plain", "json"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			app := &cli.App{Writer: &out, Commands: []*cli.Command{cmdRun()}}
			args := []string{"nvkr", "run", "inspect", "--record", record}
			if format == "json" {
				args = append(args, "--json")
			}
			if err := app.RunContext(t.Context(), args); err != nil {
				t.Fatal(err)
			}
			if format == "json" {
				var result struct {
					Authority   string                  `json:"authority"`
					Observation string                  `json:"observation"`
					Record      lifecycle.RunInspection `json:"record"`
				}
				if err := json.Unmarshal(out.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Authority != "description-only" || result.Observation != "file-recorded" || result.Record.Status != lifecycle.RunPending || len(result.Record.Outputs) != 3 {
					t.Fatalf("unsafe or incomplete inspection: %+v", result)
				}
			} else if !strings.Contains(out.String(), "metadata: file-recorded") || !strings.Contains(out.String(), "payload verification: not performed") {
				t.Fatal("missing scope labels")
			}
			if strings.Contains(out.String(), root) || strings.ContainsRune(out.String(), '\x1b') {
				t.Fatal("path or terminal control leaked")
			}
			current, err := os.ReadFile(record)
			if err != nil || !bytes.Equal(current, raw) {
				t.Fatal("inspection modified record")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatal("inspection created files")
			}
		})
	}
}

func TestRunInspectionCLIReportsMissingRecordWithoutInventingRun(t *testing.T) {
	var out bytes.Buffer
	app := &cli.App{Writer: &out, Commands: []*cli.Command{cmdRun()}}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "lifecycle.json")
	if err := app.RunContext(t.Context(), []string{"nvkr", "run", "inspect", "--record", path, "--json"}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Record lifecycle.RunInspection `json:"record"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Record.Status != "unavailable" || result.Record.RecordAvailable || !result.Record.Interrupted {
		t.Fatalf("missing record invented terminal result: %+v", result)
	}
}

func TestReadOnlyModesRejectBeforeInputOrOutput(t *testing.T) {
	for _, args := range [][]string{
		{"run", "inspect", "--record", "missing/lifecycle.json", "--ui=tui"},
		{"run", "inspect", "--record", "missing/lifecycle.json", "--ui=tui", "--json"},
		{"run", "inspect", "--record", "missing/lifecycle.json", "--ui=unknown"},
		{"run", "inspect", "--record", "missing/lifecycle.json", "extra"},
		{"run", "plan", "--plan", "missing", "--ui=tui"},
		{"run", "plan", "--plan", "missing", "--ui=tui", "--json"},
		{"run", "plan", "--plan", "missing", "--ui=tui", "--pretty"},
		{"run", "plan", "--plan", "missing", "--ui=unknown"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out bytes.Buffer
			app := &cli.App{Writer: &out, Commands: []*cli.Command{cmdRun()}}
			err := app.RunContext(t.Context(), append([]string{"nvkr"}, args...))
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			if out.Len() != 0 {
				t.Fatal("rejected mode emitted result")
			}
		})
	}
}

func TestRunInspectionFailureIsDescriptiveAndDoesNotExposeCause(t *testing.T) {
	lifecycleCommandRepo(t)
	prepared, err := lifecycle.PrepareFiles(t.Context(), "definitions/nvke-v1/definition.yaml", "definitions/nvke-v1/environment-local.yaml", "test", "failed-inspection")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canonical.Marshal(prepared.Description())
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := canonical.UnmarshalExact(raw, &record); err != nil {
		t.Fatal(err)
	}
	record["status"] = lifecycle.RunFailed
	record["failure"] = map[string]any{"code": string(e.CodeBuildFailed), "message": "private provider credential-sentinel /private/host"}
	raw, err = canonical.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "lifecycle.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"plain", "json"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			args := []string{"nvkr", "run", "inspect", "--record", path}
			if format == "json" {
				args = append(args, "--json")
			}
			app := &cli.App{Writer: &out, Commands: []*cli.Command{cmdRun()}}
			if err := app.RunContext(t.Context(), args); err != nil {
				t.Fatalf("description inherited failed Run execution status: %v", err)
			}
			if !strings.Contains(out.String(), string(e.CodeBuildFailed)) || strings.Contains(out.String(), "credential-sentinel") || strings.Contains(out.String(), "/private/host") {
				t.Fatalf("unsafe inspection: %q", out.String())
			}
		})
	}
}
