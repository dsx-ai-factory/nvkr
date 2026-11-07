// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sync/semaphore"
)

func TestMetadataBlobReadDeclaredBound(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		size, max  int64
		valid      bool
		consumed   int
	}{
		{"exact", "abc", 3, 8, true, 3},
		{"empty", "", 0, 8, true, 0},
		{"oversized response", strings.Repeat("a", 100), 3, 8, false, 4},
		{"short response", "ab", 3, 8, false, 2},
		{"oversized declaration", "abc", 3, 2, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := strings.NewReader(tc.body)
			writer := metadataBlobWriter{writer: layoutWriter{root: t.TempDir()}, limit: 16, inflight: semaphore.NewWeighted(maxMetadataReadBytes)}
			err := writer.copyBlob(t.Context(), Descriptor{Digest: digestBytes([]byte(tc.body)), Size: tc.size}, tc.max, func() (io.ReadCloser, error) { return io.NopCloser(reader), nil })
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
			}
			if consumed := len(tc.body) - reader.Len(); consumed != tc.consumed {
				t.Fatalf("read %d bytes, want %d", consumed, tc.consumed)
			}
		})
	}
}

func TestMetadataBlobReservationIncludesPendingWrites(t *testing.T) {
	writer := metadataBlobWriter{writer: layoutWriter{root: t.TempDir()}, limit: 32, inflight: semaphore.NewWeighted(8)}
	d := Descriptor{Digest: digestBytes([]byte("abc")), Size: 3}
	opened := make(chan struct{}, 2)
	results := make(chan error, 2)
	writer.mu.Lock()
	for range 2 {
		go func() {
			results <- writer.copyBlob(t.Context(), d, 8, func() (io.ReadCloser, error) {
				opened <- struct{}{}
				return io.NopCloser(strings.NewReader("abc")), nil
			})
		}()
	}
	for range 2 {
		<-opened
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err := writer.copyBlob(ctx, d, 8, func() (io.ReadCloser, error) {
		t.Error("opened a response without a buffer reservation")
		return io.NopCloser(strings.NewReader("abc")), nil
	})
	writer.mu.Unlock()
	for range 2 {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	assertRegistryCode(t, err, e.CodeTimeout)
	// Reservations are released after writes, so subsequent reads can proceed.
	if err := writer.copyBlob(t.Context(), d, 8, func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("abc")), nil }); err != nil {
		t.Fatal(err)
	}
}
