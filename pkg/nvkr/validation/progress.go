// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package validation

import "context"

// Observed check phases, using the existing closed operation-state vocabulary.
// No phase establishes capture completeness, a passing verdict or qualification.
const (
	// PhaseRunning begins a selected check, including its configuration admission.
	PhaseRunning   = "running"
	PhaseCompleted = "completed"
	PhaseFailed    = "failed"
	PhaseCanceled  = "canceled"
)

// Progress describes one observed configured-check phase. Check is the profile
// instance name; raw diagnostics, private paths and credentials never appear.
// It is presentation data and grants no capture, verification or qualification
// authority.
type Progress struct {
	Check string
	Phase string
}

// Reporter receives observed check phases. One validation call invokes it
// serially and synchronously. It must not block or own resources, and a panic
// in it never skips owner work.
type Reporter func(Progress)

type reporterKey struct{}

// WithReporter returns a context carrying an optional progress reporter. Context
// propagation keeps the existing RunFiles arguments, and therefore the SDK
// validation surface, unchanged while nested dispatch reports its own real
// boundaries. A nil reporter returns the original context.
func WithReporter(ctx context.Context, report Reporter) context.Context {
	if report == nil {
		return ctx
	}
	return context.WithValue(ctx, reporterKey{}, report)
}

// ReporterFrom returns the reporter carried by ctx, or nil. The returned value
// remains a presentation callback and confers no authority.
func ReporterFrom(ctx context.Context) Reporter {
	report, _ := ctx.Value(reporterKey{}).(Reporter)
	return report
}

// emit reports one observed phase. Only internally constructed descriptions
// reach the reporter; a presentation panic is contained so owner cleanup and
// error return stand.
func emit(ctx context.Context, check, phase string) {
	report := ReporterFrom(ctx)
	if report == nil {
		return
	}
	defer func() { _ = recover() }()
	report(Progress{Check: check, Phase: phase})
}
