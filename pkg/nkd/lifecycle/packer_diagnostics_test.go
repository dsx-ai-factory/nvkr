// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

func TestExecuteLocalCreatesForwardsAndClosesDiagnosticsBeforeFailureRecord(t *testing.T) {
	t.Chdir(t.TempDir())
	prepared, _ := diskLifecycleFixtureWithProducer(t, "diagnostic-lifecycle", true)
	workspace := filepath.Join(".nkd", "runs", "diagnostic-lifecycle")
	record := filepath.Join(workspace, "lifecycle.json")
	var capability *pipeline.PackerDiagnosticSink
	buildFailure := nkderr.New(nkderr.CodeBuildFailed, "synthetic Packer failure")
	events := make([]string, 0, 3)

	failed, err := executeLocalWithDependencies(t.Context(), prepared,
		LocalOptions{AttemptsRoot: filepath.Join(".nkd", "runs")}, localExecutionDependencies{
			admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
			openDiagnostics: func(ctx context.Context, gotWorkspace string) (*localPackerDiagnostics, error) {
				events = append(events, "open")
				if gotWorkspace != workspace {
					t.Fatalf("diagnostic workspace = %q, want %q", gotWorkspace, workspace)
				}
				if info, statErr := os.Stat(gotWorkspace); statErr != nil || !info.IsDir() {
					t.Fatalf("diagnostic sink opened before attempt reservation: info=%v error=%v", info, statErr)
				}
				diagnostics, openErr := newLocalPackerDiagnosticSink(ctx, gotWorkspace)
				if openErr != nil {
					return nil, openErr
				}
				capability = diagnostics.capability
				closeDiagnostics := diagnostics.close
				diagnostics.close = func() error {
					events = append(events, "close")
					if _, statErr := os.Stat(record); !errors.Is(statErr, os.ErrNotExist) {
						t.Errorf("diagnostic sink closed after lifecycle record: %v", statErr)
					}
					return closeDiagnostics()
				}
				return diagnostics, nil
			},
			runFactory: func(_ context.Context, _ *factory.Plan,
				options factory.LocalRunOptions,
			) (localExecutionResult, error) {
				events = append(events, "run")
				if options.Diagnostics != capability {
					t.Fatalf("factory diagnostic capability = %p, want %p", options.Diagnostics, capability)
				}
				return nil, buildFailure
			},
			writeRecordState: writeRunRecordState,
			beginDiskCommit:  beginDiskAttemptTransaction,
		})
	if !errors.Is(err, buildFailure) || failed == nil || failed.Status != RunFailed {
		t.Fatalf("failed Run = %+v, error=%v", failed, err)
	}
	if want := []string{"open", "run", "close"}; !reflect.DeepEqual(events, want) {
		t.Fatalf("diagnostic lifecycle = %#v, want %#v", events, want)
	}
	if _, statErr := os.Stat(record); statErr != nil {
		t.Fatalf("failed lifecycle record missing after diagnostic close: %v", statErr)
	}
}

