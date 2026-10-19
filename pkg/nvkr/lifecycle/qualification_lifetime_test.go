// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestArtifactCheckCloseCancelsAndJoinsBeforePayloadRelease(t *testing.T) {
	request, consumer, verifier := newQualificationFixture(t)
	receipt, _, err := consumer.checkQualification(t.Context(), request, verifier.source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receipt.Close() }()
	if _, err := receipt.lifetime.verified.Description(); err != nil {
		t.Fatalf("publication released before consumer finished: %v", err)
	}
	want, err := receipt.lifetime.plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	request.Plan.Definition.Metadata.Name = "changed-by-caller"
	got, err := receipt.lifetime.plan.Digest()
	if err != nil || got != want {
		t.Fatalf("retained Plan changed with caller: %s != %s (%v)", got, want, err)
	}
	operation, finish, err := receipt.beginOperation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	copyReceipt := *receipt
	closed := make(chan error, 1)
	go func() { closed <- copyReceipt.Close() }()
	select {
	case <-operation.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not cancel owned work")
	}
	select {
	case err := <-closed:
		t.Fatalf("Close returned before owned work joined: %v", err)
	default:
	}
	if _, err := receipt.lifetime.verified.Description(); err != nil {
		t.Fatalf("payload released before join: %v", err)
	}
	if _, _, err := receipt.beginOperation(t.Context()); err == nil {
		t.Fatal("closing receipt admitted new work")
	}
	finish()
	finish() // Completion is idempotent; shallow owners cannot underflow the join.
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not finish after owned work joined")
	}
	if _, err := receipt.lifetime.verified.Description(); err == nil {
		t.Fatal("verified payload remained live after Close")
	}
	_, err = receipt.Description()
	assertCode(t, err, e.CodeGateUnmet)
}

func TestArtifactCheckOperationAdmissionAndCloseErrors(t *testing.T) {
	request, consumer, verifier := newQualificationFixture(t)
	receipt, _, err := consumer.checkQualification(t.Context(), request, verifier.source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = receipt.Close() }()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code e.Code
	}{
		{"nil", nil, e.CodeUsage}, {"canceled", canceled, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, finish, err := receipt.beginOperation(tc.ctx)
			assertCode(t, err, tc.code)
			if finish != nil {
				t.Fatal("refused operation returned ownership")
			}
		})
	}
	verifier.source.closed = true
	_, _, err = receipt.beginOperation(t.Context())
	assertCode(t, err, e.CodeProvenanceMissing)
	cause := errors.New("source cleanup failed")
	closed := 0
	receipt.lifetime.closeSource = func() error { closed++; return cause }
	copyReceipt := *receipt
	for _, r := range []*ArtifactCheckReceipt{receipt, &copyReceipt} {
		if err := r.Close(); !errors.Is(err, cause) {
			t.Fatalf("lost cleanup failure: %v", err)
		}
	}
	if closed != 1 {
		t.Fatalf("source closed %d times", closed)
	}
}
