// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestHostedDiskPublicationReservation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		owner *LocalRunResult
		want  e.Code
	}{
		{name: "new", owner: &LocalRunResult{}},
		{name: "package completed", owner: &LocalRunResult{Nodes: []LocalNodeResult{{ID: "package"}}}, want: e.CodeImmutableViolation},
		{name: "evidence completed", owner: &LocalRunResult{Nodes: []LocalNodeResult{{ID: "evidence"}}}, want: e.CodeImmutableViolation},
		{name: "pending", owner: &LocalRunResult{diskPublicationPending: map[string]bool{"package": true}}, want: e.CodeImmutableViolation},
		{name: "closed", owner: &LocalRunResult{diskClosed: true}, want: e.CodeGateUnmet},
		{name: "closing", owner: &LocalRunResult{diskClosing: true}, want: e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release, err := tc.owner.reserveHostedDiskPublication(t.Context(), "package", "evidence")
			if factoryErrorCode(err) != tc.want {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
			if tc.want != "" {
				if release != nil {
					t.Fatal("failed reservation returned permit")
				}
				return
			}
			release()
			if len(tc.owner.diskPublicationPending) != 0 {
				t.Fatal("reservation leaked")
			}
		})
	}
}

func TestHostedDiskPublicationCloseJoinsReservedCompletion(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "aborted"
		if committed {
			name = "committed"
		}
		t.Run(name, func(t *testing.T) {
			owner := &LocalRunResult{}
			release, err := owner.reserveHostedDiskPublication(t.Context(), "package", "evidence")
			if err != nil {
				t.Fatal(err)
			}
			defer release()
			closed := make(chan error, 1)
			go func() { closed <- owner.Close() }()
			deadline := time.Now().Add(5 * time.Second)
			for {
				owner.diskMu.Lock()
				closing := owner.diskClosing
				owner.diskMu.Unlock()
				if closing {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("Close did not revoke new admission")
				}
				time.Sleep(time.Millisecond)
			}
			select {
			case err := <-closed:
				t.Fatalf("Close abandoned a reserved completion: %v", err)
			default:
			}
			if committed {
				// The durable commit is now complete: late cancellation/Close
				// must not erase its descriptive node outcomes.
				owner.recordCommittedHostedDiskPublication("package", "evidence")
			}
			release()
			release() // cleanup is idempotent, including its wait-group release
			select {
			case err := <-closed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Close did not join the released completion")
			}
			want := 0
			if committed {
				want = 2
			}
			if len(owner.Nodes) != want || len(owner.diskPublicationPending) != 0 {
				t.Fatalf("completion outcomes=%v, pending=%v", owner.Nodes, owner.diskPublicationPending)
			}
		})
	}
}

func TestHostedDiskPublicationConcurrentReservation(t *testing.T) {
	owner := &LocalRunResult{}
	const callers = 16
	ready := make(chan struct{})
	results := make(chan error, callers)
	cleanup := make(chan func(), callers)
	for range callers {
		go func() {
			<-ready
			release, err := owner.reserveHostedDiskPublication(t.Context(), "package", "evidence")
			cleanup <- release
			results <- err
		}()
	}
	close(ready)
	admitted := 0
	for range callers {
		err := <-results
		if err == nil {
			admitted++
		} else if factoryErrorCode(err) != e.CodeImmutableViolation {
			t.Fatal(err)
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted %d competing completions", admitted)
	}
	for range callers {
		if release := <-cleanup; release != nil {
			release()
		}
	}
	release, err := owner.reserveHostedDiskPublication(t.Context(), "other-package", "other-evidence")
	if err != nil {
		t.Fatal(err)
	}
	release()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if release, err := owner.reserveHostedDiskPublication(ctx, "package", "evidence"); release != nil || factoryErrorCode(err) != e.CodeTimeout {
		t.Fatalf("canceled reservation: %v", err)
	}
}
