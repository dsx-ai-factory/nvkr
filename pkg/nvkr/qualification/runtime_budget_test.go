// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestHostedRuntimeBudgetBoundsCompleteOperation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		parentTimeout time.Duration
		wantTimeout   time.Duration
	}{
		{"no caller deadline", 0, 59 * time.Minute},
		{"shorter caller deadline", 5 * time.Minute, 5 * time.Minute},
		{"longer caller deadline", 90 * time.Minute, 59 * time.Minute},
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
				ctx, cancel := WithHostedRuntimeBudget(parent)
				defer cancel()
				if deadline, ok := ctx.Deadline(); !ok || !deadline.Equal(start.Add(tc.wantTimeout)) {
					t.Fatalf("deadline = %v, present = %t; want %v", deadline, ok, start.Add(tc.wantTimeout))
				}
				finished := make(chan error, 1)
				go func() {
					<-ctx.Done()
					finished <- ctx.Err()
				}()
				time.Sleep(tc.wantTimeout - time.Nanosecond)
				synctest.Wait()
				select {
				case err := <-finished:
					t.Fatalf("operation stopped before its deadline: %v", err)
				default:
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				if err := <-finished; !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("operation did not observe its deadline: %v", err)
				}
				if tc.parentTimeout == 0 || tc.parentTimeout > tc.wantTimeout {
					if err := parent.Err(); err != nil {
						t.Fatalf("hosted deadline canceled its caller: %v", err)
					}
				}
			})
		})
	}
}

func TestHostedRuntimeBudgetPreservesCancellation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		parent bool
		before bool
	}{
		{"inherited cancellation", true, false},
		{"already canceled caller", true, true},
		{"owner release", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancelParent := context.WithCancel(t.Context())
				defer cancelParent()
				if tc.before {
					cancelParent()
				}
				ctx, cancel := WithHostedRuntimeBudget(parent)
				defer cancel()
				finished := make(chan error, 1)
				go func() {
					<-ctx.Done()
					finished <- ctx.Err()
				}()
				if !tc.before {
					time.Sleep(time.Minute)
					if err := ctx.Err(); err != nil {
						t.Fatalf("owner canceled before requested: %v", err)
					}
					if tc.parent {
						cancelParent()
					} else {
						cancel()
					}
				}
				synctest.Wait()
				if err := <-finished; !errors.Is(err, context.Canceled) {
					t.Fatalf("operation lost cancellation: %v", err)
				}
				if !tc.parent && parent.Err() != nil {
					t.Fatal("owner release canceled its caller")
				}
			})
		})
	}
}
