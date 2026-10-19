// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestTriggeredRunFreezesEvidenceAndRefusesRepeatedPublication(t *testing.T) {
	run := newTriggeredRun(t.Context())
	defer func() { _ = run.Close() }()
	evidence := evidenceFixture()
	exact, err := evidence.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	run.state.publish = func(context.Context) (*Publication, error) {
		calls++
		return &Publication{Evidence: &evidence, Successful: true}, nil
	}
	result, err := run.Publish(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	result.Evidence.Smoke[0].Status = "fail"
	result.Successful = false
	if run.state.evidence != string(exact) {
		t.Fatal("mutable diagnostics changed the retained predicate")
	}
	_, err = run.Publish(t.Context())
	requireCode(t, err, e.CodeGateUnmet)
	if calls != 1 {
		t.Fatalf("publication repeated: %d calls", calls)
	}
	// Frozen descriptions still cannot mint or replace live artifact authority.
	_, _, err = run.PublishedEvidence()
	requireCode(t, err, e.CodeGateUnmet)
}

func TestTriggeredRunFailuresNeverExposePublishedInputs(t *testing.T) {
	injected := e.New(e.CodeGateUnmet, "injected attachment failure")
	for _, tc := range []struct {
		name   string
		result *Publication
		err    error
		code   e.Code
	}{
		{"partial attachments", &Publication{PublishedSubjects: []string{"registry.example.test/first"}, Successful: true}, injected, e.CodeGateUnmet},
		{"missing result", nil, nil, e.CodeGateUnmet},
		{"missing evidence", &Publication{Successful: true}, nil, e.CodeGateUnmet},
		{"invalid evidence", &Publication{Evidence: &Evidence{}, Successful: true}, nil, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := newTriggeredRun(t.Context())
			defer func() { _ = run.Close() }()
			run.state.publish = func(context.Context) (*Publication, error) { return tc.result, tc.err }
			result, err := run.Publish(t.Context())
			requireCode(t, err, tc.code)
			if result != tc.result || (tc.err != nil && !errors.Is(err, tc.err)) {
				t.Fatal("original failure diagnostics or cause were lost")
			}
			_, _, err = run.PublishedEvidence()
			requireCode(t, err, e.CodeGateUnmet)
		})
	}
}

func TestTriggeredRunCloseCancelsJoinsAndReleasesDependencies(t *testing.T) {
	run := newTriggeredRun(t.Context())
	copy := *run
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	run.state.publish = func(ctx context.Context) (*Publication, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "publication canceled")
	}
	var closed []int
	for i := 0; i < 4; i++ {
		run.state.closers = append(run.state.closers, func() error { closed = append(closed, i); return nil })
	}
	publishDone := make(chan error, 1)
	go func() { _, err := run.Publish(t.Context()); publishDone <- err }()
	awaitTriggeredSignal(t, entered)
	closeDone := make(chan error, 1)
	go func() { closeDone <- copy.Close() }()
	awaitTriggeredSignal(t, canceled)
	select {
	case <-closeDone:
		t.Fatal("Close returned before its publication joined")
	default:
	}
	close(release)
	if err := <-publishDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(closed, []int{3, 2, 1, 0}) {
		t.Fatalf("dependency close order: %v", closed)
	}
	if err := run.Close(); err != nil || len(closed) != 4 {
		t.Fatalf("repeated Close changed its outcome: %v %v", err, closed)
	}
	_, err := copy.Publish(t.Context())
	requireCode(t, err, e.CodeGateUnmet)
	_, _, err = run.PublishedEvidence()
	requireCode(t, err, e.CodeGateUnmet)
}

func TestTriggeredRunCloseRetainsCleanupFailure(t *testing.T) {
	run := newTriggeredRun(t.Context())
	injected := errors.New("injected cleanup failure")
	calls := 0
	run.state.closers = []func() error{
		func() error { calls++; return injected },
		func() error { calls++; return nil },
	}
	for range 2 {
		if err := run.Close(); !errors.Is(err, injected) {
			t.Fatalf("cleanup failure lost: %v", err)
		}
	}
	if calls != 2 {
		t.Fatalf("cleanup repeated: %d", calls)
	}
}

func TestTriggeredRunCanceledBeforePublicationDoesNoWork(t *testing.T) {
	for _, tc := range []struct {
		name string
		own  bool
	}{
		{"operation context", false}, {"retained context", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			parent, operation := t.Context(), ctx
			if tc.own {
				parent, operation = ctx, t.Context()
			}
			run := newTriggeredRun(parent)
			defer func() { _ = run.Close() }()
			run.state.publish = func(context.Context) (*Publication, error) { t.Fatal("canceled operation did work"); return nil, nil }
			cancel()
			_, err := run.Publish(operation)
			requireCode(t, err, e.CodeTimeout)
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}

func awaitTriggeredSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for qualification operation")
	}
}

func TestTriggeredRunRetainsHostedBudgetUntilClose(t *testing.T) {
	for _, tc := range []struct {
		name          string
		parentTimeout time.Duration
		wantTimeout   time.Duration
	}{
		{"hosted deadline", 0, 19 * time.Minute},
		{"shorter caller", 5 * time.Minute, 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent := t.Context()
				if tc.parentTimeout != 0 {
					var cancel context.CancelFunc
					parent, cancel = context.WithTimeout(parent, tc.parentTimeout)
					defer cancel()
				}
				start := time.Now()
				run := newTriggeredRun(parent)
				defer func() { _ = run.Close() }()
				if err := run.state.ctx.Err(); err != nil {
					t.Fatalf("returned operation is already canceled: %v", err)
				}
				deadline, ok := run.state.ctx.Deadline()
				if !ok || !deadline.Equal(start.Add(tc.wantTimeout)) {
					t.Fatalf("retained deadline = %v, present = %t", deadline, ok)
				}
				run.state.publish = func(ctx context.Context) (*Publication, error) {
					<-ctx.Done()
					return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "injected stalled publication")
				}
				_, err := run.Publish(t.Context())
				requireCode(t, err, e.CodeTimeout)
				if !errors.Is(err, context.Canceled) || !time.Now().Equal(deadline) {
					t.Fatalf("retained budget not enforced: %v at %v", err, time.Now())
				}
			})
		})
	}
}
