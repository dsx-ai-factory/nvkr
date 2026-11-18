// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/inputinspect"
	"github.com/urfave/cli/v2"
)

func TestExplainCommand(t *testing.T) {
	root := t.TempDir()
	raw, err := os.ReadFile("../../demos/hello/environment.yaml")
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "environment.yaml")
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		args []string
		want e.Code
	}{
		{"plain", []string{"--root", root, file}, ""},
		{"json", []string{"--root", root, "--json", file}, ""},
		{"missing root", []string{file}, e.CodeUsage},
		{"extra file", []string{"--root", root, file, file}, e.CodeUsage},
		{"json tui", []string{"--root", root, "--json", "--ui=tui", file}, e.CodeUsage},
		{"missing file", []string{"--root", root, filepath.Join(root, "absent.yaml")}, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			app := newApp()
			app.Writer = &out
			err := app.RunContext(t.Context(), append([]string{"nvkr", "explain"}, tc.args...))
			if tc.want != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.want {
					t.Fatalf("got %v want %s", err, tc.want)
				}
				if tc.name == "missing file" && !strings.Contains(err.Error(), "not found") {
					t.Fatalf("misleading missing target: %v", err)
				}
				if out.Len() != 0 {
					t.Fatalf("partial output: %s", out.String())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.name == "json" {
				var got inputinspect.Report
				if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Kind != "Environment" {
					t.Fatalf("JSON: %s %v", out.String(), err)
				}
			} else if !strings.Contains(out.String(), "Scanned root:") {
				t.Fatalf("no scope: %s", out.String())
			}
		})
	}
}

func TestExplainTUIKeepsReferenceOrderAndMappings(t *testing.T) {
	report := inputinspect.Report{Kind: "Definition", Path: "input.yaml", Scope: "test scope"}
	for i := 0; i < 12; i++ {
		report.References = append(report.References, inputinspect.Reference{Path: fmt.Sprintf("overlay-%d", i), Relation: "ordered overlay"})
	}
	report.Consumers = []inputinspect.Reference{{Path: "consumer.yaml", Relation: "fragment source bindings (fragment -> local)", Mappings: map[string]string{"original": "local"}}}
	shown := false
	services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, _ runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
		shown = true
		if work != nil {
			t.Fatal("viewer started work")
		}
		snapshot := source.Snapshot()
		for i := 0; i < 12; i++ {
			if snapshot.Items[i].Name != fmt.Sprintf("overlay-%d", i) {
				t.Fatalf("overlay order changed: %+v", snapshot.Items)
			}
		}
		fields := snapshot.Items[12].Fields
		foundDirection, foundMapping := false, false
		for _, f := range fields {
			if f.Value == "Reverse reference" {
				foundDirection = true
			}
			if f.Value == "local" {
				foundMapping = true
			}
		}
		if !foundDirection || !foundMapping {
			t.Fatalf("lost reverse mapping: %+v", fields)
		}
		return runpresenter.Result{}
	}}
	app := &cli.App{Metadata: map[string]interface{}{commandPresentationServicesKey: services}, Action: func(c *cli.Context) error {
		return showCommandResult(c, runPresentation{tui: true}, explanationDisplay(report), nil)
	}}
	if err := app.RunContext(t.Context(), []string{"nvkr"}); err != nil {
		t.Fatal(err)
	}
	if !shown {
		t.Fatal("viewer was not shown")
	}
}
