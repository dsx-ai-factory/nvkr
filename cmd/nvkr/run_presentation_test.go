// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/rundiagnostics"
	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

func localPresentationTestApp(execute localLifecycleExecutor, output io.Writer, services runPresentationServices) *cli.App {
	// Injected executors own their fake telemetry, just as ExecuteLocal owns
	// production finalization. The CLI adapter does not reconcile it again.
	owner := execute
	execute = func(ctx context.Context, prepared *lifecycle.PreparedRun, options lifecycle.LocalOptions) (*lifecycle.Run, error) {
		result, err := owner(ctx, prepared, options)
		status, lane := "unavailable", ""
		var observed []string
		if result != nil {
			status, lane = result.Status, result.Lane
			for _, output := range result.Observed {
				observed = append(observed, output.Name)
			}
		}
		options.Progress.Finish(status, lane, observed, string(runErrorCode(err)), result != nil && !options.DryRun, result != nil && result.Status == lifecycle.RunComplete && err != nil)
		return result, err
	}
	command := cmdRunWithLocalExecutor(execute)
	for _, child := range command.Subcommands {
		if child.Name == "local" {
			child.Action = func(c *cli.Context) error { return runLocalCommandWithServices(c, execute, services) }
		}
	}
	return &cli.App{Writer: output, Commands: []*cli.Command{command}}
}

func localPresentationArgs(flags ...string) []string {
	return append([]string{"nvkr", "run", "local", "--definition", "docs/examples/nvke-tutorial/definition.yaml", "--environment", "docs/examples/nvke-tutorial/environment-local.yaml", "--attempt", "presenter-test"}, flags...)
}

func completedPresentationRun(prepared *lifecycle.PreparedRun) *lifecycle.Run {
	run := prepared.Description()
	run.Status, run.Lane = lifecycle.RunComplete, "unqualified-local-execution"
	for _, slot := range run.Expected {
		run.Observed = append(run.Observed, lifecycle.Artifact{Name: slot.Name, Kind: slot.Kind, BuildDigest: slot.BuildDigest, Components: slot.Components, Digest: "sha256:" + strings.Repeat("a", 64)})
	}
	return &run
}

func TestLocalPresentationModeAdmissionBeforeInputs(t *testing.T) {
	for _, test := range []struct {
		name  string
		flags []string
	}{
		{"unknown", []string{"--ui=unknown"}},
		{"redirected explicit", []string{"--ui=tui"}},
		{"JSON conflict", []string{"--ui=tui", "--json"}},
		{"positional", []string{"unexpected"}},
		{"empty attempts root", []string{"--attempts-root="}},
		{"empty attempts root JSON", []string{"--attempts-root=", "--json"}},
		{"empty attempts root dry run", []string{"--attempts-root=", "--dry-run"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			called := false
			execute := func(context.Context, *lifecycle.PreparedRun, lifecycle.LocalOptions) (*lifecycle.Run, error) {
				called = true
				return nil, nil
			}
			app := localPresentationTestApp(execute, &out, defaultRunPresentationServices())
			args := []string{"nvkr", "run", "local", "--definition", "missing", "--environment", "missing", "--attempt", "fresh"}
			err := app.RunContext(t.Context(), append(args, test.flags...))
			assertLifecycleCommandCode(t, err, e.CodeUsage)
			if called || out.Len() != 0 {
				t.Fatalf("rejected mode performed work or wrote result: %t %q", called, out.String())
			}
		})
	}
}

