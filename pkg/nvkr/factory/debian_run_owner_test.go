// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// No producer runs in these tests. They prove custody, exact original Plan and
// named-output binding and cleanup, not archive/content/evidence acceptance.
func debianRunOwnerFixture(t *testing.T) (*Plan, *LocalRunResult, *pipeline.NodeSession) {
	t.Helper()
	d, env, stack := debianFactoryInputs(t)
	plan, err := Compile(t.Context(), d, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	result := &LocalRunResult{SchemaVersion: LocalRunSchemaVersion, PlanDigest: digest, Qualification: TierLocalExecution}
	session, err := pipeline.NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	t.Cleanup(func() { _ = result.Close() })
	if err := bindDebianRunSession(t.Context(), plan, result, session); err != nil {
		t.Fatal(err)
	}
	return plan, result, session
}

func TestDebianRunCustodyRequiresOriginalRunPlanAndOutput(t *testing.T) {
	plan, result, _ := debianRunOwnerFixture(t)
	output := plan.LifecycleOutputs[0]
	raw, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded LocalRunResult
	if err := canonical.UnmarshalExact(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	changed := cloneDebianTestPlan(t, plan)
	changed.GeneratorVersion = "changed"
	sibling := output
	sibling.Name = "sibling"
	for _, tc := range []struct {
		name   string
		result *LocalRunResult
		plan   *Plan
		output LifecycleOutputPlan
	}{
		{"decoded description", &decoded, plan, output},
		{"equal Plan other Run", &LocalRunResult{PlanDigest: result.PlanDigest}, plan, output},
		{"changed complete Plan", result, changed, output},
		{"sibling same Build", result, plan, sibling},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, finish, err := result.debian.begin(t.Context(), tc.result, tc.plan, tc.output)
			if ctx != nil || finish != nil || factoryErrorCode(err) != e.CodeVerifyFailed {
				t.Fatal("description restored original custody", err)
			}
		})
	}
	// A fresh exact description is allowed only with the original live Run owner.
	ctx, finish, err := result.debian.begin(t.Context(), result, cloneDebianTestPlan(t, plan), output)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal(ctx.Err())
	}
	finish()
}

func TestDebianRunCustodyTransfersOriginalSessionThroughFinish(t *testing.T) {
	plan, result, session := debianRunOwnerFixture(t)
	if err := finishLocalRun(result, session, true, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.RequireOpenContext(t.Context()); err != nil {
		t.Fatal("successful custody handoff closed producer too early", err)
	}
	type key struct{}
	deadline := time.Now().Add(time.Minute)
	caller, cancel := context.WithDeadline(context.WithValue(t.Context(), key{}, "value"), deadline)
	defer cancel()
	owned, finish, err := result.debian.begin(caller, result, plan, plan.LifecycleOutputs[0])
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := owned.Deadline(); !ok || !got.Equal(deadline) || owned.Value(key{}) != "value" {
		t.Fatal("custody lost caller deadline or values")
	}
	done := make(chan error, 1)
	go func() { done <- result.Close() }()
	select {
	case <-result.debian.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Run close did not publish cancellation")
	}
	if owned.Err() != context.Canceled {
		t.Fatal("Run close did not synchronously cancel original operation")
	}
	select {
	case <-done:
		t.Fatal("Run closed before custody operation joined")
	default:
	}
	finish()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := session.RequireOpenContext(t.Context()); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatal("Run close retained producer session", err)
	}
	if _, _, err := result.debian.begin(t.Context(), result, plan, plan.LifecycleOutputs[0]); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatal("closed Run restored custody", err)
	}
}

func TestDebianRunCustodyRejectsForeignSessionAndJoinsFailure(t *testing.T) {
	for _, tc := range []struct {
		name    string
		foreign bool
	}{
		{"foreign session", true}, {"failure after completion", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, result, session := debianRunOwnerFixture(t)
			other, err := pipeline.NewNodeSession(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(other.Close)
			if tc.foreign {
				if err := finishLocalRun(result, other, true, nil); factoryErrorCode(err) != e.CodeVerifyFailed {
					t.Fatal("foreign session accepted", err)
				}
			} else {
				cause := e.New(e.CodeBuildFailed, "original failure")
				if err := finishLocalRun(result, session, true, cause); !errors.Is(err, cause) {
					t.Fatal("Run failure was lost", err)
				}
			}
			if err := session.RequireOpenContext(t.Context()); factoryErrorCode(err) != e.CodeEngineCapability {
				t.Fatal("failed custody leaked original session", err)
			}
			if err := other.RequireOpenContext(t.Context()); err != nil {
				t.Fatal("failure closed an unrelated session", err)
			}
		})
	}
}

func TestDebianRunCustodyFreezesSessionFacade(t *testing.T) {
	_, result, facade := debianRunOwnerFixture(t)
	original := *facade
	other, err := pipeline.NewNodeSession(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(other.Close)
	*facade = *other
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
	if err := original.RequireOpenContext(t.Context()); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatal("Run close followed replacement facade", err)
	}
	if err := other.RequireOpenContext(t.Context()); err != nil {
		t.Fatal("Run closed unrelated replacement", err)
	}
}

func TestDebianRunCustodyFailureNeverClosesForeignSession(t *testing.T) {
	for _, replaced := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreign argument", true: "replaced facade"}[replaced], func(t *testing.T) {
			_, result, facade := debianRunOwnerFixture(t)
			original := *facade
			other, err := pipeline.NewNodeSession(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(other.Close)
			argument := other
			if replaced {
				*facade = *other
				argument = facade
			}
			cause := e.New(e.CodeBuildFailed, "original producer failure")
			if err := finishLocalRun(result, argument, false, cause); !errors.Is(err, cause) {
				t.Fatal("original failure lost", err)
			}
			if err := original.RequireOpenContext(t.Context()); factoryErrorCode(err) != e.CodeEngineCapability {
				t.Fatal("failure retained original session", err)
			}
			if err := other.RequireOpenContext(t.Context()); err != nil {
				t.Fatal("failure closed unrelated session", err)
			}
		})
	}
}
