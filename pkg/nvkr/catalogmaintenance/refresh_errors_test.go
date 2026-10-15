// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestRefreshPreservesTimeoutAfterRealPinCommit(t *testing.T) {
	cfg, promote := maintenanceFixture(t)
	promote("1.1.0")
	beforeLock := readFixture(t, "catalog/pins.lock.yaml")
	beforePlan := readFixture(t, ".github/nvkr-run/plan.json")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result, err := refresh(ctx, cfg, "test (deadbeef)", false, func(ctx context.Context, request catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
		update, err := catalog.UpdateSourcePins(ctx, request)
		if err == nil {
			cancel()
		}
		return update, err
	})
	assertCode(t, err, e.CodeTimeout)
	if e.ExitCode(err) != 31 || !e.Retryable(err) || !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation cause/classification: %v", err)
	}
	if !strings.Contains(err.Error(), "pins committed but regeneration failed") || !strings.Contains(err.Error(), "retain the disposable checkout") {
		t.Fatalf("missing recovery context: %v", err)
	}
	if !result.Changed || result.Update.Changed != 3 || bytes.Equal(beforeLock, readFixture(t, "catalog/pins.lock.yaml")) || !bytes.Equal(beforePlan, readFixture(t, ".github/nvkr-run/plan.json")) {
		t.Fatalf("cancellation lost the committed lock boundary: %+v", result)
	}
	lock, err := catalog.LoadLockContext(t.Context(), "catalog")
	if err != nil || lock.Pins["root"].Tag != "release/v1.1.0" {
		t.Fatalf("complete committed lock missing: %+v %v", lock, err)
	}
}

func TestRegenerationCheckPreservesOwnerError(t *testing.T) {
	// Real pin commits, regeneration and retained failure state are covered by
	// TestRefreshRealLocalPinSetJourney and TestRefreshFailureBoundaries. Check
	// this adapter's error propagation without repeating that Git/compile work.
	for _, tc := range []struct {
		name string
		code e.Code
	}{
		{"canceled", e.CodeTimeout},
		{"drift", e.CodePipelineDrift},
		{"invalid definition", e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Definition: "definition.yaml", Environment: "environment.yaml"}
			cause := errors.New("owner failure")
			if tc.code == e.CodeTimeout {
				cause = context.Canceled
			}
			ownerErr := e.Wrap(tc.code, cause, "candidate check failed")
			calls := 0
			digest, err := regenerate(t.Context(), cfg, "test (deadbeef)", func(ctx context.Context, definition, environment, version, engine, root string, check bool) (string, error) {
				calls++
				if ctx != t.Context() || definition != cfg.Definition || environment != cfg.Environment || version != "test (deadbeef)" || engine != "github" || root != "." {
					t.Fatal("generation changed selected inputs or context")
				}
				if check != (calls == 2) {
					t.Fatalf("unexpected generation/check order: call %d, check %v", calls, check)
				}
				if check {
					return "", ownerErr
				}
				return "candidate-digest", nil
			})
			assertCode(t, err, tc.code)
			if !errors.Is(err, ownerErr) || !errors.Is(err, cause) {
				t.Fatalf("lost owner error: %v", err)
			}
			if calls != 2 || digest != "" || !strings.Contains(err.Error(), "candidate regeneration did not reproduce") || !strings.Contains(err.Error(), "retain the disposable checkout") {
				t.Fatalf("failed final check became a candidate: digest=%s calls=%d error=%v", digest, calls, err)
			}
			if tc.code == e.CodeTimeout && (e.ExitCode(err) != 31 || !e.Retryable(err) || !errors.Is(err, context.Canceled)) {
				t.Fatalf("lost second-check cancellation classification: %v", err)
			}
			if tc.code == e.CodePipelineDrift && (e.ExitCode(err) != 51 || e.Retryable(err)) {
				t.Fatalf("drift was reclassified: %v", err)
			}
		})
	}
}