func TestLocalPresentationModesPreserveOwningOutcome(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, ownerCode := range []e.Code{"", e.CodeBuildFailed, e.CodeTimeout} {
		for _, mode := range []string{"plain", "json", "tui"} {
			t.Run(string(ownerCode)+"/"+mode, func(t *testing.T) {
				var out bytes.Buffer
				execute := func(_ context.Context, prepared *lifecycle.PreparedRun, options lifecycle.LocalOptions) (*lifecycle.Run, error) {
					if options.Progress == nil {
						t.Fatal("missing descriptive monitor")
					}
					if mode == "tui" {
						if _, ok := options.Stdout.(*rundiagnostics.Buffer); !ok || options.Stdout != options.Stderr {
							t.Fatal("producer logs have no shared private memory collector")
						}
					}
					if ownerCode == "" {
						return completedPresentationRun(prepared), nil
					}
					run := prepared.Description()
					run.Status = lifecycle.RunFailed
					return &run, e.New(ownerCode, "private provider message credential-sentinel")
				}
				shown := false
				services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(ctx context.Context, opts runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
					shown = true
					if !opts.Live || work == nil {
						t.Fatal("live Run became static viewer")
					}
					workErr := work(ctx)
					snapshot := source.Snapshot()
					if len(snapshot.Outputs) != 3 {
						t.Fatal("frozen output denominator changed")
					}
					return runpresenter.Result{WorkErr: workErr}
				}}
				app := localPresentationTestApp(execute, &out, services)
				flags := []string{"--ui=" + mode}
				if mode == "json" {
					flags = []string{"--ui=auto", "--json"}
				}
				err := app.RunContext(t.Context(), localPresentationArgs(flags...))
				if e.ExitCode(err) != e.ExitCode(e.New(ownerCode, "")) && ownerCode != "" {
					t.Fatalf("owning code changed: %v", err)
				}
				if ownerCode == "" && err != nil {
					t.Fatal(err)
				}
				if shown != (mode == "tui") {
					t.Fatalf("unexpected mode dispatch: shown=%t mode=%s", shown, mode)
				}
				if mode == "json" {
					var summary localRunResult
					if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
						t.Fatal(err)
					}
					if len(summary.RequiredOutputs) != 3 || summary.FailureCode != string(ownerCode) {
						t.Fatalf("summary lost frozen outputs/code: %+v", summary)
					}
					if strings.Contains(out.String(), "credential-sentinel") || strings.ContainsRune(out.String(), '\x1b') {
						t.Fatal("unsafe JSON result")
					}
				}
			})
		}
	}
}

func TestLocalJSONDiagnosticsAndFrozenRequirements(t *testing.T) {
	lifecycleCommandRepo(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStderr := os.Stderr
	os.Stderr = writer
	t.Cleanup(func() { os.Stderr = oldStderr; _ = reader.Close(); _ = writer.Close() })
	var out bytes.Buffer
	execute := func(_ context.Context, prepared *lifecycle.PreparedRun, options lifecycle.LocalOptions) (*lifecycle.Run, error) {
		if options.Stdout != writer || options.Stderr != writer {
			t.Fatal("JSON diagnostics did not select stderr")
		}
		_, _ = io.WriteString(options.Stdout, "private-stdout-sentinel\n")
		_, _ = io.WriteString(options.Stderr, "private-stderr-sentinel\n")
		run := prepared.Description()
		run.Status, run.Expected = lifecycle.RunFailed, nil
		return &run, e.New(e.CodeBuildFailed, "private-error-sentinel")
	}
	app := localPresentationTestApp(execute, &out, defaultRunPresentationServices())
	err = app.RunContext(t.Context(), localPresentationArgs("--json"))
	assertLifecycleCommandCode(t, err, e.CodeBuildFailed)
	_ = writer.Close()
	os.Stderr = oldStderr
	diagnostics, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(diagnostics, []byte("private-stdout-sentinel")) || !bytes.Contains(diagnostics, []byte("private-stderr-sentinel")) {
		t.Fatal("producer diagnostics lost stderr route")
	}
	var summary localRunResult
	if err := json.Unmarshal(out.Bytes(), &summary); err != nil {
		t.Fatal(err)
	}
	if len(summary.RequiredOutputs) != 3 || bytes.Contains(out.Bytes(), []byte("private-")) {
		t.Fatalf("unsafe or incomplete summary: %q", out.String())
	}
}

func TestLocalDryRunTUIUsesStaticViewerAndNoColor(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, envColor := range []bool{false, true} {
		t.Run(map[bool]string{false: "flag", true: "environment"}[envColor], func(t *testing.T) {
			if envColor {
				t.Setenv("NO_COLOR", "1")
			}
			var out bytes.Buffer
			called := false
			execute := func(_ context.Context, prepared *lifecycle.PreparedRun, options lifecycle.LocalOptions) (*lifecycle.Run, error) {
				if !options.DryRun {
					t.Fatal("dry-run became execution")
				}
				called = true
				run := prepared.Description()
				return &run, nil
			}
			services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(_ context.Context, opts runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
				if !called || work != nil || opts.Live || !opts.NoColor {
					t.Fatal("static dry-run/color contract violated")
				}
				if source.Snapshot().Status != lifecycle.RunPending {
					t.Fatal("dry-run falsely completed")
				}
				return runpresenter.Result{}
			}}
			flags := []string{"--ui=tui", "--dry-run"}
			if !envColor {
				flags = append(flags, "--no-color")
			}
			if err := localPresentationTestApp(execute, &out, services).RunContext(t.Context(), localPresentationArgs(flags...)); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "lane: static-unqualified") || strings.Contains(out.String(), "record:") {
				t.Fatal("dry-run summary changed")
			}
		})
	}
}

