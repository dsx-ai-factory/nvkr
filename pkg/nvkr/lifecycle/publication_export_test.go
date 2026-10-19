// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestVerifiedReleaseExportRequiresOriginAndLiveCapability(t *testing.T) {
	const invocation = "https://github.com/example/nvkr/actions/runs/123/attempts/2"
	plan := newExactInputPlan(t)
	fixture := exactInputFixtureForPlanAndInvocation(t, plan.Definition, plan.Environment, plan, invocation)
	expectation, err := PrepareExactInputs(fixture.definition, fixture.environment, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		invocation string
		closed     bool
		cancel     bool
		receipt    string
		code       e.Code
	}{
		{"exact invocation", invocation, false, false, "issued", ""},
		{"earlier aggregate attempt", "https://github.com/example/nvkr/actions/runs/123/attempts/1", false, false, "issued", e.CodeVerifyFailed},
		{"wrong run", "https://github.com/example/nvkr/actions/runs/124/attempts/2", false, false, "issued", e.CodeVerifyFailed},
		{"closed capability", invocation, true, false, "issued", e.CodeGateUnmet},
		{"canceled", invocation, false, true, "issued", e.CodeTimeout},
		{"missing receipt", invocation, false, false, "nil", e.CodeGateUnmet},
		{"unissued receipt", invocation, false, false, "zero", e.CodeGateUnmet},
		{"other live owner of same publication", invocation, false, false, "other", e.CodeGateUnmet},
		{"different policy", invocation, false, false, "policy", e.CodeVerifyFailed},
		{"different inputs", invocation, false, false, "inputs", e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verified, receipt, err := fixture.consumer(t, nil).VerifyExactInputs(t.Context(), fixture.reference, exactInputPolicy(), expectation)
			if err != nil {
				t.Fatal(err)
			}
			defer verified.Close()
			switch tc.receipt {
			case "nil":
				receipt = nil
			case "zero":
				receipt = &ExactInputReceipt{}
			case "other":
				other, otherReceipt, err := fixture.consumer(t, nil).VerifyExactInputs(t.Context(), fixture.reference, exactInputPolicy(), expectation)
				if err != nil {
					t.Fatal(err)
				}
				defer other.Close()
				receipt = otherReceipt
			case "policy":
				receipt.description.PolicyDigest = "different-policy"
			case "inputs":
				receipt.description.PlanDigest = "different-plan"
			}
			if tc.closed {
				_ = verified.Close()
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			out := filepath.Join(t.TempDir(), "exported")
			err = verified.ExportForInvocation(ctx, receipt, tc.invocation, out)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				if _, err := os.Lstat(out); !os.IsNotExist(err) {
					t.Fatal("failed export exposed a destination", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := delivery.Inspect(t.Context(), out)
			if err != nil || got.RootDigest != fixture.rootDigest {
				t.Fatalf("exported root changed: %+v %v", got, err)
			}
			assertCode(t, verified.ExportForInvocation(ctx, receipt, invocation, out), e.CodeImmutableViolation)
		})
	}
}
