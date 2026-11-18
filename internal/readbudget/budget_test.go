// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package readbudget

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

func TestOperationReadBudgetIncludesFailedResponsesAndRetries(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ctx, budget, err := WithLimit(t.Context(), 10)
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			base := roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("sixsix"))}, nil
			})
			transport := Transport(ctx, base)
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://synthetic.example.test/object", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || string(raw) != "sixsix" || budget.Used() != 6 {
				t.Fatal("first response", string(raw), err, budget.Used())
			}
			response, err = transport.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			raw, err = io.ReadAll(response.Body)
			_ = response.Body.Close()
			requireBudgetCode(t, err, e.CodeEngineCapability)
			if string(raw) != "sixs" || budget.Used() != 10 {
				t.Fatal("retry escaped shared allowance", string(raw), budget.Used())
			}
			response, err = transport.RoundTrip(request)
			if response != nil {
				_ = response.Body.Close()
			}
			requireBudgetCode(t, err, e.CodeEngineCapability)
			if calls != 2 {
				t.Fatal("exhausted operation performed another request")
			}
			requireBudgetCode(t, budget.Check(), e.CodeEngineCapability)
		})
	}
}

func TestOperationReadBudgetAdmissionAndCompatibility(t *testing.T) {
	for _, limit := range []int64{0, -1} {
		_, _, err := WithLimit(t.Context(), limit)
		requireBudgetCode(t, err, e.CodeUsage)
	}
	_, _, err := WithLimit(nil, 1) //nolint:staticcheck // Exercise explicit nil-context admission.
	requireBudgetCode(t, err, e.CodeUsage)
	ctx, _, err := WithLimit(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = WithLimit(ctx, 2)
	requireBudgetCode(t, err, e.CodeUsage)
	base := &http.Transport{}
	if Transport(t.Context(), base) != base {
		t.Fatal("unbudgeted transport changed")
	}
	_, err = Require(t.Context())
	requireBudgetCode(t, err, e.CodeEngineCapability)
	if budget, err := Require(ctx); err != nil || budget == nil {
		t.Fatal("admitted budget missing", err)
	}
}

type stalledBody struct{ entered, release chan struct{} }

func (reader stalledBody) Read([]byte) (int, error) {
	close(reader.entered)
	<-reader.release
	return 0, io.EOF
}
func (stalledBody) Close() error { return nil }

func TestOperationReadBudgetDoesNotLockAcrossIOAndRefundsReservations(t *testing.T) {
	for _, reservation := range []int{1, 64} {
		t.Run(strconv.Itoa(reservation), func(t *testing.T) {
			ctx, budget, err := WithLimit(t.Context(), 64)
			if err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}), make(chan struct{})
			slow := &body{ReadCloser: stalledBody{entered, release}, budget: budget, ctx: ctx}
			slowDone := make(chan struct{})
			go func() { _, _ = slow.Read(make([]byte, reservation)); close(slowDone) }()
			<-entered
			fastDone := make(chan error, 1)
			go func() {
				fast := &body{ReadCloser: io.NopCloser(strings.NewReader("ok")), budget: budget, ctx: ctx}
				raw, err := io.ReadAll(fast)
				if err == nil && string(raw) != "ok" {
					err = errors.New("fast response changed")
				}
				fastDone <- err
			}()
			if reservation == 64 {
				// All capacity is reserved, not consumed. It must be refunded,
				// rather than producing a sticky exhaustion failure.
				close(release)
			}
			var fastErr error
			timedOut := false
			select {
			case fastErr = <-fastDone:
			case <-time.After(time.Second):
				timedOut = true
				fastErr = errors.New("stalled response blocked independent budgeted read")
			}
			if reservation == 1 {
				close(release)
			}
			<-slowDone
			if timedOut {
				<-fastDone
			}
			if fastErr != nil {
				t.Fatal(fastErr)
			}
			if budget.Check() != nil || budget.Used() != 2 {
				t.Fatal("reservation was counted as consumed bytes", budget.Check(), budget.Used())
			}
		})
	}
}

type closeObservedBody struct{ closed bool }

func (*closeObservedBody) Read([]byte) (int, error) { return 0, io.EOF }
func (reader *closeObservedBody) Close() error      { reader.closed = true; return nil }

func TestOperationReadBudgetStillAttemptsEmptyCleanupAndClosesRefusedRequest(t *testing.T) {
	ctx, budget, err := WithLimit(t.Context(), 1)
	if err != nil {
		t.Fatal(err)
	}
	reader := &body{ReadCloser: io.NopCloser(strings.NewReader("x")), budget: budget, ctx: ctx}
	if n, err := reader.Read(make([]byte, 1)); n != 1 || err != nil {
		t.Fatal(n, err)
	}
	calls := 0
	transport := Transport(ctx, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != http.MethodDelete {
			t.Error("exhausted ordinary request reached transport")
		}
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	}))
	requestBody := &closeObservedBody{}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, "https://synthetic.example.test/object", requestBody)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if response != nil {
		_ = response.Body.Close()
	}
	requireBudgetCode(t, err, e.CodeEngineCapability)
	if !requestBody.closed {
		t.Fatal("refused transport did not close request body")
	}
	request, err = http.NewRequestWithContext(ctx, http.MethodDelete, "https://synthetic.example.test/object?uploadId=synthetic", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err = transport.RoundTrip(request)
	if err != nil {
		t.Fatal("cleanup was suppressed", err)
	}
	raw, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || len(raw) != 0 || calls != 1 || budget.Used() != 1 {
		t.Fatal("cleanup altered read budget", err)
	}
}

func TestOperationReadBudgetCancellationAndConcurrentBodies(t *testing.T) {
	ctx, budget, err := WithLimit(t.Context(), 64)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Go(func() {
			body := &body{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("a", 64))), budget: budget, ctx: ctx}
			_, _ = io.Copy(io.Discard, body)
			_ = body.Close()
		})
	}
	wait.Wait()
	if budget.Used() != 64 {
		t.Fatal("concurrent responses exceeded byte allowance", budget.Used())
	}
	requireBudgetCode(t, budget.Check(), e.CodeEngineCapability)
	ctx, cancel := context.WithCancel(t.Context())
	ctx, budget, err = WithLimit(ctx, 64)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	body := &body{ReadCloser: io.NopCloser(strings.NewReader("not read")), budget: budget, ctx: ctx}
	_, err = io.ReadAll(body)
	requireBudgetCode(t, err, e.CodeTimeout)
	if budget.Used() != 0 {
		t.Fatal("canceled response consumed bytes")
	}
}

func requireBudgetCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}