func TestLocalAutoDryRunStaysPlainOnEligibleTerminal(t *testing.T) {
	lifecycleCommandRepo(t)
	var out bytes.Buffer
	execute := func(_ context.Context, prepared *lifecycle.PreparedRun, options lifecycle.LocalOptions) (*lifecycle.Run, error) {
		if !options.DryRun || options.Stdout != &out {
			t.Fatal("auto dry-run did not preserve ordinary plain execution options")
		}
		run := prepared.Description()
		return &run, nil
	}
	services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(context.Context, runpresenter.Options, runSnapshotSource, func(context.Context) error) runpresenter.Result {
		t.Fatal("auto dry-run opened an interactive viewer")
		return runpresenter.Result{}
	}}
	if err := localPresentationTestApp(execute, &out, services).RunContext(t.Context(), localPresentationArgs("--ui=auto", "--dry-run")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "lane: static-unqualified") || strings.ContainsRune(out.String(), '\x1b') {
		t.Fatal("auto dry-run lost its plain pending summary")
	}
}

type failingRunWriter struct {
	err   error
	short bool
}

func (w failingRunWriter) Write(raw []byte) (int, error) {
	if w.short {
		return len(raw) / 2, nil
	}
	return 0, w.err
}

func TestCheckedLocalResultFailurePreservesRetainedOutcome(t *testing.T) {
	lifecycleCommandRepo(t)
	owner := e.New(e.CodeImmutableViolation, "private cleanup failure")
	for _, jsonMode := range []bool{false, true} {
		for _, cleanupFailure := range []bool{false, true} {
			for _, short := range []bool{false, true} {
				t.Run(strings.Join([]string{map[bool]string{false: "plain", true: "json"}[jsonMode], map[bool]string{false: "complete", true: "cleanup"}[cleanupFailure], map[bool]string{false: "write", true: "short"}[short]}, "/"), func(t *testing.T) {
					execute := func(_ context.Context, prepared *lifecycle.PreparedRun, _ lifecycle.LocalOptions) (*lifecycle.Run, error) {
						if cleanupFailure {
							return completedPresentationRun(prepared), owner
						}
						return completedPresentationRun(prepared), nil
					}
					out := failingRunWriter{err: io.ErrClosedPipe, short: short}
					flags := []string{"--ui=plain"}
					if jsonMode {
						flags = append(flags, "--json")
					}
					err := localPresentationTestApp(execute, out, defaultRunPresentationServices()).RunContext(t.Context(), localPresentationArgs(flags...))
					want := e.CodeInternal
					if cleanupFailure {
						want = e.CodeImmutableViolation
						if !errors.Is(err, owner) {
							t.Fatal("owning cleanup cause lost")
						}
					}
					assertLifecycleCommandCode(t, err, want)
					if short && !errors.Is(err, io.ErrShortWrite) || !short && !errors.Is(err, io.ErrClosedPipe) {
						t.Fatalf("write cause lost: %v", err)
					}
				})
			}
		}
	}
}

