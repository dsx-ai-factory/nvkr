// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// BootReaderCredential supplies independently authorized runtime reader access.
// It is never serialized into a binding, inventory, URL or rendered artifact.
type BootReaderCredential struct {
	Ref    string `json:"-" yaml:"-"`
	Bearer string `json:"-" yaml:"-"`
}

func (BootReaderCredential) String() string   { return "BootReaderCredential{redacted}" }
func (BootReaderCredential) GoString() string { return "BootReaderCredential{redacted}" }

type bootServingClient struct {
	mu         sync.Mutex
	closed     bool
	active     sync.WaitGroup
	lifetime   context.Context
	cancel     context.CancelFunc
	config     definition.BootServing
	credential BootReaderCredential
	client     *http.Client
	transport  *http.Transport
}

func newBootServingClient(ctx context.Context, config definition.BootServing, credential BootReaderCredential, timeout time.Duration) (*bootServingClient, error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, err
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if timeout <= 0 || timeout > 3509*time.Second {
		return nil, e.New(e.CodeEngineCapability, "boot serving requires an admitted operation timeout")
	}
	if config.ReaderMode == "anonymous" {
		if credential != (BootReaderCredential{}) {
			return nil, e.New(e.CodeTrustPolicyMissing, "anonymous boot reader cannot receive credentials")
		}
	} else if credential.Ref != config.CredentialRef || credential.Bearer == "" || len(credential.Bearer) > 16384 || strings.ContainsFunc(credential.Bearer, func(c rune) bool { return c <= ' ' || c > '~' }) {
		return nil, e.New(e.CodeTrustPolicyMissing, "boot reader requires its explicit runtime credential reference and bearer value")
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, e.New(e.CodeEngineCapability, "boot serving requires a standard owned HTTPS transport")
	}
	transport := base.Clone()
	transport.DisableCompression = true
	transport.ResponseHeaderTimeout = min(30*time.Second, timeout)
	client := &http.Client{Transport: readbudget.Transport(ctx, transport), Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	lifetime, cancel := context.WithTimeout(ctx, timeout)
	return &bootServingClient{config: config, credential: credential, client: client, transport: transport, lifetime: lifetime, cancel: cancel}, nil
}

// bootServingURL accepts only the exact content-addressed payload key. The
// configured origin and prefix are independent of the S3 API endpoint/prefix.
func bootServingURL(config definition.BootServing, object Object) (string, error) {
	if err := config.Validate(); err != nil {
		return "", err
	}
	if !canonical.IsDigest(object.SHA256) || object.Size <= 0 || object.Size > maxDeliveryFileBytes || object.Key != mirrorBlobKey(object.SHA256) {
		return "", e.New(e.CodeVerifyFailed, "boot serving requires an exact nonempty content-addressed payload")
	}
	return config.Origin + strings.TrimSuffix(config.Prefix, "/") + "/" + object.Key, nil
}

// verify reads the full approved client path independently of pinned S3 reads.
// It reports equality at check time, not permanent routing or firmware trust.
func (reader *bootServingClient) verify(ctx context.Context, object Object) (_ string, resultErr error) {
	ctx, finish, err := reader.begin(ctx)
	if err != nil {
		return "", err
	}
	defer finish()
	location, err := bootServingURL(reader.config, object)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "constructing exact boot-serving request")
	}
	request.Header.Set("Accept-Encoding", "identity")
	if reader.config.ReaderMode == "bearer" {
		request.Header.Set("Authorization", "Bearer "+reader.credential.Bearer)
	}
	response, err := reader.client.Do(request)
	if err != nil {
		return "", bootServingFailure(ctx, err, "boot-serving HTTPS retrieval failed")
	}
	defer func() {
		if err := response.Body.Close(); err != nil {
			resultErr = errors.Join(resultErr, e.New(e.CodeVerifyFailed, "closing boot-serving response failed"))
		}
	}()
	if response.StatusCode >= 500 || response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests {
		return "", e.New(e.CodeUnavailable, "boot-serving endpoint is temporarily unavailable")
	}
	if response.StatusCode != http.StatusOK || response.Header.Get("Content-Range") != "" ||
		(response.Header.Get("Content-Encoding") != "" && response.Header.Get("Content-Encoding") != "identity") ||
		(response.ContentLength >= 0 && response.ContentLength != object.Size) {
		return "", e.New(e.CodeVerifyFailed, "boot-serving response is not the complete admitted raw payload")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(response.Body, object.Size+1))
	if err != nil {
		return "", bootServingFailure(ctx, err, "reading complete boot-serving response failed")
	}
	if n != object.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != object.SHA256 {
		return "", e.New(e.CodeVerifyFailed, "boot-serving response differs from the exact selected raw bytes")
	}
	if err := deliveryContext(ctx); err != nil {
		return "", err
	}
	return location, nil
}

func bootServingFailure(ctx context.Context, err error, operation string) error {
	// Retain the existing transport classification, but drop URLs, underlying
	// provider text and credential-bearing error chains from the public error.
	classified := metadataFetchError(ctx, err, operation)
	var coded *e.Error
	if errors.As(classified, &coded) {
		return e.New(coded.Code, "%s", operation)
	}
	return e.New(e.CodeFetchFailed, "%s", operation)
}

func (reader *bootServingClient) begin(ctx context.Context) (context.Context, func(), error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, nil, err
	}
	reader.mu.Lock()
	if reader.closed || reader.lifetime.Err() != nil {
		reader.mu.Unlock()
		return nil, nil, e.New(e.CodeTimeout, "boot-serving operation lifetime ended")
	}
	reader.active.Add(1)
	reader.mu.Unlock()
	deadline, _ := reader.lifetime.Deadline()
	owned, cancel := context.WithDeadline(ctx, deadline)
	joined := make(chan struct{})
	stop := context.AfterFunc(reader.lifetime, func() { cancel(); close(joined) })
	finish := func() {
		if !stop() {
			<-joined
		}
		cancel()
		reader.active.Done()
	}
	if err := deliveryContext(owned); err != nil {
		finish()
		return nil, nil, err
	}
	return owned, finish, nil
}

func (reader *bootServingClient) close() {
	reader.mu.Lock()
	reader.closed = true
	reader.cancel()
	reader.mu.Unlock()
	reader.active.Wait()
	reader.transport.CloseIdleConnections()
}
