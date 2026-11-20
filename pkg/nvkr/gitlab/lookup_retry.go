// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package gitlab

import (
	"context"
	"net/http"
	"strconv"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// lookupGET retries only throttled, idempotent metadata reads, preserving the
// same endpoint, credentials and lookup deadline. No failed response is reused.
func (c *Client) lookupGET(ctx context.Context, request *http.Request) (*http.Response, error) {
	wait := c.waitRetry
	if wait == nil {
		wait = waitGitLabRetry
	}
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return nil, e.New(e.CodeTimeout, "GitLab lookup canceled")
		}
		response, err := c.http.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, e.New(e.CodeTimeout, "GitLab lookup canceled")
			}
			return nil, e.New(e.CodeUnavailable, "GitLab lookup transport failed")
		}
		if response.StatusCode != http.StatusTooManyRequests {
			return response, nil
		}
		if err := response.Body.Close(); err != nil {
			return nil, e.New(e.CodeUnavailable, "GitLab throttled response could not be closed")
		}
		if attempt == 2 {
			return nil, e.New(e.CodeUnavailable, "GitLab lookup throttling retry budget exhausted")
		}
		delay, err := gitlabRetryDelay(response.Header, attempt, time.Now())
		if err != nil {
			return nil, err
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
			return nil, e.New(e.CodeTimeout, "GitLab throttling delay exceeds remaining lookup time")
		}
		if err := wait(ctx, delay); err != nil {
			return nil, e.New(e.CodeTimeout, "GitLab lookup retry canceled")
		}
	}
	return nil, e.New(e.CodeInternal, "GitLab lookup retry state invalid")
}

func waitGitLabRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func gitlabRetryDelay(header http.Header, attempt int, now time.Time) (time.Duration, error) {
	values := header.Values("Retry-After")
	if len(values) == 0 {
		return time.Second << attempt, nil
	}
	if len(values) == 1 {
		if seconds, err := strconv.ParseUint(values[0], 10, 32); err == nil && seconds <= 30 {
			return time.Duration(seconds) * time.Second, nil
		}
		if until, err := http.ParseTime(values[0]); err == nil {
			delay := max(until.Sub(now), 0)
			if delay <= 30*time.Second {
				return delay, nil
			}
		}
	}
	return 0, e.New(e.CodeUnavailable, "GitLab throttling delay is invalid or exceeds the bounded retry allowance")
}
