// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func cleanupLoopFixture() ([]resourceRef, map[string]string, object) {
	refs := resourceRefs("fixture")
	owned := map[string]string{refs[0].path: "namespace-uid"}
	var found object
	found.Metadata.UID = owned[refs[0].path]
	return refs, owned, found
}
func TestCleanupPermanentDeleteRefusalStopsImmediately(t *testing.T) {
	for _, name := range []string{"Forbidden", "UID precondition conflict", "unclassified command refusal"} {
		t.Run(name, func(t *testing.T) {
			refs, owned, found := cleanupLoopFixture()
			refusal := failure(name)
			calls := 0
			err := cleanupDeleteLoop(t.Context(), refs, owned, func(_ context.Context, ref resourceRef) (object, error) {
				if ref.path == refs[0].path {
					return found, nil
				}
				return object{}, nil
			}, func(_ context.Context, ref resourceRef, body []byte) error {
				calls++
				var options struct {
					Preconditions struct {
						UID string `json:"uid"`
					} `json:"preconditions"`
				}
				if err := decodeAPI(body, &options); err != nil {
					t.Fatal(err)
				}
				if options.Preconditions.UID != owned[ref.path] {
					t.Fatal("deletion lost UID precondition")
				}
				return refusal
			}, time.Millisecond)
			requireCode(t, err, e.CodeVerifyFailed)
			if calls != 1 || !errors.Is(err, refusal) {
				t.Fatalf("refusal retried/lost: calls%d err%v", calls, err)
			}
		})
	}
}
func TestCleanupTransientDeletionPreservesCauseOnTimeout(t *testing.T) {
	for _, code := range []e.Code{e.CodeUnavailable, e.CodeTimeout} {
		t.Run(string(code), func(t *testing.T) {
			refs, owned, found := cleanupLoopFixture()
			cause := errors.New("independent deletion failure")
			transient := e.Wrap(code, cause, "bounded transient deletion")
			ctx, cancel := context.WithTimeout(t.Context(), 35*time.Millisecond)
			defer cancel()
			calls := 0
			err := cleanupDeleteLoop(ctx, refs, owned, func(_ context.Context, ref resourceRef) (object, error) {
				if ref.path == refs[0].path {
					return found, nil
				}
				return object{}, nil
			}, func(context.Context, resourceRef, []byte) error { calls++; return transient }, time.Millisecond)
			requireCode(t, err, code)
			if calls < 2 || !errors.Is(err, cause) || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("retry cause lost: calls%d err%v", calls, err)
			}
		})
	}
}
func TestCleanupDeleteFailurePreservedOnLaterGetFailure(t *testing.T) {
	refs, owned, found := cleanupLoopFixture()
	deletion := e.New(e.CodeUnavailable, "deletion unavailable")
	getErr := failure("GET refused")
	calls := 0
	err := cleanupDeleteLoop(t.Context(), refs, owned, func(_ context.Context, ref resourceRef) (object, error) {
		if calls > 0 {
			return object{}, getErr
		}
		if ref.path == refs[0].path {
			return found, nil
		}
		return object{}, nil
	}, func(context.Context, resourceRef, []byte) error { calls++; return deletion }, time.Millisecond)
	requireCode(t, err, e.CodeUnavailable)
	if !errors.Is(err, deletion) || !errors.Is(err, getErr) || calls != 1 {
		t.Fatalf("lost delete/GET causes: %v", err)
	}
}
func TestCleanupTransientDeleteCanReachTerminalAbsence(t *testing.T) {
	refs, owned, found := cleanupLoopFixture()
	calls := 0
	err := cleanupDeleteLoop(t.Context(), refs, owned, func(_ context.Context, ref resourceRef) (object, error) {
		if calls < 2 && ref.path == refs[0].path {
			return found, nil
		}
		return object{}, nil
	}, func(context.Context, resourceRef, []byte) error {
		calls++
		if calls == 1 {
			return e.New(e.CodeUnavailable, "transient")
		}
		return nil
	}, time.Millisecond)
	if err != nil || calls != 2 {
		t.Fatalf("terminal cleanup err%v calls%d", err, calls)
	}
}

func TestCleanupPollsAcceptedOrTerminatingDeletionAndVerifiesDeleteRaces(t *testing.T) {
	for _, tc := range []struct {
		name              string
		terminating, race bool
		wantDeletes       int
	}{
		{"accepted deletion is polled", false, false, 1},
		{"already terminating is polled", true, false, 0},
		{"DELETE NotFound race is verified", false, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs, owned, found := cleanupLoopFixture()
			if tc.terminating {
				stamp := "2026-10-14T00:00:00Z"
				found.Metadata.DeletionTimestamp = &stamp
			}
			deletes, gets := 0, 0
			err := cleanupDeleteLoop(t.Context(), refs, owned, func(_ context.Context, ref resourceRef) (object, error) {
				if ref.path != refs[0].path {
					return object{}, nil
				}
				gets++
				if gets >= 4 || tc.race && deletes > 0 {
					return object{}, nil
				}
				return found, nil
			}, func(context.Context, resourceRef, []byte) error {
				deletes++
				if tc.race {
					return failure("kubectl exit1 (404)")
				}
				return nil
			}, time.Millisecond)
			if err != nil || deletes != tc.wantDeletes {
				t.Fatalf("cleanup err=%v deletes=%d want=%d", err, deletes, tc.wantDeletes)
			}
		})
	}
}
