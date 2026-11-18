// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/theupdateframework/go-tuf/v2/metadata"
)

const maxTrustDocumentBytes int64 = 8 << 20

// AdmitBoundedVerification checks the selected keyless executable capability
// before a publication phase opens its session or acquires trust/artifact bytes.
func (s *CosignOCISigner) AdmitBoundedVerification() error {
	if s == nil || s.runner == nil || s.keyTrust != nil {
		return e.New(e.CodeEngineCapability, "bounded boot verification requires the pinned keyless verifier")
	}
	return s.admit()
}

type boundedTrustKey struct{}

type boundedTrust struct {
	ctx     context.Context
	budget  *readbudget.Budget
	raw     []byte
	mu      sync.Mutex
	closed  bool
	pending int
	active  sync.WaitGroup
}

// WithBoundedVerification authenticates the public-good Sigstore trust target
// using the same TUF bootstrap and client version as cosign 3.1.3. Its network
// reads share the caller's phase allowance. Verification consumes a local
// snapshot, so cosign cannot start an unmetered TUF refresh. No caller-provided
// root, ambient cache, alternate mirror or weaker transparency policy is used.
// The snapshot is usable only in the callback and grants no artifact authority.
func WithBoundedVerification(ctx context.Context, work func(context.Context) error) (resultErr error) {
	if work == nil {
		return e.New(e.CodeUsage, "bounded verification requires a consumer")
	}
	if err := boundedVerificationFailure(ctx); err != nil {
		return err
	}
	budget, err := readbudget.Require(ctx)
	if err != nil {
		return err
	}
	if _, ok := ctx.Deadline(); !ok {
		return e.New(e.CodeEngineCapability, "bounded verification requires an operation deadline")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	standard, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return e.New(e.CodeEngineCapability, "bounded trust requires an owned standard HTTPS transport")
	}
	base := standard.Clone()
	base.ResponseHeaderTimeout = 30 * time.Second
	defer base.CloseIdleConnections()
	client := &http.Client{Transport: readbudget.Transport(ctx, base), CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	opts := tuf.DefaultOptions().WithContext(ctx).WithFetcher(&boundedTrustFetcher{ctx: ctx, client: client})
	raw, err := loadBoundedTrust(ctx, budget, opts)
	if err != nil {
		return err
	}
	return withBoundedTrustSnapshot(ctx, budget, raw, work)
}

// The only production caller supplies the public-good default bootstrap. Tests
// exercise TUF rotation and cache isolation with a signed synthetic repository.
func loadBoundedTrust(ctx context.Context, budget *readbudget.Budget, opts *tuf.Options) (result []byte, resultErr error) {
	cache, err := os.MkdirTemp("", "nvkr-bounded-tuf-*")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "creating private trust metadata workspace")
	}
	defer func() {
		if err := os.RemoveAll(cache); err != nil {
			resultErr = errors.Join(resultErr, e.Wrap(e.CodeImmutableViolation, err, "cleaning private trust metadata workspace"))
			result = nil
		}
	}()
	// DisableLocalCache suppresses writes, but go-tuf still tries metadata reads
	// from its configured directory. Always give it a fresh private directory.
	local := *opts
	local.WithContext(ctx).WithCachePath(cache).WithDisableLocalCache()
	trusted, err := tuf.New(&local)
	if err != nil {
		if failure := boundedVerificationFailure(ctx); failure != nil {
			return nil, failure
		}
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "authenticating bounded Sigstore trust metadata")
	}
	raw, err := trusted.GetTarget("trusted_root.json")
	if err != nil {
		if failure := boundedVerificationFailure(ctx); failure != nil {
			return nil, failure
		}
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "authenticating bounded Sigstore trusted root")
	}
	if len(raw) == 0 || int64(len(raw)) > maxTrustDocumentBytes {
		return nil, e.New(e.CodeTrustPolicyMissing, "Sigstore trusted root exceeds its document limit")
	}
	if err := budget.Check(); err != nil {
		return nil, err
	}
	return raw, nil
}

func withBoundedTrustSnapshot(ctx context.Context, budget *readbudget.Budget, raw []byte, work func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	owned := &boundedTrust{ctx: ctx, budget: budget, raw: bytes.Clone(raw)}
	err := work(context.WithValue(ctx, boundedTrustKey{}, owned))
	contextErr := contextError(ctx)
	owned.mu.Lock()
	owned.closed = true
	if owned.pending != 0 {
		err = errors.Join(err, e.New(e.CodeGateUnmet, "trust consumer returned with unfinished verification"))
	}
	owned.mu.Unlock()
	cancel()
	owned.active.Wait()
	return errors.Join(err, contextErr, budget.Check())
}

// The TUF fetcher interface omits context. Retain the admitted phase context
// explicitly so every request, failed response and retry uses the same bounds.
type boundedTrustFetcher struct {
	ctx    context.Context
	client *http.Client
}

