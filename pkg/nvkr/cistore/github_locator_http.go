// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	maxGitHubLocatorZIPBytes = 64 << 10
	maxGitHubLocatorAPIBytes = 1 << 20
	githubLocatorAPIAttempts = 4
	githubLocatorAPISpacing  = 100 * time.Millisecond
)

// One operation owns this serial client, its credential and pacing state. Test
// seams are private; public acquisition always uses the fixed GitHub endpoint.
type githubLocatorClient struct {
	token       string
	client      *http.Client
	lastRequest time.Time
	wait        func(context.Context, time.Duration) error
}

type githubLocatorResponse struct {
	status int
	header http.Header
	body   []byte
}

func newGitHubLocatorClient(token string) *githubLocatorClient {
	return &githubLocatorClient{token: token, wait: waitChunkRetry, client: &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (g *githubLocatorClient) getJSON(ctx context.Context, endpoint string, result any) error {
	response, err := g.apiGET(ctx, endpoint)
	if err != nil {
		return err
	}
	if response.status != http.StatusOK {
		return githubLocatorStatusError(ctx, response.status)
	}
	if canonical.ValidateJSONDocument(response.body) != nil || json.Unmarshal(response.body, result) != nil {
		return e.New(e.CodeVerifyFailed, "CI locator API returned invalid metadata")
	}
	return nil
}

func (g *githubLocatorClient) apiGET(ctx context.Context, endpoint string) (githubLocatorResponse, error) {
	for attempt := 0; attempt < githubLocatorAPIAttempts; attempt++ {
		if delay := githubLocatorAPISpacing - time.Since(g.lastRequest); delay > 0 {
			if err := g.wait(ctx, delay); err != nil {
				return githubLocatorResponse{}, safeIOError(ctx, e.CodeTimeout, "CI locator API pacing canceled")
			}
		}
		if ctx.Err() != nil {
			return githubLocatorResponse{}, safeIOError(ctx, e.CodeTimeout, "CI locator API canceled")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+endpoint, nil)
		if err != nil {
			return githubLocatorResponse{}, e.New(e.CodeInternal, "creating CI locator API request failed")
		}
		request.Header.Set("Authorization", "Bearer "+g.token)
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		g.lastRequest = time.Now()
		response, err := g.get(ctx, request, maxGitHubLocatorAPIBytes)
		if err != nil {
			return githubLocatorResponse{}, err
		}
		delay, limited, err := githubLocatorRetryDelay(response, attempt, time.Now())
		if err != nil {
			return githubLocatorResponse{}, err
		}
		if !limited {
			return response, nil
		}
		if attempt+1 == githubLocatorAPIAttempts {
			return githubLocatorResponse{}, e.New(e.CodeUnavailable, "CI locator API rate-limit retry budget exhausted")
		}
		if err := g.wait(ctx, delay); err != nil {
			return githubLocatorResponse{}, safeIOError(ctx, e.CodeTimeout, "CI locator API rate-limit wait canceled")
		}
	}
	return githubLocatorResponse{}, e.New(e.CodeInternal, "CI locator API retry state invalid")
}

// get never follows redirects and never keeps raw provider errors or bodies in
// an error chain. Every response body is bounded and closed before returning.
func (g *githubLocatorClient) get(ctx context.Context, request *http.Request, limit int64) (githubLocatorResponse, error) {
	client := *g.client
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	if err != nil {
		return githubLocatorResponse{}, githubLocatorHTTPError(ctx, err, "CI locator HTTP request failed")
	}
	if response.ContentLength > limit {
		_ = response.Body.Close()
		return githubLocatorResponse{}, e.New(e.CodeVerifyFailed, "CI locator HTTP response exceeds its byte bound")
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, limit+1))
	closeErr := response.Body.Close()
	if ctx.Err() != nil {
		return githubLocatorResponse{}, safeIOError(ctx, e.CodeTimeout, "CI locator HTTP response canceled")
	}
	if closeErr != nil {
		return githubLocatorResponse{}, e.New(e.CodeFetchFailed, "closing CI locator HTTP response failed")
	}
	if readErr != nil {
		return githubLocatorResponse{}, githubLocatorHTTPError(ctx, readErr, "reading CI locator HTTP response failed")
	}
	if int64(len(raw)) > limit || (response.ContentLength >= 0 && int64(len(raw)) != response.ContentLength) {
		return githubLocatorResponse{}, e.New(e.CodeVerifyFailed, "CI locator HTTP response size is invalid")
	}
	return githubLocatorResponse{status: response.StatusCode, header: response.Header.Clone(), body: raw}, nil
}

func githubLocatorHTTPError(ctx context.Context, cause error, message string) error {
	code := e.CodeUnavailable
	var network net.Error
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || (errors.As(cause, &network) && network.Timeout()) {
		code = e.CodeTimeout
	}
	// The client's own request deadline can expire while the parent is live.
	// Preserve that code without retaining the provider URL or raw cause chain.
	return safeIOError(ctx, code, message)
}

func githubLocatorRetryDelay(response githubLocatorResponse, attempt int, now time.Time) (time.Duration, bool, error) {
	limited := response.status == http.StatusTooManyRequests
	if response.status == http.StatusForbidden {
		var body struct {
			Message string `json:"message"`
		}
		limited = canonical.ValidateJSONDocument(response.body) == nil && json.Unmarshal(response.body, &body) == nil && strings.Contains(strings.ToLower(body.Message), "secondary rate limit")
	}
	if !limited {
		return 0, false, nil
	}
	remaining := response.header.Values("X-RateLimit-Remaining")
	if len(remaining) > 1 || (len(remaining) == 1 && remaining[0] == "0") {
		return 0, true, e.New(e.CodeUnavailable, "CI locator API primary rate limit is exhausted or ambiguous")
	}
	delay := (60 * time.Second) << attempt
	values := response.header.Values("Retry-After")
	if len(values) > 1 {
		return 0, true, e.New(e.CodeUnavailable, "CI locator API returned ambiguous rate-limit delay")
	}
	if len(values) == 1 {
		value := values[0]
		seconds, err := strconv.ParseUint(value, 10, 32)
		if err == nil && strconv.FormatUint(seconds, 10) == value {
			delay = time.Duration(seconds) * time.Second
		} else if deadline, err := http.ParseTime(value); err == nil {
			delay = deadline.Sub(now)
			if delay < 0 {
				delay = 0
			}
		} else {
			return 0, true, e.New(e.CodeUnavailable, "CI locator API returned invalid rate-limit delay")
		}
	}
	// Respect the server's delay, with an increasing minimum for repeated limits.
	// Refuse delays outside the budget instead of retrying earlier than requested.
	minimum := time.Second << attempt
	if delay < minimum {
		delay = minimum
	}
	if delay > 120*time.Second {
		return 0, true, e.New(e.CodeUnavailable, "CI locator API rate-limit delay exceeds retry budget")
	}
	return delay, true, nil
}

func githubLocatorStatusError(ctx context.Context, status int) error {
	if ctx.Err() != nil {
		return safeIOError(ctx, e.CodeTimeout, "CI locator request canceled")
	}
	code := e.CodeFetchFailed
	if status >= 500 || status == http.StatusTooManyRequests {
		code = e.CodeUnavailable
	}
	return e.New(code, "CI locator request returned HTTP %d", status)
}

func githubLocatorDownloadURL(raw string) (*url.URL, error) {
	address, err := url.Parse(raw)
	if err != nil || address.Scheme != "https" || address.Host == "" || address.User != nil || address.Port() != "" ||
		address.Opaque != "" || strings.Contains(raw, "#") || len(raw) > 16384 {
		return nil, e.New(e.CodeVerifyFailed, "CI locator ZIP location is not an admitted HTTPS URL")
	}
	host := strings.ToLower(address.Hostname())
	if host != "results-receiver.actions.githubusercontent.com" &&
		!(len(host) > len(".blob.core.windows.net") && strings.HasSuffix(host, ".blob.core.windows.net") && !strings.Contains(host, "..")) {
		return nil, e.New(e.CodeVerifyFailed, "CI locator ZIP location has an unapproved host")
	}
	return address, nil
}
