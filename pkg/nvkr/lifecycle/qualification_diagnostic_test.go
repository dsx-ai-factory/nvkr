// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func diagnosticReceiptFixture(t *testing.T) *ArtifactCheckReceipt {
	t.Helper()
	request, consumer, verifier := newQualificationFixture(t)
	receipt, _, err := consumer.checkQualification(t.Context(), request, verifier.source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receipt.Close() })
	return receipt
}
func TestArtifactDiagnosticAdmission(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name        string
		receipt     func(*testing.T) *ArtifactCheckReceipt
		ctx         context.Context
		nilCallback bool
		code        e.Code
	}{
		{"nil receipt", func(*testing.T) *ArtifactCheckReceipt { return nil }, t.Context(), false, e.CodeGateUnmet},
		{"zero receipt", func(*testing.T) *ArtifactCheckReceipt { return &ArtifactCheckReceipt{} }, t.Context(), false, e.CodeGateUnmet},
		{"closed receipt", func(t *testing.T) *ArtifactCheckReceipt {
			r := diagnosticReceiptFixture(t)
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			return r
		}, t.Context(), false, e.CodeGateUnmet},
		{"nil callback", diagnosticReceiptFixture, t.Context(), true, e.CodeUsage},
		{"nil context", diagnosticReceiptFixture, nil, false, e.CodeUsage},
		{"canceled context", diagnosticReceiptFixture, canceled, false, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			callback := func(context.Context, ArtifactCheckDescription) error { calls++; return nil }
			if tc.nilCallback {
				callback = nil
			}
			err := tc.receipt(t).WithDiagnosticOperation(tc.ctx, callback)
			assertCode(t, err, tc.code)
			if calls != 0 {
				t.Fatal("invalid admission reached observation")
			}
			if tc.ctx == canceled && !errors.Is(err, context.Canceled) {
				t.Fatal("cancellation cause lost")
			}
		})
	}
}
func TestArtifactDiagnosticDescriptionAndCallbackError(t *testing.T) {
	receipt := diagnosticReceiptFixture(t)
	original, err := receipt.Description()
	if err != nil {
		t.Fatal(err)
	}
	sentinel := e.New(e.CodeUnavailable, "independent observer failure")
	calls := 0
	err = receipt.WithDiagnosticOperation(t.Context(), func(_ context.Context, description ArtifactCheckDescription) error {
		calls++
		if description.Inputs != original.Inputs || len(description.Outputs) != len(original.Outputs) {
			t.Fatal("diagnostic lost receipt bindings")
		}
		description.Outputs[0].Subject = "caller mutation"
		return sentinel
	})
	if err != sentinel || calls != 1 {
		t.Fatalf("callback failure changed: %v calls%d", err, calls)
	}
	after, err := receipt.Description()
	if err != nil {
		t.Fatal(err)
	}
	if after.Outputs[0].Subject != original.Outputs[0].Subject {
		t.Fatal("callback mutated receipt")
	}
	if err := receipt.WithDiagnosticOperation(t.Context(), func(context.Context, ArtifactCheckDescription) error { return nil }); err != nil {
		t.Fatal(err)
	}
}
func TestArtifactDiagnosticCloseCancelsAndJoins(t *testing.T) {
	receipt := diagnosticReceiptFixture(t)
	copied := *receipt
	entered, canceled, cleanupRelease := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseCleanup := func() { releaseOnce.Do(func() { close(cleanupRelease) }) }
	defer releaseCleanup()
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- receipt.WithDiagnosticOperation(t.Context(), func(ctx context.Context, _ ArtifactCheckDescription) error {
			close(entered)
			<-ctx.Done()
			close(canceled)
			// Models independently bounded cleanup: Close must not release receipt
			// resources until this owned callback has finished its join.
			<-cleanupRelease
			return nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("callback never entered")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- copied.Close() }()
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		releaseCleanup()
		t.Fatal("Close failed to cancel operation")
	}
	select {
	case err := <-closeDone:
		releaseCleanup()
		t.Fatalf("Close completed before cleanup join: %v", err)
	default:
	}
	_, err := receipt.Description()
	assertCode(t, err, e.CodeGateUnmet)
	releaseCleanup()
	select {
	case err := <-operationDone:
		assertCode(t, err, e.CodeTimeout)
		if !errors.Is(err, context.Canceled) {
			t.Fatal("Close cancellation cause lost")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("callback failed to join")
	}
	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close failed to join")
	}
	calls := 0
	for _, r := range []*ArtifactCheckReceipt{receipt, &copied} {
		assertCode(t, r.WithDiagnosticOperation(t.Context(), func(context.Context, ArtifactCheckDescription) error { calls++; return nil }), e.CodeGateUnmet)
	}
	if calls != 0 {
		t.Fatal("revoked shallow receipt reached observation")
	}
}
func TestArtifactDiagnosticRequestCancellationCannotReturnSuccess(t *testing.T) {
	receipt := diagnosticReceiptFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := receipt.WithDiagnosticOperation(ctx, func(context.Context, ArtifactCheckDescription) error { cancel(); return nil })
	assertCode(t, err, e.CodeTimeout)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("callback cancellation cause lost")
	}
}

func TestArtifactDiagnosticSourceRevocationIsRecheckedWithoutAsyncCancellation(t *testing.T) {
	for _, during := range []bool{false, true} {
		name := "before registration"
		if during {
			name = "during callback"
		}
		t.Run(name, func(t *testing.T) {
			receipt := diagnosticReceiptFixture(t)
			source, ok := receipt.origin.(*qualificationFixtureSource)
			if !ok {
				t.Fatal("unexpected existing fixture source")
			}
			if !during {
				source.closed = true
			}
			calls := 0
			err := receipt.WithDiagnosticOperation(t.Context(), func(ctx context.Context, _ ArtifactCheckDescription) error {
				calls++
				source.closed = true
				if ctx.Err() != nil {
					t.Fatal("separate source revocation unexpectedly canceled operation context")
				}
				return nil
			})
			assertCode(t, err, e.CodeProvenanceMissing)
			expected := 0
			if during {
				expected = 1
			}
			if calls != expected {
				t.Fatalf("source admission/completion differed: calls%d want%d", calls, expected)
			}
		})
	}
}

func TestArtifactDiagnosticCodesUnclassifiedCallbackFailure(t *testing.T) {
	receipt := diagnosticReceiptFixture(t)
	cause := errors.New("independent uncoded observer failure")
	err := receipt.WithDiagnosticOperation(t.Context(), func(context.Context, ArtifactCheckDescription) error { return cause })
	assertCode(t, err, e.CodeVerifyFailed)
	if !errors.Is(err, cause) {
		t.Fatal("uncoded callback cause lost")
	}
}
