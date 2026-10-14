// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/planview"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

func TestPublicCommandPresentationCoverage(t *testing.T) {
	var leaves []string
	canonicalDiagnostics := 0
	var visit func([]*cli.Command, string)
	visit = func(commands []*cli.Command, parent string) {
		for _, command := range commands {
			if command.Hidden {
				continue
			}
			name := strings.TrimSpace(parent + " " + command.Name)
			if len(command.Subcommands) > 0 {
				visit(command.Subcommands, name)
				continue
			}
			leaves = append(leaves, name)
			flags := make(map[string]bool)
			for _, flag := range command.Flags {
				for _, name := range flag.Names() {
					flags[name] = true
				}
			}
			if name == "run check-artifacts" {
				// Qualification diagnostics retain their canonical JSON contract.
				canonicalDiagnostics++
				if flags["ui"] || flags["no-color"] {
					t.Errorf("canonical diagnostic command %s unexpectedly offers presentation flags", name)
				}
			} else if !flags["ui"] || !flags["no-color"] {
				t.Errorf("public command %s has no presentation flags", name)
			}
		}
	}
	visit(newApp().Commands, "")
	if len(leaves) != 23 || canonicalDiagnostics != 1 {
		t.Fatalf("public coverage changed: %d leaves, %d canonical diagnostic commands: %v", len(leaves), canonicalDiagnostics, leaves)
	}
}

func TestCommandViewerPreservesOutcomeAndRejectsAmbiguousNavigation(t *testing.T) {
	for _, test := range []struct {
		name  string
		items []progress.Item
		code  e.Code
		want  e.Code
		shown bool
	}{
		{"sorted", []progress.Item{{ID: "b", Fields: commandFields("Name", "b"), Dependencies: []string{"a"}}, {ID: "a", Fields: commandFields("Name", "a"), Dependencies: []string{"b"}}}, "", "", true},
		{"owning difference", nil, e.CodeDiff, e.CodeDiff, true},
		{"owning failure", nil, e.CodeBuildFailed, e.CodeBuildFailed, true},
		{"missing key", []progress.Item{{Name: "a"}}, "", e.CodeInternal, false},
		{"duplicate key", []progress.Item{{ID: "a"}, {ID: "a"}}, "", e.CodeInternal, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			shown := false
			services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, options runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
				shown = true
				if options.Live || work != nil {
					t.Fatal("result viewer starts work")
				}
				snapshot := source.Snapshot()
				if snapshot.Mode != "command" || snapshot.FailureCode != string(test.code) {
					t.Fatalf("wrong outcome: %+v", snapshot)
				}
				if len(snapshot.Items) == 2 && snapshot.Items[0].ID != "a" {
					t.Fatal("unstable order")
				}
				raw, _ := json.Marshal(snapshot)
				if strings.Contains(string(raw), "private-sentinel") {
					t.Fatal("owner error leaked")
				}
				if test.name == "sorted" {
					snapshot.Items[0].Fields[0].Value = "reader mutation"
					snapshot.Items[0].Dependencies[0] = "reader mutation"
					fresh := source.Snapshot()
					if fresh.Items[0].Fields[0].Value != "a" || fresh.Items[0].Dependencies[0] != "b" || test.items[1].Fields[0].Value != "a" || test.items[1].Dependencies[0] != "b" {
						t.Fatal("nested reader mutation changed the retained or caller description")
					}
					// Conversely, the caller cannot alter a retained viewer snapshot.
					test.items[1].Fields[0].Value = "caller mutation"
					test.items[1].Dependencies[0] = "caller mutation"
					fresh = source.Snapshot()
					if fresh.Items[0].Fields[0].Value != "a" || fresh.Items[0].Dependencies[0] != "b" {
						t.Fatal("nested caller mutation changed the retained viewer description")
					}
				}
				return runpresenter.Result{}
			}}
			app := &cli.App{Metadata: map[string]interface{}{commandPresentationServicesKey: services}, Action: func(c *cli.Context) error {
				var ownerErr error
				if test.code != "" {
					ownerErr = e.New(test.code, "private-sentinel")
				}
				return showCommandResult(c, runPresentation{tui: true}, commandDisplay{Title: "Result", Items: test.items}, ownerErr)
			}}
			err := app.RunContext(t.Context(), []string{"nvkr"})
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertLifecycleCommandCode(t, err, test.want)
			}
			if shown != test.shown {
				t.Fatal("unexpected viewer invocation")
			}
			if len(test.items) == 2 && test.name == "sorted" && test.items[0].ID != "b" {
				t.Fatal("caller result was mutated")
			}
		})
	}
}

