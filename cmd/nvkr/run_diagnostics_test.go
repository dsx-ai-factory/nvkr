// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/rundiagnostics"
	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

type restoredDiagnosticWriter struct {
	t        *testing.T
	restored *bool
	joined   *bool
	bytes.Buffer
}

func (w *restoredDiagnosticWriter) Write(raw []byte) (int, error) {
	w.t.Helper()
	if !*w.restored || !*w.joined {
		w.t.Fatal("raw diagnostics were written before restoration and joining")
	}
	return w.Buffer.Write(raw)
}

func TestLocalAutoTUIDiagnosticsReplayAfterRestorationAndJoining(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, test := range []struct {
		name, status string
		ownerCode    e.Code
		rendererCode e.Code
		cancel       bool
	}{
		{"producer failure", lifecycle.RunFailed, e.CodeBuildFailed, "", false},
		{"canceled", lifecycle.RunFailed, e.CodeTimeout, "", true},
		{"retained cleanup error", lifecycle.RunComplete, e.CodeImmutableViolation, "", false},
		{"renderer failure", lifecycle.RunComplete, "", e.CodePreflightFailed, false},
		{"success", lifecycle.RunComplete, "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out bytes.Buffer
			restored, joined := false, false
			diagnostics := &restoredDiagnosticWriter{t: t, restored: &restored, joined: &joined}
			var returned *lifecycle.Run
			execute := func(ctx context.Context, prepared *lifecycle.PreparedRun, options lifecycle.LocalOptions) (*lifecycle.Run, error) {
				if _, ok := options.Stdout.(*rundiagnostics.Buffer); !ok || options.Stdout != options.Stderr {
					t.Fatal("TUI did not select one bounded private collector")
				}
				_, _ = options.Stdout.Write([]byte("private-stdout-sentinel\n"))
				_, _ = options.Stderr.Write([]byte("private-stderr-sentinel\n"))
				if test.cancel {
					<-ctx.Done()
				}
				returned = completedPresentationRun(prepared)
				returned.Status = test.status
				if test.status != lifecycle.RunComplete {
					returned.Observed = nil
				}
				joined = true
				if test.ownerCode != "" {
					return returned, e.New(test.ownerCode, "producer failed")
				}
				return returned, nil
			}
			services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(ctx context.Context, _ runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
				workCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				var ownerErr error
				if test.cancel {
					done := make(chan error, 1)
					go func() { done <- work(workCtx) }()
					cancel()
					ownerErr = <-done
				} else {
					ownerErr = work(workCtx)
				}
				snapshot := source.Snapshot()
				if snapshot.Status != test.status || test.status == lifecycle.RunComplete && len(snapshot.Outputs) != len(returned.Observed) || snapshot.CleanupIncomplete != (test.status == lifecycle.RunComplete && test.ownerCode != "") {
					t.Fatal("diagnostics changed final Run membership or cleanup description")
				}
				if test.status == lifecycle.RunComplete {
					for _, output := range snapshot.Outputs {
						if output.State != "observed" {
							t.Fatal("complete retained Run lost an observed output")
						}
					}
				}
				raw, err := json.Marshal(snapshot)
				if err != nil || bytes.Contains(raw, []byte("private-stdout-sentinel")) || bytes.Contains(raw, []byte("private-stderr-sentinel")) || diagnostics.Len() != 0 {
					t.Fatal("private diagnostics reached the active view", err)
				}
				restored = true
				var rendererErr error
				if test.rendererCode != "" {
					rendererErr = e.New(test.rendererCode, "renderer failed")
				}
				return runpresenter.Result{WorkErr: ownerErr, PresentationErr: rendererErr}
			}}
			app := localPresentationTestApp(execute, &out, services)
			app.ErrWriter = diagnostics
			err := app.RunContext(t.Context(), localPresentationArgs())
			wantCode := test.ownerCode
			if wantCode == "" {
				wantCode = test.rendererCode
			}
			if wantCode == "" {
				if err != nil || diagnostics.Len() != 0 {
					t.Fatal("successful capture was replayed or changed the result", err)
				}
			} else {
				assertLifecycleCommandCode(t, err, wantCode)
				if !strings.Contains(diagnostics.String(), "private-stdout-sentinel") || !strings.Contains(diagnostics.String(), "private-stderr-sentinel") {
					t.Fatal("failed Run lost generic producer diagnostics")
				}
			}
			if strings.Contains(out.String(), "private-") || returned.Status != test.status {
				t.Fatal("diagnostic reporting changed safe stdout or retained Run status")
			}
		})
	}
}

