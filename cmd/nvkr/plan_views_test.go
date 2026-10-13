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
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/planview"
)

func TestRunPlanFileViewsRetainCanonicalBytes(t *testing.T) {
	lifecycleCommandRepo(t)
	path := ".github/nvkr-run/plan.json"
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		flags []string
	}{{"canonical", []string{"--json"}}, {"pretty", []string{"--pretty"}}, {"text", nil}} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"run", "plan", "--plan", path}, test.flags...)
			output, err := invokeLifecycleCLI(t, args...)
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "canonical" && !bytes.Equal([]byte(output), original) {
				t.Fatal("canonical CLI output is not exact stored bytes")
			}
			if test.name == "pretty" {
				var plan factory.Plan
				if err := json.Unmarshal([]byte(output), &plan); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(output, "\n  ") {
					t.Fatal("pretty did not indent")
				}
				if canonical.UnmarshalExact([]byte(output), &plan) == nil {
					t.Fatal("pretty display became executable Plan input")
				}
			}
			if test.name == "text" && !strings.Contains(output, "Artifact operations") {
				t.Fatal("no readable operations")
			}
		})
	}
	unchanged, _ := os.ReadFile(path)
	if !bytes.Equal(unchanged, original) {
		t.Fatal("inspection modified generated Plan")
	}
}

func TestRunGraphFormats(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, format := range []string{"json", "dot", "mermaid"} {
		t.Run(format, func(t *testing.T) {
			output, err := invokeLifecycleCLI(t, "run", "graph", "--plan", ".github/nvkr-run/plan.json", "--format", format)
			if err != nil {
				t.Fatal(err)
			}
			if format == "json" {
				var graph planview.Graph
				if err := json.Unmarshal([]byte(output), &graph); err != nil {
					t.Fatal(err)
				}
				if len(graph.Vertices) == 0 || len(graph.Edges) == 0 || graph.View != "complete" {
					t.Fatal("missing complete graph")
				}
			} else if !strings.Contains(output, "controller-chart") {
				t.Fatal("graph missing required output")
			}
		})
	}
}

func TestRunDiffCountsAllChangesAndSupportsExitStatus(t *testing.T) {
	lifecycleCommandRepo(t)
	before := ".github/nvkr-run/plan.json"
	p, err := factory.LoadPlanContext(t.Context(), before)
	if err != nil {
		t.Fatal(err)
	}
	p.GeneratorVersion = "new-generator"
	raw, err := canonical.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	after := filepath.Join(t.TempDir(), "after.json")
	if err := os.WriteFile(after, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name  string
		flags []string
		after string
		code  e.Code
	}{{"summary", nil, after, ""}, {"details", []string{"--details"}, after, ""}, {"json", []string{"--format", "json"}, after, ""}, {"status", []string{"--exit-code"}, after, e.CodeDiff}, {"equal", []string{"--exit-code"}, before, ""}} {
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"run", "diff", "--before", before, "--after", test.after}, test.flags...)
			output, err := invokeLifecycleCLI(t, args...)
			if test.code != "" {
				assertLifecycleCommandCode(t, err, test.code)
			} else if err != nil {
				t.Fatal(err)
			}
			if test.name == "json" {
				var comparison planview.Comparison
				if err := json.Unmarshal([]byte(output), &comparison); err != nil {
					t.Fatal(err)
				}
				if len(comparison.Changes) != 1 || len(comparison.Builds) != 0 {
					t.Fatalf("unexpected changes: %+v", comparison)
				}
			} else if test.name == "equal" {
				if !strings.Contains(output, "No changes") {
					t.Fatal("missing equal report")
				}
			} else if !strings.Contains(output, "generatorVersion") || !strings.Contains(output, "0 changed/added/removed") {
				t.Fatalf("missing semantic cause: %s", output)
			}
		})
	}
}

func TestPlanViewCommandsRejectBeforeOutput(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, test := range []struct {
		name string
		args []string
		code e.Code
	}{
		{"mixed inputs", []string{"run", "plan", "--plan", "missing", "--definition", "missing"}, e.CodeUsage},
		{"missing inputs", []string{"run", "plan"}, e.CodeUsage},
		{"extra arguments", []string{"run", "plan", "--plan", "missing", "argument"}, e.CodeUsage},
		{"bad graph view", []string{"run", "graph", "--plan", "missing", "--view", "unknown"}, e.CodeUsage},
		{"bad graph format", []string{"run", "graph", "--plan", "missing", "--format", "unknown"}, e.CodeUsage},
		{"bad diff format", []string{"run", "diff", "--before", "missing", "--after", "missing", "--format", "unknown"}, e.CodeUsage},
		{"JSON details before input", []string{"run", "diff", "--before", "missing", "--after", "missing", "--format", "json", "--details"}, e.CodeUsage},
		{"JSON details with valid plans", []string{"run", "diff", "--before", ".github/nvkr-run/plan.json", "--after", ".github/nvkr-run/plan.json", "--format", "json", "--details"}, e.CodeUsage},
		{"missing plan", []string{"run", "graph", "--plan", "missing"}, e.CodePipelineDrift},
		{"missing after", []string{"run", "diff", "--before", ".github/nvkr-run/plan.json", "--after", "missing"}, e.CodePipelineDrift},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := invokeLifecycleCLI(t, test.args...)
			assertLifecycleCommandCode(t, err, test.code)
			if output != "" {
				t.Fatalf("failed command emitted partial result: %q", output)
			}
		})
	}
}