func BenchmarkCommandResultProjection(b *testing.B) {
	display := commandDisplay{Title: "Catalog"}
	for index := 63; index >= 0; index-- {
		name := strconv.Itoa(index)
		display.Items = append(display.Items, progress.Item{ID: name, Fields: commandFields("Name", name, "Kind", "image"), Dependencies: []string{"source", "toolchain"}})
	}
	services := runPresentationServices{show: func(_ context.Context, _ runpresenter.Options, source runSnapshotSource, _ func(context.Context) error) runpresenter.Result {
		_ = source.Snapshot()
		return runpresenter.Result{}
	}, eligible: func(io.Writer) bool { return true }}
	app := &cli.App{Metadata: map[string]interface{}{commandPresentationServicesKey: services}}
	c := cli.NewContext(app, flag.NewFlagSet("benchmark", flag.ContinueOnError), nil)
	c.Context = b.Context()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := showCommandResult(c, runPresentation{tui: true}, display, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func TestRootCommandAdmissionBeforeInputIO(t *testing.T) {
	for _, args := range [][]string{
		{"definition", "validate", "--definition", "missing"}, {"definition", "inspect", "--definition", "missing"},
		{"run", "generate", "--definition", "missing", "--environment", "missing"},
		{"run", "graph", "--plan", "missing"}, {"run", "diff", "--before", "missing", "--after", "missing"}, {"resolve", "--data", "missing"},
		{"release", "export", "--reference", "missing"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			app := newApp()
			app.Metadata = map[string]interface{}{commandPresentationServicesKey: runPresentationServices{eligible: func(io.Writer) bool { return false }, show: func(context.Context, runpresenter.Options, runSnapshotSource, func(context.Context) error) runpresenter.Result {
				t.Fatal("rejected viewer opened")
				return runpresenter.Result{}
			}}}
			out, err := runCLIWithCapturedStdout(t, app, append(append([]string{"nvkr"}, args...), "--ui=tui")...)
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			if len(out) != 0 {
				t.Fatal("rejected mode emitted bytes")
			}
		})
	}
}

func TestRootCommandExplicitJSONConflictsBeforeInputIO(t *testing.T) {
	for _, args := range [][]string{
		{"resolve", "--data", "missing", "--output", "canonical"},
		{"resolve", "--data", "missing", "-o", "nodes"},
		{"run", "graph", "--plan", "missing", "--format", "json"},
		{"run", "diff", "--before", "missing", "--after", "missing", "--format", "json"},
		{"release", "export", "--reference", "missing", "--format", "json"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			app := newApp()
			app.Metadata = map[string]interface{}{commandPresentationServicesKey: runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(context.Context, runpresenter.Options, runSnapshotSource, func(context.Context) error) runpresenter.Result {
				t.Fatal("conflicting format reached viewer")
				return runpresenter.Result{}
			}}}
			out, err := runCLIWithCapturedStdout(t, app, append(append([]string{"nvkr"}, args...), "--ui=tui")...)
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			if len(out) != 0 {
				t.Fatal("conflicting format emitted bytes")
			}
		})
	}
}

func TestGraphAndComparisonProjectionExcludeTransportAndRawValues(t *testing.T) {
	graph := graphDisplay(planview.Graph{Vertices: []planview.Vertex{{Key: "route/public", Kind: "route", Label: "https://private-sentinel", Reference: "public"}}})
	comparison := comparisonDisplay(&planview.Comparison{Changes: []planview.Change{{Category: "transport", Path: "/private-sentinel", Before: json.RawMessage(`"private-sentinel"`)}}})
	for _, display := range []commandDisplay{graph, comparison} {
		raw, err := json.Marshal(display)
		if err != nil || strings.Contains(string(raw), "private-sentinel") {
			t.Fatalf("unsafe projection: %s %v", raw, err)
		}
	}
}