func TestExecuteLocalDiagnosticCloseFailureIsRecordedWithoutMaskingBuildFailure(t *testing.T) {
	for _, test := range []struct {
		name             string
		factoryFailure   error
		wantCode         nkderr.Code
		wantResultClosed bool
	}{
		{name: "close failure after factory success", wantCode: nkderr.CodeImmutableViolation, wantResultClosed: true},
		{name: "build failure remains primary", factoryFailure: nkderr.New(nkderr.CodeBuildFailed,
			"synthetic build failure"), wantCode: nkderr.CodeBuildFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			prepared, exporter := diskLifecycleFixtureWithProducer(t, "diagnostic-close-failure", true)
			closeFailure := errors.New("synthetic diagnostic close failure")
			closeCalls := 0
			failed, err := executeLocalWithDependencies(t.Context(), prepared,
				LocalOptions{AttemptsRoot: filepath.Join(".nkd", "runs")}, localExecutionDependencies{
					admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
					openDiagnostics: func(context.Context, string) (*localPackerDiagnostics, error) {
						return &localPackerDiagnostics{
							capability: &pipeline.PackerDiagnosticSink{},
							close: func() error {
								closeCalls++
								return nkderr.Wrap(nkderr.CodeImmutableViolation, closeFailure,
									"closing synthetic Packer diagnostics")
							},
						}, nil
					},
					runFactory: func(context.Context, *factory.Plan,
						factory.LocalRunOptions,
					) (localExecutionResult, error) {
						if test.factoryFailure != nil {
							return nil, test.factoryFailure
						}
						return exporter, nil
					},
					writeRecordState: writeRunRecordState,
					beginDiskCommit:  beginDiskAttemptTransaction,
				})
			if closeCalls != 1 || failed == nil || failed.Status != RunFailed || !errors.Is(err, closeFailure) {
				t.Fatalf("diagnostic close failure: calls=%d run=%+v error=%v", closeCalls, failed, err)
			}
			if test.factoryFailure != nil && !errors.Is(err, test.factoryFailure) {
				t.Fatalf("diagnostic close failure masked build failure: %v", err)
			}
			var coded *nkderr.Error
			if !errors.As(err, &coded) || coded.Code != test.wantCode {
				got := nkderr.Code("")
				if coded != nil {
					got = coded.Code
				}
				t.Fatalf("diagnostic close failure code = %s, want %s", got, test.wantCode)
			}
			if failed.Failure == nil || failed.Failure.Code != string(test.wantCode) {
				t.Fatalf("recorded diagnostic close failure = %+v, want code %s", failed.Failure, test.wantCode)
			}
			if exporter.closed != test.wantResultClosed {
				t.Fatalf("factory result closed = %t, want %t", exporter.closed, test.wantResultClosed)
			}
			if _, statErr := os.Stat(filepath.Join(".nkd", "runs", "diagnostic-close-failure",
				"lifecycle.json")); statErr != nil {
				t.Fatalf("failed lifecycle record missing: %v", statErr)
			}
		})
	}
}

func TestExecuteLocalDiagnosticAdmissionFailures(t *testing.T) {
	for _, test := range []struct {
		name    string
		partial bool
		code    nkderr.Code
	}{
		{"open failed", false, nkderr.CodeImmutableViolation},
		{"partial capability", true, nkderr.CodeInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			prepared, _ := diskLifecycleFixtureWithProducer(t, "diagnostic-open-failure", true)
			factoryCalled, partialClosed := false, false
			failed, err := executeLocalWithDependencies(t.Context(), prepared, LocalOptions{}, localExecutionDependencies{
				admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
				openDiagnostics: func(context.Context, string) (*localPackerDiagnostics, error) {
					if test.partial {
						return &localPackerDiagnostics{close: func() error { partialClosed = true; return nil }}, nil
					}
					return nil, nkderr.New(test.code, "cannot open diagnostics")
				},
				runFactory: func(context.Context, *factory.Plan, factory.LocalRunOptions) (localExecutionResult, error) {
					factoryCalled = true
					return nil, nil
				},
				writeRecordState: writeRunRecordState, beginDiskCommit: beginDiskAttemptTransaction,
			})
			assertCode(t, err, test.code)
			if factoryCalled || failed == nil || failed.Status != RunFailed || failed.Failure.Code != string(test.code) || partialClosed != test.partial {
				t.Fatalf("factory=%t partialClosed=%t failed=%+v", factoryCalled, partialClosed, failed)
			}
			raw, err := os.ReadFile(filepath.Join(".nkd", "runs", prepared.run.Attempt, "lifecycle.json"))
			if err != nil {
				t.Fatal(err)
			}
			var persisted Run
			if err := canonical.UnmarshalExact(raw, &persisted); err != nil {
				t.Fatal(err)
			}
			if persisted.Status != RunFailed || persisted.Failure.Code != string(test.code) {
				t.Fatalf("persisted Run=%+v", persisted)
			}
		})
	}
}

func TestExecuteLocalNonPackerDoesNotOpenPackerDiagnostics(t *testing.T) {
	t.Chdir(t.TempDir())
	prepared, _ := diskLifecycleFixture(t, "not-packer")
	failure := nkderr.New(nkderr.CodeBuildFailed, "stop command build")
	_, err := executeLocalWithDependencies(t.Context(), prepared, LocalOptions{}, localExecutionDependencies{
		admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
		openDiagnostics: func(context.Context, string) (*localPackerDiagnostics, error) {
			t.Fatal("opened Packer diagnostics for command producer")
			return nil, nil
		},
		runFactory: func(_ context.Context, _ *factory.Plan, options factory.LocalRunOptions) (localExecutionResult, error) {
			if options.Diagnostics != nil {
				t.Fatal("command producer received Packer diagnostics")
			}
			return nil, failure
		},
		writeRecordState: writeRunRecordState, beginDiskCommit: beginDiskAttemptTransaction,
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
}
