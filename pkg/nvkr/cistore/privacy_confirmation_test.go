// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPrivatePublicationWaitsForSeedConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		missing    int
		status     int
		visibility string
		cancelWait bool
		want       e.Code
		waits      int
	}{
		{"immediate", 0, 200, "private", false, "", 0},
		{"eventual", 3, 200, "private", false, "", 3},
		{"last attempt", 5, 200, "internal", false, "", 5},
		{"never visible", 6, 200, "private", false, e.CodeTrustPolicyMissing, 5},
		{"public after absence", 1, 200, "public", false, e.CodeTrustPolicyMissing, 1},
		{"unknown after absence", 1, 200, "unknown", false, e.CodeTrustPolicyMissing, 1},
		{"denied after absence", 1, 401, "private", false, e.CodeFetchFailed, 1},
		{"cancel during wait", 6, 200, "private", true, e.CodeTimeout, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
			defer cancel()
			operationDeadline, _ := ctx.Deadline()
			var reads atomic.Int64
			f, _ := publicationBackend(t, func(packageName string, count int) (int, any) {
				reads.Add(1)
				// Whole-set admission and the pre-seed check see no package.
				if count <= 2+tc.missing {
					return http.StatusNotFound, nil
				}
				p := publicationPackage(packageName)
				p["visibility"] = tc.visibility
				return tc.status, p
			})
			var waits []time.Duration
			var seedCalls int64
			f.b.wait = func(waitCtx context.Context, delay time.Duration) error {
				deadline, ok := waitCtx.Deadline()
				if !ok || !deadline.Equal(operationDeadline) {
					t.Fatal("absence wait changed the operation deadline")
				}
				waits = append(waits, delay)
				if len(waits) == 1 {
					seedCalls = f.transport.calls.Load()
				}
				if tc.cancelWait {
					cancel()
					return waitCtx.Err()
				}
				return nil
			}
			err := f.b.admitPrivatePublication(ctx, "Example/Source", []string{"ghcr.io/example/source/releases"}, publisherOptions)
			if tc.want != "" {
				assertCode(t, err, tc.want)
			} else if err != nil {
				t.Fatal(err)
			}
			backoff := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 32 * time.Second}
			if !slices.Equal(waits, backoff[:tc.waits]) {
				t.Fatalf("confirmation waits = %v", waits)
			}
			wantReads := 2 + min(tc.missing+1, 6)
			if tc.cancelWait {
				wantReads = 3
			} else if tc.want == "" {
				wantReads++ // Final whole-set revalidation remains mandatory.
			}
			if reads.Load() != int64(wantReads) {
				t.Fatalf("package checks = %d, want %d", reads.Load(), wantReads)
			}
			if len(waits) != 0 && (seedCalls == 0 || f.transport.calls.Load() != seedCalls) {
				t.Fatal("confirmation repeated registry work")
			}
		})
	}
}

func TestSeedConfirmationPreservesRateLimitBudget(t *testing.T) {
	for _, tc := range []struct {
		name       string
		limited    int
		retryAfter string
		elapsed    time.Duration
	}{
		{"server delay", 1, "120", 122 * time.Second},
		{"success after two limits", 2, "", 182 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				b := newBackend()
				calls := 0
				b.client.Transport = admissionTransportFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					status := http.StatusOK
					if calls == 1 {
						status = http.StatusNotFound
					} else if calls <= 1+tc.limited {
						status = http.StatusTooManyRequests
					}
					response := admissionResponseFor(r, status, "", `{"name":"source/releases","package_type":"container","visibility":"private","owner":{"login":"Example"},"repository":null}`)
					if status == http.StatusTooManyRequests && tc.retryAfter != "" {
						response.Header.Set("Retry-After", tc.retryAfter)
					}
					return response, nil
				})
				started := time.Now()
				source := repositoryRecord{Owner: account{Login: "Example", Type: "Organization"}}
				if err := b.confirmSeedPrivacy(t.Context(), "source/releases", source, publisherOptions, true); err != nil {
					t.Fatal(err)
				}
				if calls != tc.limited+2 || time.Since(started) != tc.elapsed {
					t.Fatalf("confirmation calls=%d elapsed=%v; want calls=%d elapsed=%v", calls, time.Since(started), tc.limited+2, tc.elapsed)
				}
			})
		})
	}
}