func TestLocalPresentationFailureDoesNotReplaceBuildCode(t *testing.T) {
	lifecycleCommandRepo(t)
	owner := e.New(e.CodeBuildFailed, "producer failed")
	reporting := e.New(e.CodePreflightFailed, "terminal failed")
	var out bytes.Buffer
	execute := func(_ context.Context, prepared *lifecycle.PreparedRun, _ lifecycle.LocalOptions) (*lifecycle.Run, error) {
		run := prepared.Description()
		run.Status = lifecycle.RunFailed
		return &run, owner
	}
	services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(ctx context.Context, _ runpresenter.Options, _ runSnapshotSource, work func(context.Context) error) runpresenter.Result {
		return runpresenter.Result{WorkErr: work(ctx), PresentationErr: reporting}
	}}
	err := localPresentationTestApp(execute, &out, services).RunContext(t.Context(), localPresentationArgs("--ui=tui"))
	assertLifecycleCommandCode(t, err, e.CodeBuildFailed)
	if !errors.Is(err, owner) || !errors.Is(err, reporting) {
		t.Fatal("owning or presentation cause lost")
	}
}

func TestLocalJSONResultIsDeterministicAndCleanupDistinct(t *testing.T) {
	lifecycleCommandRepo(t)
	prepared, err := lifecycle.PrepareFiles(t.Context(), "docs/examples/nvke-tutorial/definition.yaml", "docs/examples/nvke-tutorial/environment-local.yaml", "test", "summary-test")
	if err != nil {
		t.Fatal(err)
	}
	run := completedPresentationRun(prepared)
	var first, second bytes.Buffer
	reporting := e.New(e.CodeInternal, "private reporting failure")
	for _, out := range []*bytes.Buffer{&first, &second} {
		if err := writeLocalRunJSON(out, prepared, run, reporting, false, false); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("summary is nondeterministic")
	}
	var result localRunResult
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(first.Bytes(), []byte{'\n'}), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != lifecycle.RunComplete || result.CleanupIncomplete || result.ExitCode != 1 || !result.RecordAvailable || len(result.RequiredOutputs) != 3 || len(result.ObservedOutputs) != 3 {
		t.Fatalf("report failure corrupted retained outcome: %+v", result)
	}
}

func TestLocalJSONResultRejectsUnknownOwnerDescriptionsBeforeWriting(t *testing.T) {
	lifecycleCommandRepo(t)
	prepared, err := lifecycle.PrepareFiles(t.Context(), "docs/examples/nvke-tutorial/definition.yaml", "docs/examples/nvke-tutorial/environment-local.yaml", "test", "projection-test")
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []struct {
		name  string
		apply func(*lifecycle.Run)
	}{
		{"unknown lane", func(run *lifecycle.Run) { run.Lane = "future-lane" }},
		{"unavailable qualified lane", func(run *lifecycle.Run) { run.Lane = lifecycle.LaneQualified }},
		{"unknown output", func(run *lifecycle.Run) { run.Observed[0].Name = "not-required" }},
		{"changed kind", func(run *lifecycle.Run) { run.Observed[0].Kind = "future-kind" }},
		{"invalid digest", func(run *lifecycle.Run) { run.Observed[0].Digest = "private-invalid-digest" }},
	} {
		t.Run(change.name, func(t *testing.T) {
			run := completedPresentationRun(prepared)
			change.apply(run)
			var out bytes.Buffer
			err := writeLocalRunJSON(&out, prepared, run, nil, false, false)
			assertLifecycleCommandCode(t, err, e.CodeInternal)
			if out.Len() != 0 {
				t.Fatal("invalid owner description emitted partial JSON")
			}
		})
	}
}

func TestLocalJSONRejectedInputReportsNoInventedAttempt(t *testing.T) {
	var out bytes.Buffer
	called := false
	execute := func(context.Context, *lifecycle.PreparedRun, lifecycle.LocalOptions) (*lifecycle.Run, error) {
		called = true
		return nil, nil
	}
	app := localPresentationTestApp(execute, &out, defaultRunPresentationServices())
	err := app.RunContext(t.Context(), []string{"nvkr", "run", "local", "--definition", "missing", "--environment", "missing", "--attempt", "fresh", "--json"})
	assertLifecycleCommandCode(t, err, e.CodeInvalidCatalog)
	var result localRunResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if called || result.Status != "not-started" || result.RecordAvailable || result.Attempt != "" || result.ExitCode != 16 {
		t.Fatalf("input failure invented execution: %+v", result)
	}
}
