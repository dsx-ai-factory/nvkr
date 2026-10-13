// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Description delegates to the fixture's normal initial admission. The gate's
// final Require returns success just as cancellation arrives at that boundary.
type finalQualificationCancelSource struct {
	*qualificationFixtureSource
	cancel context.CancelFunc
}

func (source *finalQualificationCancelSource) Require() error {
	err := source.qualificationFixtureSource.Require()
	source.cancel()
	return err
}

func TestQualificationRefusesCancellationAfterFinalSuccessfulCheck(t *testing.T) {
	request, consumer, verifier := newQualificationFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	source := &finalQualificationCancelSource{qualificationFixtureSource: verifier.source, cancel: cancel}
	originalMaterialize := consumer.materialize
	var held *delivery.Publication
	consumer.materialize = func(ctx context.Context, reference string) (*delivery.Publication, error) {
		publication, err := originalMaterialize(ctx, reference)
		held = publication
		return publication, err
	}
	receipt, report, err := consumer.checkQualification(ctx, request, source)
	assertCode(t, err, e.CodeTimeout)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation cause: %v", err)
	}
	if receipt != nil || report == nil || len(report.Outputs) != 2 || len(verifier.verified) != 4 || verifier.evidence != 2 {
		t.Fatalf("late cancellation skipped checks or issued authority: receipt=%v report=%+v signatures=%v evidence=%d", receipt, report, verifier.verified, verifier.evidence)
	}
	if held == nil {
		t.Fatal("fixture did not materialize publication")
	}
	if _, err := held.Description(request.Reference); err == nil {
		t.Fatal("canceled issuance retained verified publication bytes")
	}
	for _, output := range report.Outputs {
		if output.Overall != "fail" || len(output.Checks) != 7 {
			t.Fatalf("cancellation missing from output verdict: %+v", output)
		}
		previous, lifetime := "", false
		for _, check := range output.Checks {
			if check.Name <= previous {
				t.Fatal("checks are not sorted and unique")
			}
			previous = check.Name
			if check.Name == "operation-lifetime" {
				lifetime = true
				if check.Status != "fail" || check.Code != string(e.CodeTimeout) || check.Message != err.Error() {
					t.Fatalf("cancellation diagnostic changed: %+v", check)
				}
			} else if check.Status != "pass" && !(check.Name == "embedded-declarations" && check.Status == "not-applicable") {
				t.Fatalf("cancellation changed a completed artifact check: %+v", check)
			}
		}
		if !lifetime {
			t.Fatal("cancellation refusal was omitted")
		}
	}
}
