// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

// This is the only file in NVKR that imports the cloud SDK.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/resultspublish"
)

const (
	// gcsAttempts bounds one object operation; only transient failures retry.
	gcsAttempts = 4
	// gcsRetryBase doubles after each transient failure.
	gcsRetryBase = 2 * time.Second
	// gcsOperationTimeout bounds one attempt; every object is small.
	gcsOperationTimeout = time.Minute
	// tokenTimeout bounds one token request, as the auth library's own client does.
	tokenTimeout = 30 * time.Second
)

// A failed token fetch carries the status of its own last request, or one of these.
const (
	noTokenRequest  = -1
	noTokenResponse = 0
)

// gcsStore writes the results bucket with the job's Application Default
// Credentials. Creates carry a does-not-exist precondition; reads are bounded.
// Errors are fixed text naming relative object paths only, never the SDK's own
// text, which can name the bucket.
type gcsStore struct {
	create func(ctx context.Context, name string, data []byte) error
	read   func(ctx context.Context, name string) (io.ReadCloser, error)
	wait   func(context.Context, time.Duration) error
}

func openGCSStore(ctx context.Context, bucket string) (resultspublish.ObjectStore, func() error, error) {
	client, err := newGCSClient(ctx)
	if err != nil {
		return nil, nil, err
	}
	return newGCSStore(client.Bucket(bucket)), client.Close, nil
}

// newGCSClient opens a client with the job's Application Default Credentials, the
// external-account file the auth step writes. Their token provider is wrapped so a
// failed token fetch carries the status of its own last request: the auth library
// reports a refused or failed federated exchange only as text, which can also name
// the identity pool. On a route whose token requests bypass the recording client,
// such as a metadata server, a failed fetch reports no usable credentials.
func newGCSClient(ctx context.Context, options ...option.ClientOption) (*storage.Client, error) {
	creds, err := credentials.DetectDefault(&credentials.DetectOptions{
		Scopes: []string{storage.ScopeReadWrite},
		Client: &http.Client{Transport: statusTransport{base: http.DefaultTransport}, Timeout: tokenTimeout},
	})
	if err != nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "the publisher has no usable credentials")
	}
	creds.TokenProvider = statusTokens{tokens: creds.TokenProvider}
	client, err := storage.NewClient(ctx, append([]option.ClientOption{option.WithAuthCredentials(creds)}, options...)...)
	if err != nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "the publisher has no usable credentials")
	}
	return client, nil
}

func newGCSStore(handle *storage.BucketHandle) *gcsStore {
	// The store retries each operation itself, so the SDK retries none. The SDK still
	// reopens a read whose body breaks off, within the same attempt and its timeout.
	object := func(name string) *storage.ObjectHandle {
		return handle.Object(name).Retryer(storage.WithPolicy(storage.RetryNever))
	}
	return &gcsStore{
		create: func(ctx context.Context, name string, data []byte) error {
			writer := object(name).If(storage.Conditions{DoesNotExist: true}).NewWriter(ctx)
			writer.ContentType = contentType(name)
			writer.ChunkSize = 0 // one request: every object is small
			if _, err := writer.Write(data); err != nil {
				_ = writer.Close() // it reports the same failure
				return err
			}
			return writer.Close()
		},
		read: func(ctx context.Context, name string) (io.ReadCloser, error) {
			return object(name).NewReader(ctx)
		},
		wait: waitRetry,
	}
}

func (s *gcsStore) Create(ctx context.Context, name string, data []byte) error {
	return s.retry(ctx, "writing", name, func(ctx context.Context) error { return s.create(ctx, name, data) })
}

