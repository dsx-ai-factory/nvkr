// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

type scratchCleanupBudgetRuntime struct {
	*scratchTestRuntime
	budgets []time.Duration
}

func (runtime *scratchCleanupBudgetRuntime) recordBudget(ctx context.Context) {
	deadline, ok := ctx.Deadline()
	if !ok || ctx.Err() != nil {
		runtime.budgets = append(runtime.budgets, 0)
		return
	}
	runtime.budgets = append(runtime.budgets, time.Until(deadline))
}

func (runtime *scratchCleanupBudgetRuntime) Run(ctx context.Context, input io.Reader, output, diagnostic io.Writer, args ...string) error {
	runtime.recordBudget(ctx)
	return runtime.scratchTestRuntime.Run(ctx, input, output, diagnostic, args...)
}

func (runtime *scratchCleanupBudgetRuntime) Cleanup(ctx context.Context, grace time.Duration, output, diagnostic io.Writer, args ...string) error {
	runtime.recordBudget(ctx)
	return runtime.scratchTestRuntime.Cleanup(ctx, grace, output, diagnostic, args...)
}

func TestDiskInspectionScratchCleanupHasIndependentBudget(t *testing.T) {
	for _, name := range []string{"short remaining scan deadline", "canceled scan", "expired scan deadline", "nil scan context"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			switch name {
			case "canceled scan":
				cancel()
			case "expired scan deadline":
				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				defer cancel()
			case "nil scan context":
				ctx = nil
			}
			volume := "nkd-inspect-" + strings.Repeat("b", 24) + "-scratch"
			runtime := &scratchCleanupBudgetRuntime{scratchTestRuntime: &scratchTestRuntime{
				name: "podman", volume: volume, attempt: strings.Repeat("b", 24),
			}}
			scratch := &diskInspectionScratch{state: &diskInspectionScratchState{
				runtime: runtime, volume: volume, attempt: runtime.attempt,
			}}
			if err := scratch.Close(ctx); err != nil {
				t.Fatal(err)
			}
			if len(runtime.budgets) != 2 || runtime.removed != 1 || len(scratch.MountArgs()) != 0 {
				t.Fatalf("cleanup did not authenticate and remove exactly its owned volume: budgets=%v removed=%d", runtime.budgets, runtime.removed)
			}
			for _, budget := range runtime.budgets {
				if budget < 9*time.Second || budget > 10*time.Second {
					t.Fatalf("scan deadline shrank or removed the independent ten-second cleanup budget: %s", budget)
				}
			}
			if err := scratch.Close(ctx); err != nil || runtime.removed != 1 {
				t.Fatalf("idempotent cleanup = %v, removals=%d", err, runtime.removed)
			}
		})
	}
}
