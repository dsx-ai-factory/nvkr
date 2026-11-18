// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestVerificationWorkersAdmission(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"1", 1}, {"4", 4}, {"16", 16}, {"", 0}, {"0", 0}, {"17", 0},
		{"-1", 0}, {"04", 0}, {"+4", 0}, {" 4", 0}, {"unknown", 0},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			t.Setenv("NVKR_VERIFY_WORKERS", tc.raw)
			got, err := verificationWorkers()
			if tc.want == 0 {
				assertFactoryErrorCode(t, err, e.CodeUsage)
			} else if err != nil || got != tc.want {
				t.Fatalf("workers = %d, %v", got, err)
			}
		})
	}
}

func TestVerifyBatchBoundedCompleteAndOrdered(t *testing.T) {
	for _, workers := range []int{1, 2, 4} {
		t.Run(string(rune('0'+workers)), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			names := []string{"a", "b", "c", "d", "e", "f"}
			entered := make(chan struct{}, len(names))
			release := make(chan struct{})
			done := make(chan error, 1)
			var active, peak, calls atomic.Int32
			go func() {
				done <- verifyBatch(ctx, names, workers, func(ctx context.Context, i int) error {
					n := active.Add(1)
					defer active.Add(-1)
					for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
					}
					calls.Add(1)
					entered <- struct{}{}
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					if i%2 == 0 {
						return e.New(e.CodeSignatureInvalid, "refused-%s", names[i])
					}
					return nil
				})
			}()
			for range workers {
				select {
				case <-entered:
				case <-ctx.Done():
					<-done
					t.Fatal("worker pool did not fill")
				}
			}
			close(release)
			err := <-done
			assertFactoryErrorCode(t, err, e.CodeSignatureInvalid)
			if calls.Load() != int32(len(names)) || peak.Load() != int32(workers) || active.Load() != 0 {
				t.Fatalf("calls=%d peak=%d active=%d", calls.Load(), peak.Load(), active.Load())
			}
			last := -1
			for _, name := range []string{"a", "c", "e"} {
				position := strings.Index(err.Error(), "refused-"+name)
				if position <= last {
					t.Fatalf("missing or unordered errors: %v", err)
				}
				last = position
			}
		})
	}
}

func TestVerifyBatchCancellationJoinsAndStopsWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered := make(chan struct{}, 2)
	done := make(chan error, 1)
	var active, calls atomic.Int32
	go func() {
		done <- verifyBatch(ctx, []string{"a", "b", "c", "d"}, 2, func(ctx context.Context, _ int) error {
			active.Add(1)
			defer active.Add(-1)
			calls.Add(1)
			entered <- struct{}{}
			<-ctx.Done()
			return classifyContext(ctx, ctx.Err())
		})
	}()
	for range 2 {
		<-entered
	}
	cancel()
	err := <-done
	assertFactoryErrorCode(t, err, e.CodeTimeout)
	if !errors.Is(err, context.Canceled) || active.Load() != 0 || calls.Load() != 2 {
		t.Fatalf("cancel/join: %v active=%d calls=%d", err, active.Load(), calls.Load())
	}
}

func TestVerifyBatchCancellationPreservesEarlierFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	err := verifyBatch(ctx, []string{"failed", "canceled"}, 1, func(_ context.Context, i int) error {
		if i == 0 {
			return e.New(e.CodeSignatureInvalid, "earlier signature failure")
		}
		cancel()
		return nil
	})
	assertFactoryErrorCode(t, err, e.CodeTimeout)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "earlier signature failure") {
		t.Fatalf("cancellation lost previous failure or cause: %v", err)
	}
}