func (s *gcsStore) Read(ctx context.Context, name string, maxBytes int64) ([]byte, error) {
	var data []byte
	err := s.retry(ctx, "reading", name, func(ctx context.Context) error {
		reader, err := s.read(ctx, name)
		if err != nil {
			return err
		}
		defer func() { _ = reader.Close() }()
		data, err = io.ReadAll(io.LimitReader(reader, maxBytes+1))
		if err == nil && int64(len(data)) > maxBytes {
			return e.New(e.CodeImmutableViolation, "%s exceeds %d bytes", name, maxBytes)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return data, nil
}

// retry runs one bounded attempt at a time until it succeeds, fails for good, or
// runs out of attempts.
func (s *gcsStore) retry(ctx context.Context, operation, name string, attempt func(context.Context) error) error {
	for n := 1; ; n++ {
		attemptCtx, cancel := context.WithTimeout(ctx, gcsOperationTimeout)
		err := attempt(attemptCtx)
		cancel()
		if err == nil {
			return nil
		}
		transient, failure := classify(ctx, err, operation, name)
		if !transient {
			return failure
		}
		if n == gcsAttempts {
			return e.New(e.CodeUnavailable, "%s %s failed %d times", operation, name, gcsAttempts)
		}
		if s.wait(ctx, gcsRetryBase<<(n-1)) != nil {
			return e.New(e.CodeTimeout, "%s %s was interrupted", operation, name)
		}
	}
}

// classify says whether another attempt could succeed, and maps one attempt's
// error to a fixed error.
func classify(ctx context.Context, err error, operation, name string) (bool, error) {
	var coded *e.Error
	var api *googleapi.Error
	var token tokenError
	switch {
	case errors.As(err, &coded):
		return false, err
	case ctx.Err() != nil:
		return false, e.New(e.CodeTimeout, "%s %s was interrupted", operation, name)
	case errors.As(err, &token):
		return token.classify(operation, name)
	case errors.As(err, &api) && api.Code == http.StatusPreconditionFailed:
		return false, resultspublish.ErrExists
	case errors.As(err, &api) && (api.Code == http.StatusUnauthorized || api.Code == http.StatusForbidden):
		return false, e.New(e.CodeTrustPolicyMissing, "the publisher identity was refused while %s %s", operation, name)
	case errors.Is(err, storage.ErrBucketNotExist), operation == "writing" && errors.As(err, &api) && api.Code == http.StatusNotFound:
		return false, e.New(e.CodeTrustPolicyMissing, "the results bucket is unavailable")
	case errors.Is(err, storage.ErrObjectNotExist):
		return false, e.New(e.CodeUnavailable, "%s %s found no object", operation, name)
	case errors.As(err, &api) && transientStatus(api.Code), errors.Is(err, context.DeadlineExceeded), storage.ShouldRetry(err):
		return true, e.New(e.CodeUnavailable, "%s %s failed", operation, name)
	}
	return false, e.New(e.CodeUnavailable, "%s %s failed", operation, name)
}

// transientStatus is a response another attempt could clear: a timeout,
// throttling or a server error.
func transientStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

// fetchStatusKey carries a token fetch's status into the context of its requests.
type fetchStatusKey struct{}

// fetchStatus is the status of a token fetch's last request. A fetch makes its
// requests one after another, in the goroutine that waits for it.
type fetchStatus struct{ status int }

// statusTransport records each token request's status in the fetch whose context
// the request carries. A request outside any fetch, such as a background refresh
// or lookup the auth library starts itself, records nothing.
type statusTransport struct{ base http.RoundTripper }

func (t statusTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if fetch, ok := request.Context().Value(fetchStatusKey{}).(*fetchStatus); ok {
		fetch.status = noTokenResponse
		if err == nil {
			fetch.status = response.StatusCode
		}
	}
	return response, err
}

// statusTokens reports a failed token fetch as a tokenError carrying the status of
// that fetch's own last request.
type statusTokens struct{ tokens auth.TokenProvider }

func (s statusTokens) Token(ctx context.Context) (*auth.Token, error) {
	fetch := &fetchStatus{status: noTokenRequest}
	token, err := s.tokens.Token(context.WithValue(ctx, fetchStatusKey{}, fetch))
	if err != nil {
		return nil, tokenError{status: fetch.status}
	}
	return token, nil
}

// tokenError is a failed token fetch. Its text is fixed: the library's names the
// identity pool or carries a response body.
type tokenError struct{ status int }

func (t tokenError) Error() string { return "fetching an access token failed" }

// classify retries a token request that got no response, timed out, was throttled
// or hit a server error; any other refusal is the identity's.
func (t tokenError) classify(operation, name string) (bool, error) {
	switch {
	case t.status == noTokenResponse || transientStatus(t.status):
		return true, e.New(e.CodeUnavailable, "%s %s failed", operation, name)
	case t.status == noTokenRequest:
		return false, e.New(e.CodeTrustPolicyMissing, "the publisher has no usable credentials")
	case t.status >= http.StatusBadRequest:
		return false, e.New(e.CodeTrustPolicyMissing, "the publisher identity was refused while %s %s", operation, name)
	}
	return false, e.New(e.CodeUnavailable, "%s %s failed", operation, name)
}

func contentType(name string) string {
	switch path.Ext(name) {
	case ".json":
		return "application/json"
	case ".xml":
		return "application/xml"
	}
	return "application/octet-stream"
}

func waitRetry(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
