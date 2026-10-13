// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestLocalProgressReconcilesOwningReturnedRun(t *testing.T) {
	for _, mode := range []string{"success", "dry-run", "factory failure", "observation failure", "canceled", "cleanup failure", "record failure"} {
		t.Run(mode, func(t *testing.T) {
			t.Chdir(t.TempDir())
			prepared, exporter := diskLifecycleFixture(t, "progress")
			monitor, err := NewProgress(prepared)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			deps := localExecutionDependencies{admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
				runFactory: func(_ context.Context, _ *factory.Plan, options factory.LocalRunOptions) (localExecutionResult, error) {
					if mode == "factory failure" {
						return nil, e.New(e.CodeBuildFailed, "private provider text")
					}
					if mode == "canceled" {
						cancel()
						return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "private canceled text")
					}
					if options.DryRun {
						return nil, nil
					}
					if mode == "observation failure" {
						exporter.err = e.New(e.CodeVerifyFailed, "private observation text")
					}
					return exporter, nil
				}, writeRecordState: writeRunRecordState, beginDiskCommit: beginDiskAttemptTransaction}
			if mode == "cleanup failure" {
				deps.beginDiskCommit = func(workspace string) (*diskAttemptTransaction, error) {
					transaction, err := beginDiskAttemptTransaction(workspace)
					if err != nil {
						return nil, err
					}
					transaction.unlink = func(int, string, int) error { return errors.New("private marker failure") }
					return transaction, nil
				}
			}
			if mode == "record failure" {
				deps.writeRecordState = func(context.Context, string, *Run) (bool, error) {
					return false, e.New(e.CodeImmutableViolation, "private record failure")
				}
			}
			run, err := executeLocalWithDependencies(ctx, prepared, LocalOptions{AttemptsRoot: ".nvkr/runs", DryRun: mode == "dry-run", Progress: monitor}, deps)
			got := monitor.Snapshot()
			if got.Attempt != "progress" || len(got.Outputs) != len(prepared.run.Expected) {
				t.Fatal("progress changed frozen requirements")
			}
			if run == nil {
				if got.Status != "unavailable" || got.RecordAvailable {
					t.Fatalf("nil return manufactured Run: %+v", got)
				}
			} else {
				if got.Status != run.Status || got.Lane != run.Lane || got.RecordAvailable != (mode != "dry-run") {
					t.Fatalf("returned Run differs from snapshot: Run=%+v snapshot=%+v", run, got)
				}
				observed := 0
				for _, output := range got.Outputs {
					if output.State == "observed" {
						observed++
					}
				}
				if observed != len(run.Observed) {
					t.Fatalf("partial telemetry survived final result: observed=%d want=%d", observed, len(run.Observed))
				}
			}
			if (err != nil) != (got.FailureCode != "") {
				t.Fatalf("error code lost: %v snapshot=%+v", err, got)
			}
			if got.CleanupIncomplete != (mode == "cleanup failure") {
				t.Fatalf("cleanup status lost: %+v", got)
			}
			if mode == "dry-run" {
				if got.Status != RunPending || got.Mode != "dry-run" {
					t.Fatal("dry-run became completed execution")
				}
				for _, operation := range got.Operations {
					if operation.State == "running" || operation.State == "completed" {
						t.Fatal("dry-run claimed live work")
					}
				}
			}
			for _, activity := range got.Activity {
				if activity.Text == "private provider text" {
					t.Fatal("raw provider text leaked")
				}
			}
		})
	}
}

func TestLocalProgressUnknownBindingDoesNotAffectExecution(t *testing.T) {
	t.Chdir(t.TempDir())
	prepared, exporter := diskLifecycleFixture(t, "actual")
	monitor, err := factory.NewProgress(prepared.plan, "other")
	if err != nil {
		t.Fatal(err)
	}
	run, err := executeLocalWithDependencies(t.Context(), prepared, LocalOptions{AttemptsRoot: ".nvkr/runs", Progress: monitor}, localExecutionDependencies{
		admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil }, runFactory: func(context.Context, *factory.Plan, factory.LocalRunOptions) (localExecutionResult, error) {
			return exporter, nil
		},
		writeRecordState: writeRunRecordState, beginDiskCommit: beginDiskAttemptTransaction,
	})
	if err != nil || run.Status != RunComplete {
		t.Fatalf("optional telemetry changed admission: %v", err)
	}
	got := monitor.Snapshot()
	if !got.StartedAt.IsZero() || len(got.Activity) != 0 || got.Status != "pending" {
		t.Fatal("unknown binding accepted telemetry")
	}
	if _, err := NewProgress(nil); err == nil {
		t.Fatal("nil prepared accepted")
	}
}
