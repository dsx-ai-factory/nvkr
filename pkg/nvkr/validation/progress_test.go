// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import (
	"context"
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

func progressChecks(names ...string) []Check {
	checks := make([]Check, 0, len(names))
	for _, name := range names {
		check := profileFixture().Checks[0]
		check.Name = name
		checks = append(checks, check)
	}
	return checks
}

func TestRunSelectedReportsObservedCheckPhases(t *testing.T) {
	source := profileFixture().Source
	const privateFailure = "upstream check failed with private diagnostic detail"
	for _, tc := range []struct {
		name     string
		checks   []Check
		cancel   bool
		run      func(context.Context) (testresults.Output, error)
		want     []Progress
		wantCode e.Code
		wantText string
	}{
		{
			name:   "completed",
			checks: progressChecks("cluster-conformance"),
			run: func(context.Context) (testresults.Output, error) {
				return testresults.Output{SchemaVersion: testresults.SchemaVersion, CaptureStatus: testresults.CaptureComplete}, nil
			},
			want: []Progress{{Check: "cluster-conformance", Phase: PhaseRunning}, {Check: "cluster-conformance", Phase: PhaseCompleted}},
		},
		{
			name:   "failure reports phase without private diagnostics",
			checks: progressChecks("cluster-conformance"),
			run: func(context.Context) (testresults.Output, error) {
				return testresults.Output{}, e.New(e.CodeVerifyFailed, privateFailure)
			},
			want:     []Progress{{Check: "cluster-conformance", Phase: PhaseRunning}, {Check: "cluster-conformance", Phase: PhaseFailed}},
			wantCode: e.CodeVerifyFailed,
			wantText: privateFailure,
		},
		{
			name:   "canceled context dominates a returned error",
			checks: progressChecks("cluster-conformance"),
			cancel: true,
			run: func(context.Context) (testresults.Output, error) {
				return testresults.Output{}, e.New(e.CodeVerifyFailed, "upstream check failed")
			},
			want:     []Progress{{Check: "cluster-conformance", Phase: PhaseRunning}, {Check: "cluster-conformance", Phase: PhaseCanceled}},
			wantCode: e.CodeVerifyFailed,
			wantText: "upstream check failed",
		},
		{
			name:   "internal deadline reports failed with a live caller",
			checks: progressChecks("cluster-conformance"),
			run: func(context.Context) (testresults.Output, error) {
				return testresults.Output{}, e.Wrap(e.CodeTimeout, context.DeadlineExceeded, "runner deadline")
			},
			want:     []Progress{{Check: "cluster-conformance", Phase: PhaseRunning}, {Check: "cluster-conformance", Phase: PhaseFailed}},
			wantCode: e.CodeTimeout,
			wantText: "runner deadline",
		},
		{
			name:   "partial capture still reports failed once",
			checks: progressChecks("cluster-conformance"),
			run: func(context.Context) (testresults.Output, error) {
				return testresults.Output{SchemaVersion: testresults.SchemaVersion, CaptureStatus: testresults.CaptureComplete}, e.New(e.CodeVerifyFailed, "upstream check failed")
			},
			want:     []Progress{{Check: "cluster-conformance", Phase: PhaseRunning}, {Check: "cluster-conformance", Phase: PhaseFailed}},
			wantCode: e.CodeVerifyFailed,
			wantText: "upstream check failed",
		},
		{
			name:   "later checks after a failure are never reported",
			checks: progressChecks("first", "second", "third"),
			run: func() func(context.Context) (testresults.Output, error) {
				calls := 0
				return func(context.Context) (testresults.Output, error) {
					calls++
					if calls == 2 {
						return testresults.Output{}, e.New(e.CodeUsage, "second producer configuration refused")
					}
					return testresults.Output{SchemaVersion: testresults.SchemaVersion, CaptureStatus: testresults.CaptureComplete}, nil
				}
			}(),
			want: []Progress{
				{Check: "first", Phase: PhaseRunning}, {Check: "first", Phase: PhaseCompleted},
				{Check: "second", Phase: PhaseRunning}, {Check: "second", Phase: PhaseFailed},
			},
			wantCode: e.CodeUsage,
			wantText: "second producer configuration refused",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var observed []Progress
			ctx = WithReporter(ctx, func(update Progress) { observed = append(observed, update) })
			outputs, err := runSelected(ctx, source, tc.checks, lifecycle.QualificationRuntimeOptions{}, func(ctx context.Context, _, _, _ string, _ lifecycle.QualificationRuntimeOptions) (testresults.Output, error) {
				if tc.cancel {
					cancel()
				}
				return tc.run(ctx)
			})
			if tc.wantCode != "" {
				requireError(t, err, tc.wantCode, tc.wantText)
			} else if err != nil {
				t.Fatal(err)
			}
			if len(observed) != len(tc.want) {
				t.Fatalf("phases=%+v want=%+v", observed, tc.want)
			}
			for index, update := range observed {
				if update != tc.want[index] {
					t.Fatalf("phase %d=%+v want=%+v", index, update, tc.want[index])
				}
			}
			if tc.wantCode == "" && len(outputs) != len(tc.checks) {
				t.Fatalf("outputs=%d want=%d", len(outputs), len(tc.checks))
			}
		})
	}
}

func TestRunSelectedProgressWithoutReporterAndPanic(t *testing.T) {
	source := profileFixture().Source
	checks := progressChecks("cluster-conformance")
	success := testresults.Output{SchemaVersion: testresults.SchemaVersion, CaptureStatus: testresults.CaptureComplete}
	outputs, err := runSelected(t.Context(), source, checks, lifecycle.QualificationRuntimeOptions{}, func(context.Context, string, string, string, lifecycle.QualificationRuntimeOptions) (testresults.Output, error) {
		return success, nil
	})
	if err != nil || len(outputs) != 1 {
		t.Fatalf("nil reporter changed dispatch: %v %+v", err, outputs)
	}
	recovered := errors.New("presentation panic")
	outputs, err = runSelected(WithReporter(t.Context(), func(Progress) { panic(recovered) }), source, checks, lifecycle.QualificationRuntimeOptions{}, func(context.Context, string, string, string, lifecycle.QualificationRuntimeOptions) (testresults.Output, error) {
		return success, nil
	})
	if err != nil || len(outputs) != 1 || outputs[0].CaptureStatus != testresults.CaptureComplete {
		t.Fatalf("reporter panic changed dispatch: %v %+v", err, outputs)
	}
}

func TestWithReporterNilAndMissing(t *testing.T) {
	ctx := t.Context()
	if WithReporter(ctx, nil) != ctx {
		t.Fatal("nil reporter replaced the context")
	}
	if ReporterFrom(ctx) != nil {
		t.Fatal("plain context carried a reporter")
	}
}
