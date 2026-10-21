// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Exercise the native callback's real finalizer. A returned/panicked operation
// must not invalidate its native scope until the live owner admits cleanup.
func TestNativeScopeRetainsUntilExplicitCleanup(t *testing.T) {
	for _, mode := range []string{"ready", "failure", "cancel", "panic"} {
		t.Run(mode, func(t *testing.T) {
			owner, work, cancelWork := retainedTestOwner(t)
			var scope *nativeSiteScope
			joined := make(chan error, 1)
			go func() {
				joined <- withNativeSiteScope(work, &nativeSiteAttempt{}, Runner{}, "", "", nil, func(ctx context.Context, site *NativeSite) error {
					scope = site.scope
					switch mode {
					case "ready":
						return owner.markReady(ctx, OperatorAccess{kubectl: "/fixture/tool", site: "/fixture/site", tenant: "/fixture/tenant", siteQCOW: true})
					case "failure":
						return errors.New("synthetic suite failure")
					case "cancel":
						cancelWork()
						return ctx.Err()
					default:
						panic("synthetic operation panic")
					}
				})
			}()
			state := retainedObserve(t, owner)
			if (state.state == retainedReady) != (mode == "ready") {
				t.Fatal("failed operation invented readiness", state.state)
			}
			// Canceling work after success also grants no cleanup authority.
			cancelWork()
			select {
			case err := <-joined:
				t.Fatal("native scope unwound without explicit cleanup", err)
			default:
			}
			if err := owner.requestCleanup(context.WithoutCancel(t.Context())); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-joined:
				if (err == nil) != (mode == "ready") || scope == nil || !scope.closed {
					t.Fatal("cleanup lost scope invalidation or original failure", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("explicit native cleanup did not join")
			}
		})
	}
}
