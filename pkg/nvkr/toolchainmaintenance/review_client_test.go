// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestReviewOutputBound(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		writer := reviewOutput{cancel: cancel}
		raw := make([]byte, maxReviewBytes)
		if n, err := writer.Write(raw); err != nil || n != len(raw) {
			t.Fatalf("exact bound: %d %v", n, err)
		}
		if overflow {
			if _, err := writer.Write([]byte{1}); !errors.Is(err, io.ErrShortBuffer) {
				t.Fatal(err)
			}
			if ctx.Err() == nil {
				t.Fatal("overflow did not cancel child")
			}
		} else if ctx.Err() != nil {
			t.Fatal(ctx.Err())
		}
		if len(writer.data) != maxReviewBytes {
			t.Fatal("capture exceeded bound")
		}
		cancel()
	}
}
func TestMalformedReviewPagesFailClosed(t *testing.T) {
	for _, raw := range []string{"null", "{}", "[null]", "[{}]", "[[null]]", "[[], [null]]", "not-json"} {
		t.Run(raw, func(t *testing.T) {
			c := reviewClient{invoke: func(context.Context, []string, []byte) ([]byte, error) { return []byte(raw), nil }}
			var issues []refreshIssue
			requireReviewError(t, c.pages(t.Context(), "issues", &issues))
		})
	}
}
func TestReviewClientSuppressesChildDiagnostics(t *testing.T) {
	c := newReviewClient(t.TempDir())
	_, err := c.invoke(t.Context(), []string{"sh", "-c", "echo synthetic-private-diagnostic >&2; exit 3"}, nil)
	requireReviewError(t, err)
	if strings.Contains(err.Error(), "synthetic-private-diagnostic") {
		t.Fatal("child diagnostics escaped")
	}
}