func TestLocalTUIDiagnosticsTruncationKeepsFailureTail(t *testing.T) {
	lifecycleCommandRepo(t)
	var out, diagnostic bytes.Buffer
	execute := func(_ context.Context, _ *lifecycle.PreparedRun, options lifecycle.LocalOptions) (*lifecycle.Run, error) {
		_, _ = options.Stdout.Write(bytes.Repeat([]byte("x"), 3*rundiagnostics.MaxBytes))
		_, _ = options.Stderr.Write([]byte("last failure sentinel"))
		return nil, e.New(e.CodeBuildFailed, "producer failed")
	}
	services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(ctx context.Context, _ runpresenter.Options, _ runSnapshotSource, work func(context.Context) error) runpresenter.Result {
		return runpresenter.Result{WorkErr: work(ctx)}
	}}
	app := localPresentationTestApp(execute, &out, services)
	app.ErrWriter = &diagnostic
	err := app.RunContext(t.Context(), localPresentationArgs("--ui=tui"))
	assertLifecycleCommandCode(t, err, e.CodeBuildFailed)
	if !strings.Contains(diagnostic.String(), "Earlier diagnostics truncated") || !strings.HasSuffix(diagnostic.String(), "last failure sentinel\n") || diagnostic.Len() > rundiagnostics.MaxBytes+200 {
		t.Fatal("failure tail or truncation report was lost/unbounded")
	}
}

func TestLocalTUIDiagnosticReplayFailuresPreserveOwningOutcome(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, retained := range []bool{false, true} {
		for _, short := range []bool{false, true} {
			t.Run(strings.Join([]string{map[bool]string{false: "failed", true: "retained"}[retained], map[bool]string{false: "broken writer", true: "short writer"}[short]}, "/"), func(t *testing.T) {
				ownerCode := e.CodeBuildFailed
				if retained {
					ownerCode = e.CodeImmutableViolation
				}
				owner := e.New(ownerCode, "owner failed")
				var result *lifecycle.Run
				execute := func(_ context.Context, prepared *lifecycle.PreparedRun, options lifecycle.LocalOptions) (*lifecycle.Run, error) {
					_, _ = options.Stderr.Write([]byte("failure details"))
					if retained {
						result = completedPresentationRun(prepared)
					}
					return result, owner
				}
				services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(ctx context.Context, _ runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
					ownerErr := work(ctx)
					if retained {
						snapshot := source.Snapshot()
						if snapshot.Status != lifecycle.RunComplete || !snapshot.CleanupIncomplete {
							t.Fatal("cleanup failure corrupted the retained Run description")
						}
						for _, output := range snapshot.Outputs {
							if output.State != "observed" {
								t.Fatal("cleanup failure discarded observed output membership")
							}
						}
					}
					return runpresenter.Result{WorkErr: ownerErr}
				}}
				var out bytes.Buffer
				app := localPresentationTestApp(execute, &out, services)
				app.ErrWriter = failingRunWriter{err: io.ErrClosedPipe, short: short}
				err := app.RunContext(t.Context(), localPresentationArgs("--ui=tui"))
				assertLifecycleCommandCode(t, err, ownerCode)
				cause := io.ErrClosedPipe
				if short {
					cause = io.ErrShortWrite
				}
				if !errors.Is(err, owner) || !errors.Is(err, cause) || retained && (result.Status != lifecycle.RunComplete || !strings.Contains(out.String(), "status: complete")) {
					t.Fatal("replay lost the owner/cause or corrupted a retained result", err)
				}
			})
		}
	}
}
