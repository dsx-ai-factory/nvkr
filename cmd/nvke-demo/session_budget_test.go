// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
)

func TestSessionCleanupBudgetUsesOwningTeardownCeiling(t *testing.T) {
	for _, row := range []struct {
		name         string
		command      string
		callerBudget time.Duration
		want         time.Duration
	}{
		{"cleanup", "cleanup", 0, nvkedemo.RetainedCleanupTimeout},
		{"status", "status", 0, 30 * time.Minute},
		{"caller bound", "cleanup", time.Second, time.Second},
	} {
		t.Run(row.name, func(t *testing.T) {
			ctx := t.Context()
			if row.callerBudget > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, row.callerBudget)
				defer cancel()
			}
			called := false
			action := func(ctx context.Context, session string, _ io.Writer) error {
				called = true
				deadline, bounded := ctx.Deadline()
				remaining := time.Until(deadline)
				if !bounded || remaining > row.want || remaining < row.want-500*time.Millisecond || session != "/selected-private-session" {
					t.Fatalf("incorrect owner budget: bounded=%t remaining=%v", bounded, remaining)
				}
				return nil
			}
			root := newCommand(io.Discard, io.Discard, demoActions{cleanup: action, status: action})
			if err := executeRoot(ctx, root, []string{row.command, "--plain", "--session", "/selected-private-session"}); err != nil || !called {
				t.Fatal("session operation did not receive its budget", err)
			}
		})
	}
}
