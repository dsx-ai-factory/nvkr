// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPrivatePublicationWaitsForSeedVisibility(t *testing.T) {
	for _, tc := range []struct {
		name    string
		missing int
		refusal string
		waits   int
		code    e.Code
	}{
		{"immediate", 0, "", 0, ""},
		{"delayed", 1, "", 1, ""},
		{"last attempt", 4, "", 4, ""},
		{"never visible", 5, "", 4, e.CodeTrustPolicyMissing},
		{"public", 1, "public", 1, e.CodeTrustPolicyMissing},
		{"linked", 1, "linked", 1, e.CodeTrustPolicyMissing},
		{"wrong package", 1, "wrong package", 1, e.CodeTrustPolicyMissing},
		{"malformed", 1, "malformed", 1, e.CodeTrustPolicyMissing},
		{"forbidden", 1, "forbidden", 1, e.CodeFetchFailed},
		{"canceled", 1, "canceled", 1, e.CodeTimeout},
		{"disappeared at final check", 1, "disappeared", 1, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reads := 0
			f, _ := publicationBackend(t, func(packageName string, count int) (int, any) {
				if packageName == "source/first" {
					return http.StatusOK, publicationPackage(packageName)
				}
				reads++
				// Two pre-seed reads, then bounded confirmation of the write.
				if count <= 2+tc.missing {
					return http.StatusNotFound, nil
				}
				p := publicationPackage(packageName)
				switch tc.refusal {
				case "public":
					p["visibility"] = "public"
				case "linked":
					p["repository"] = map[string]any{"id": 3, "private": true}
				case "wrong package":
					p["name"] = "source/other"
				case "malformed":
					return http.StatusOK, "malformed package"
				case "forbidden":
					return http.StatusForbidden, nil
				case "disappeared":
					if count == 4+tc.missing {
						return http.StatusNotFound, nil
					}
				}
				return http.StatusOK, p
			})
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			waits := 0
			var seedRequests int64
			f.b.wait = func(waitCtx context.Context, delay time.Duration) error {
				if delay != (2*time.Second)<<waits {
					t.Errorf("wait %d duration %s", waits, delay)
				}
				wantDeadline, _ := ctx.Deadline()
				if deadline, ok := waitCtx.Deadline(); !ok || !deadline.Equal(wantDeadline) {
					t.Error("seed confirmation changed caller deadline")
				}
				requests := f.transport.calls.Load()
				if requests == 0 || waits > 0 && requests != seedRequests {
					t.Error("confirmation rewrote seed or waited before seeding")
				}
				seedRequests = requests
				waits++
				if tc.refusal == "canceled" {
					cancel()
					return waitCtx.Err()
				}
				return nil
			}
			// An earlier output in a batch has already admitted its own policy.
			if err := f.b.admitPrivatePublication(ctx, "Example/Source", []string{"ghcr.io/example/source/first"}, publisherOptions); err != nil {
				t.Fatal(err)
			}
			if f.transport.calls.Load() != 0 {
				t.Fatal("existing first package wrote a seed")
			}
			err := f.b.admitPrivatePublication(ctx, "Example/Source", []string{"ghcr.io/example/source/second"}, publisherOptions)
			if tc.code != "" {
				assertCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
			if waits > 0 && f.transport.calls.Load() != seedRequests {
				t.Error("confirmation rewrote the seed after its last wait")
			}
			if waits != tc.waits {
				t.Errorf("waits=%d, want %d", waits, tc.waits)
			}
			wantReads := 3 + tc.missing
			if tc.code == "" || tc.refusal == "disappeared" {
				wantReads++
			}
			if tc.refusal == "canceled" {
				wantReads = 3
			}
			if tc.name == "never visible" {
				wantReads = 7
			}
			if reads != wantReads {
				t.Errorf("package reads=%d, want %d", reads, wantReads)
			}
		})
	}
}

func TestSeedVisibilityPreservesProviderRetryBudget(t *testing.T) {
	f, _ := publicationBackend(t, func(packageName string, count int) (int, any) {
		if count <= 3 {
			return http.StatusNotFound, nil
		}
		if count <= 5 {
			return http.StatusTooManyRequests, nil
		}
		return http.StatusOK, publicationPackage(packageName)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	waits := []time.Duration{}
	f.b.wait = func(ctx context.Context, delay time.Duration) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) < 7*time.Minute || time.Until(deadline) > 10*time.Minute {
			t.Error("post-seed confirmation shortened provider retry budget")
		}
		waits = append(waits, delay)
		return nil
	}
	if err := f.b.admitPrivatePublication(ctx, "Example/Source", []string{"ghcr.io/example/source/releases"}, publisherOptions); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(waits, []time.Duration{2 * time.Second, 60 * time.Second, 120 * time.Second}) {
		t.Fatalf("waits=%v", waits)
	}
}

func TestPrivateCarrierWaitsForSeedVisibility(t *testing.T) {
	f := newStorageFixture(t, func(count int) (int, any) {
		if count <= 2 {
			return http.StatusNotFound, nil
		}
		return http.StatusOK, privatePackage()
	})
	waits := 0
	f.b.wait = func(context.Context, time.Duration) error { waits++; return nil }
	root := t.TempDir()
	writePayload(t, root, "payload", "exact payload", 0600)
	if _, err := f.b.upload(t.Context(), testRequest, root, []string{"payload"}, testOptions); err != nil {
		t.Fatal(err)
	}
	if waits != 1 || f.gets != 4 {
		t.Fatalf("waits=%d, package reads=%d", waits, f.gets)
	}
}
