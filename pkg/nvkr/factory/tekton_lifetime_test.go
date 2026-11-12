// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestTektonLeaseLossCancelsAndJoinsWork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
	}{
		{"cancellation", e.New(e.CodeVerifyFailed, "provider Run canceled")},
		{"provider loss", e.New(e.CodeFetchFailed, "provider unreachable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			var joined atomic.Bool
			err := watchTektonTask(ctx, time.Millisecond, func(context.Context) error { return tc.failure }, func(ctx context.Context) error {
				<-ctx.Done()
				joined.Store(true)
				return ctx.Err()
			})
			if !errors.Is(err, tc.failure) || !joined.Load() {
				t.Fatalf("provider revocation did not stop/join work: %v", err)
			}
		})
	}
}

func TestTektonCompletionJoinsBlockedProviderRead(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	entered, joined := make(chan struct{}), make(chan struct{})
	err := watchTektonTask(ctx, time.Millisecond, func(ctx context.Context) error {
		close(entered)
		<-ctx.Done()
		close(joined)
		return ctx.Err()
	}, func(ctx context.Context) error {
		select {
		case <-entered:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-joined:
	default:
		t.Fatal("task returned before provider watcher joined")
	}
}