func (f *boundedTrustFetcher) DownloadFile(location string, maxLength int64, _ time.Duration) (_ []byte, resultErr error) {
	u, err := url.Parse(location)
	if err != nil || u.Scheme != "https" || u.Host != "tuf-repo-cdn.sigstore.dev" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || maxLength <= 0 {
		return nil, e.New(e.CodeTrustPolicyMissing, "trust metadata fetch is outside the admitted public-good repository")
	}
	request, err := http.NewRequestWithContext(f.ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "constructing bounded trust request")
	}
	response, err := f.client.Do(request)
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "fetching bounded trust metadata")
	}
	defer func() { resultErr = errors.Join(resultErr, response.Body.Close()) }()
	limit := min(maxLength, maxTrustDocumentBytes)
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, &metadata.ErrDownloadLengthMismatch{Msg: "trust metadata exceeds its admitted document limit"}
	}
	if response.StatusCode != http.StatusOK {
		// TUF uses typed HTTP 404 to stop authenticated root rotation.
		return nil, &metadata.ErrDownloadHTTP{StatusCode: response.StatusCode, URL: location}
	}
	return raw, nil
}

func admitBoundedTrust(ctx context.Context) (*boundedTrust, error) {
	if err := boundedVerificationFailure(ctx); err != nil {
		return nil, err
	}
	if !readbudget.Active(ctx) {
		return nil, nil
	}
	budget, err := readbudget.Require(ctx)
	if err != nil {
		return nil, err
	}
	owned, _ := ctx.Value(boundedTrustKey{}).(*boundedTrust)
	if owned == nil || owned.budget != budget || len(owned.raw) == 0 || owned.ctx.Err() != nil {
		return nil, e.New(e.CodeEngineCapability, "metered keyless verification requires its live authenticated trust snapshot")
	}
	deadline, bounded := ctx.Deadline()
	ownerDeadline, ownerBounded := owned.ctx.Deadline()
	if !bounded || !ownerBounded || deadline.After(ownerDeadline) {
		return nil, e.New(e.CodeEngineCapability, "verification context removed its operation deadline")
	}
	owned.mu.Lock()
	closed := owned.closed
	owned.mu.Unlock()
	if closed {
		return nil, e.New(e.CodeGateUnmet, "trust snapshot is closed")
	}
	return owned, contextError(ctx)
}

func boundedVerificationFailure(ctx context.Context) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if readbudget.Active(ctx) {
		_, err := readbudget.Require(ctx)
		return err
	}
	return nil
}

func beginBoundedVerification(ctx context.Context) (func(), error) {
	owned, err := admitBoundedTrust(ctx)
	if err != nil {
		return nil, err
	}
	if owned == nil {
		return func() {}, nil
	}
	owned.mu.Lock()
	if owned.closed {
		owned.mu.Unlock()
		return nil, e.New(e.CodeGateUnmet, "trust snapshot is closed")
	}
	owned.pending++
	owned.active.Add(1)
	owned.mu.Unlock()
	return func() {
		owned.mu.Lock()
		owned.pending--
		owned.mu.Unlock()
		owned.active.Done()
	}, nil
}

// Verification records use a stable placeholder for private per-invocation
// paths while retaining every trust/format argument actually executed.
func boundedVerificationRecordedArgs(ctx context.Context, args []string) []string {
	if !readbudget.Active(ctx) {
		return args
	}
	return append(append([]string(nil), args...), "--new-bundle-format=true", "--trusted-root", "${NVKR_TRUSTED_ROOT}")
}

func boundedVerificationCandidateFailure(ctx context.Context, err error) error {
	if failure := boundedVerificationFailure(ctx); failure != nil {
		return failure
	}
	if readbudget.Active(ctx) {
		if _, failure := admitBoundedTrust(ctx); failure != nil {
			return failure
		}
		var coded *e.Error
		if errors.As(err, &coded) && coded.Code != e.CodeSignatureInvalid {
			return err
		}
	}
	return nil
}

// withBoundedBundleVerification keeps the exact root file private for one
// subprocess and checks it again before releasing verification success.
func withBoundedBundleVerification(ctx context.Context, args []string, run func([]string) error) (resultErr error) {
	owned, err := admitBoundedTrust(ctx)
	if err != nil {
		return err
	}
	if owned == nil {
		return run(args)
	}
	if len(args) == 0 || args[0] != "verify-blob-attestation" || slices.Contains(args, "--key") || slices.Contains(args, "--trusted-root") {
		return e.New(e.CodeEngineCapability, "metered verification admits only original keyless bundles")
	}
	finish, err := beginBoundedVerification(ctx)
	if err != nil {
		return err
	}
	defer finish()
	workspace, err := os.MkdirTemp("", "nvkr-bounded-trust-*")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating bounded trust workspace")
	}
	defer func() {
		if err := os.RemoveAll(workspace); err != nil {
			resultErr = errors.Join(resultErr, e.Wrap(e.CodeImmutableViolation, err, "cleaning private trusted root"))
		}
	}()
	path := filepath.Join(workspace, "trusted-root.json")
	if err := writePrivateFile(ctx, path, owned.raw); err != nil {
		return err
	}
	args = append(append([]string(nil), args...), "--new-bundle-format=true", "--trusted-root", path)
	err = run(args)
	rootErr := assertPrivateFileUnchanged(path, owned.raw)
	if rootErr != nil {
		rootErr = e.Wrap(e.CodeImmutableViolation, rootErr, "trusted root changed during verification")
	}
	return errors.Join(contextError(ctx), owned.budget.Check(), rootErr, err)
}
