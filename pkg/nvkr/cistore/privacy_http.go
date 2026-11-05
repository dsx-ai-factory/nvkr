// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"io"
	"net/http"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// admissionGET retries only provider availability failures. It never authorizes
// metadata, forwards credentials to another endpoint, or reuses a representation
// after a failed conditional request. The caller owns successful response bodies.
func (b *backend) admissionGET(ctx context.Context, req *http.Request, key admissionResponseKey) (*http.Response, error) {
	wait, now := b.wait, b.now
	if wait == nil {
		wait = waitChunkRetry
	}
	if now == nil {
		now = time.Now
	}
	for attempt := 0; attempt < githubLocatorAPIAttempts; attempt++ {
		if ctx.Err() != nil {
			return nil, safeIOError(ctx, e.CodeTimeout, "CI storage admission canceled")
		}
		response, err := b.client.Do(req)
		if err != nil {
			return nil, safeIOError(ctx, e.CodeFetchFailed, "CI storage admission request failed")
		}
		if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNotModified || response.StatusCode == http.StatusNotFound {
			return response, nil
		}
		b.responses.forget(key)
		// Only a bounded, complete JSON message can identify a secondary limit.
		var raw []byte
		if response.StatusCode == http.StatusForbidden {
			raw, err = io.ReadAll(io.LimitReader(response.Body, maxGitHubLocatorAPIBytes+1))
			if err != nil || len(raw) > maxGitHubLocatorAPIBytes {
				raw = nil
			}
		}
		_ = response.Body.Close()
		if ctx.Err() != nil {
			return nil, safeIOError(ctx, e.CodeTimeout, "CI storage admission response canceled")
		}
		var delay time.Duration
		var retry bool
		if response.StatusCode >= 500 && response.StatusCode < 600 {
			retry = true
			delay, err = githubAPIServerDelay(response.Header, (2*time.Second)<<attempt, attempt, now())
		} else {
			delay, retry, err = githubLocatorRetryDelay(githubLocatorResponse{status: response.StatusCode, header: response.Header, body: raw}, attempt, now())
		}
		if !retry {
			return nil, e.New(e.CodeFetchFailed, "CI storage admission request returned HTTP %d", response.StatusCode)
		}
		if err != nil {
			return nil, e.Wrap(e.CodeUnavailable, err, "CI storage admission request returned HTTP %d; retry refused", response.StatusCode)
		}
		if attempt+1 == githubLocatorAPIAttempts {
			return nil, e.New(e.CodeUnavailable, "CI storage admission request returned HTTP %d; retry budget exhausted", response.StatusCode)
		}
		// A retry must obtain a complete fresh representation after any failure.
		req.Header.Del("If-None-Match")
		if err := wait(ctx, delay); err != nil {
			return nil, safeIOError(ctx, e.CodeTimeout, "CI storage admission retry wait canceled")
		}
	}
	return nil, e.New(e.CodeInternal, "CI storage admission retry state invalid")
}
