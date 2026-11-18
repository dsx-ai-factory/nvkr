// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package readbudget meters response bodies across one publication operation.
// It grants no identity or verification authority. Attach it below retry and
// authentication transports so failed attempts consume the same allowance.
package readbudget

import (
	"context"
	"io"
	"net/http"
	"sync"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type contextKey struct{}

// Budget is operation-local and cannot be restored from a reported byte count.
// Reads reserve capacity before I/O and refund unused capacity afterward.
// Concurrent responses never hold the accounting lock across network I/O.
type Budget struct {
	mu        sync.Mutex
	remaining int64
	inflight  int64
	changed   chan struct{}
	used      int64
	failure   error
}

// Active reports whether this operation requires response accounting, including
// an already exhausted allowance. Exhaustion must never select an unmetered path.
func Active(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(contextKey{}).(*Budget)
	return ok
}

func WithLimit(ctx context.Context, limit int64) (context.Context, *Budget, error) {
	if ctx == nil || limit <= 0 {
		return nil, nil, e.New(e.CodeUsage, "response reads require a context and positive finite byte allowance")
	}
	if _, exists := ctx.Value(contextKey{}).(*Budget); exists {
		return nil, nil, e.New(e.CodeUsage, "response allowance cannot be replaced during an operation")
	}
	budget := &Budget{remaining: limit, changed: make(chan struct{})}
	return context.WithValue(ctx, contextKey{}, budget), budget, nil
}

// Check reports a sticky exhaustion failure. Reaching the limit is not itself
// failure; attempting another response read is. Callers must reserve framing
// headroom because an exhausted stream may not be probed for EOF for free.
func (budget *Budget) Check() error {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.failure
}

func (budget *Budget) Used() int64 {
	budget.mu.Lock()
	defer budget.mu.Unlock()
	return budget.used
}

// Transport retains the operation's counter even when an adapter derives a new
// request context. Without an admitted budget it preserves the existing transport.
func Transport(ctx context.Context, base http.RoundTripper) http.RoundTripper {
	budget, _ := ctx.Value(contextKey{}).(*Budget)
	if budget == nil {
		return base
	}
	return transport{base: base, budget: budget}
}

type transport struct {
	base   http.RoundTripper
	budget *Budget
}

func (transport transport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.budget.mu.Lock()
	err := transport.budget.admitRead()
	transport.budget.mu.Unlock()
	// S3 multipart abort uses DELETE. Always attempt bounded cleanup; a response
	// body still consumes the same allowance and cannot exceed it. An empty
	// successful abort needs no response-byte allowance.
	if err != nil && request.Method != http.MethodDelete {
		if request.Body != nil {
			_ = request.Body.Close()
		}
		return nil, err
	}
	response, err := transport.base.RoundTrip(request)
	if response != nil && response.Body != nil && response.Body != http.NoBody {
		response.Body = &body{ReadCloser: response.Body, budget: transport.budget, ctx: request.Context()}
	}
	return response, err
}

type body struct {
	io.ReadCloser
	budget *Budget
	ctx    context.Context
}

func (body *body) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	reserved, err := body.budget.reserve(body.ctx, int64(len(p)))
	if err != nil {
		return 0, err
	}
	n, err := body.ReadCloser.Read(p[:reserved])
	body.budget.mu.Lock()
	body.budget.inflight -= reserved
	body.budget.remaining += reserved - int64(n)
	body.budget.used += int64(n)
	close(body.budget.changed)
	body.budget.changed = make(chan struct{})
	body.budget.mu.Unlock()
	return n, err
}

func (budget *Budget) admitRead() error {
	if budget.failure == nil && budget.remaining == 0 && budget.inflight == 0 {
		budget.failure = e.New(e.CodeEngineCapability, "operation response-byte allowance exhausted")
	}
	return budget.failure
}

func (budget *Budget) reserve(ctx context.Context, requested int64) (int64, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, e.Wrap(e.CodeTimeout, err, "response read operation ended")
		}
		budget.mu.Lock()
		if err := budget.admitRead(); err != nil {
			budget.mu.Unlock()
			return 0, err
		}
		if budget.remaining != 0 {
			reserved := min(requested, budget.remaining)
			budget.remaining -= reserved
			budget.inflight += reserved
			budget.mu.Unlock()
			return reserved, nil
		}
		// Another read holds the remaining capacity. Wait for its refund instead
		// of declaring exhaustion based on bytes it has not actually consumed.
		changed := budget.changed
		budget.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return 0, e.Wrap(e.CodeTimeout, ctx.Err(), "response reservation canceled")
		}
	}
}

// Require lets selective phase constructors reject missing metering before I/O.
// Existing transports remain compatible with callers that omit a budget.
func Require(ctx context.Context) (*Budget, error) {
	if ctx != nil {
		if budget, ok := ctx.Value(contextKey{}).(*Budget); ok {
			return budget, budget.Check()
		}
	}
	return nil, e.New(e.CodeEngineCapability, "operation requires an admitted response-byte allowance")
}