func TestRootCommandTypedResultsAndDefaultBytes(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, args := range [][]string{
		{"definition", "validate", "--definition", "definitions/nvke-v1/definition.yaml"},
		{"definition", "inspect", "--definition", "definitions/nvke-v1/definition.yaml"},
		{"run", "graph", "--plan", ".github/nvkr-run/plan.json", "--view", "producers"},
		{"run", "diff", "--before", ".github/nvkr-run/plan.json", "--after", ".github/nvkr-run/plan.json", "--exit-code"},
		{"resolve", "--data", "testdata/catalogs/kubernetes-layered", "--component", "kubectl", "--platform", "linux/amd64"},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			plain, err := runCLIWithCapturedStdout(t, newApp(), append([]string{"nvkr"}, args...)...)
			if err != nil {
				t.Fatal(err)
			}
			explicit, err := runCLIWithCapturedStdout(t, newApp(), append(append([]string{"nvkr"}, args...), "--ui=plain")...)
			if err != nil || string(plain) != string(explicit) {
				t.Fatal("explicit plain changed default bytes", err)
			}
			shown := false
			app := newApp()
			app.Metadata = map[string]interface{}{commandPresentationServicesKey: runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, options runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
				shown = true
				snapshot := source.Snapshot()
				if snapshot.Status != "complete" || snapshot.Mode != "command" || len(snapshot.Fields) == 0 || options.Live || work != nil {
					t.Fatalf("not a typed read-only result: %+v", snapshot)
				}
				if args[1] == "graph" {
					for _, item := range snapshot.Items {
						if item.Kind != "producer" {
							t.Fatal("ignored chosen graph view")
						}
					}
				}
				return runpresenter.Result{}
			}}}
			out, err := runCLIWithCapturedStdout(t, app, append(append([]string{"nvkr"}, args...), "--ui=tui")...)
			if err != nil || !shown || len(out) != 0 {
				t.Fatalf("TUI wrote plain bytes or did not open: %s %v", out, err)
			}
		})
	}
}

func TestReadOnlyPlanAutoSelectsEligibleTerminalWithoutLiveWork(t *testing.T) {
	t.Setenv("CI", "")
	lifecycleCommandRepo(t)
	shown := false
	app := newApp()
	app.Metadata = map[string]interface{}{commandPresentationServicesKey: runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, options runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
		shown = true
		if work != nil || options.Live || len(source.Snapshot().Outputs) == 0 {
			t.Fatal("Plan explorer became live work or lost outputs")
		}
		return runpresenter.Result{}
	}}}
	out, err := runCLIWithCapturedStdout(t, app, "nvkr", "run", "plan", "--plan", ".github/nvkr-run/plan.json", "--ui=auto")
	if err != nil || !shown || len(out) != 0 {
		t.Fatalf("read-only auto did not open eligible viewer: %v %s", err, out)
	}
}

func TestPlanDifferenceViewerRetainsRequestedExitStatus(t *testing.T) {
	lifecycleCommandRepo(t)
	before := ".github/nvkr-run/plan.json"
	plan, err := factory.LoadPlanContext(t.Context(), before)
	if err != nil {
		t.Fatal(err)
	}
	plan.GeneratorVersion = "comparison-fixture"
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	after := filepath.Join(t.TempDir(), "after.json")
	if err := os.WriteFile(after, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	shown := false
	app := newApp()
	app.Metadata = map[string]interface{}{commandPresentationServicesKey: runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, _ runpresenter.Options, source runSnapshotSource, _ func(context.Context) error) runpresenter.Result {
		shown = true
		snapshot := source.Snapshot()
		if snapshot.Status != "different" || snapshot.FailureCode != string(e.CodeDiff) || len(snapshot.Items) == 0 {
			t.Fatalf("comparison result lost differences: %+v", snapshot)
		}
		return runpresenter.Result{}
	}}}
	out, err := runCLIWithCapturedStdout(t, app, "nvkr", "run", "diff", "--before", before, "--after", after, "--exit-code", "--ui=tui")
	assertLifecycleCommandCode(t, err, e.CodeDiff)
	if !shown || len(out) != 0 {
		t.Fatal("comparison skipped viewer or emitted plain bytes")
	}
}
