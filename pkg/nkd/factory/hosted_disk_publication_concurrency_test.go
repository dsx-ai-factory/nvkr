package factory

import (
	"context"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
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
