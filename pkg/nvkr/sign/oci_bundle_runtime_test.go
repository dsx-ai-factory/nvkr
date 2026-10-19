// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

type bundleTestTransport func(*http.Request) (*http.Response, error)

func (f bundleTestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type bundleTestBody struct {
	io.Reader
	closed   int
	closeErr error
}

func (b *bundleTestBody) Close() error { b.closed++; return b.closeErr }

func TestOCIBundleReadBudgetChargesInvalidResponses(t *testing.T) {
	for _, contentLength := range []int64{-1, 6} {
		t.Run(fmt.Sprint(contentLength), func(t *testing.T) {
			budget := &ociReadBudget{remaining: 8}
			var bodies []*bundleTestBody
			transport := ociBundleTransport{budget: budget, base: bundleTestTransport(func(*http.Request) (*http.Response, error) {
				// Invalid candidate bytes count even though no parser will admit them.
				body := &bundleTestBody{Reader: strings.NewReader("broken")}
				bodies = append(bodies, body)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: contentLength, Body: body}, nil
			})}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://registry.example/v2/output/manifests/sha256:abc", nil)
			if err != nil {
				t.Fatal(err)
			}
			for i := range 2 {
				response, err := transport.RoundTrip(request)
				if err != nil {
					t.Fatal(err)
				}
				raw, readErr := io.ReadAll(response.Body)
				if err := response.Body.Close(); err != nil {
					t.Fatal(err)
				}
				if i == 0 && (readErr != nil || string(raw) != "broken") {
					t.Fatalf("first read: %q %v", raw, readErr)
				}
				if i == 1 && (readErr == nil || len(raw) > 3) {
					t.Fatalf("budget did not stop second invalid body: %d %v", len(raw), readErr)
				}
			}
			response, err := transport.RoundTrip(request)
			if response != nil {
				_ = response.Body.Close()
			}
			if err == nil || len(bodies) != 2 {
				t.Fatal("exhausted budget started another registry request")
			}
			assertBundleFailureBlocksCache(t, budget)
			for _, body := range bodies {
				if body.closed != 1 {
					t.Fatalf("body close count = %d", body.closed)
				}
			}
		})
	}
}

func TestOCIBundleResponseCloseFailureIsRetained(t *testing.T) {
	failure := errors.New("synthetic body close failure")
	body := &bundleTestBody{Reader: strings.NewReader("{}"), closeErr: failure}
	budget := &ociReadBudget{remaining: 8}
	bounded := &ociBoundedBody{ReadCloser: body, remaining: 4, budget: budget}
	if _, err := io.ReadAll(bounded); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if !errors.Is(bounded.Close(), failure) {
			t.Fatal("close failure was lost")
		}
	}
	if body.closed != 1 || !errors.Is(budget.failure(), failure) {
		t.Fatal("operation lost body cleanup failure")
	}
	assertBundleFailureBlocksCache(t, budget)
}

func TestOCIBundleRefusedResponseRetainsCloseFailure(t *testing.T) {
	for _, pagination := range []bool{false, true} {
		t.Run(fmt.Sprintf("pagination=%t", pagination), func(t *testing.T) {
			failure := errors.New("synthetic refused response close failure")
			body := &bundleTestBody{Reader: strings.NewReader("{}"), closeErr: failure}
			budget := &ociReadBudget{remaining: maxOCIBundleSearchBytes}
			transport := ociBundleTransport{budget: budget, base: bundleTestTransport(func(*http.Request) (*http.Response, error) {
				header := make(http.Header)
				length := maxOCIReferrersBytes + 1
				if pagination {
					header.Set("Link", "</v2/output/referrers/next>; rel=next")
					length = 2
				}
				return &http.Response{StatusCode: http.StatusOK, Header: header, ContentLength: length, Body: body}, nil
			})}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://registry.example/v2/output/referrers/sha256:abc", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(request)
			if response != nil {
				_ = response.Body.Close()
			}
			if !errors.Is(err, failure) || body.closed != 1 {
				t.Fatalf("refusal lost cleanup failure: %v, closes=%d", err, body.closed)
			}
			assertBundleFailureBlocksCache(t, budget)
		})
	}
}

func assertBundleFailureBlocksCache(t *testing.T, budget *ociReadBudget) {
	t.Helper()
	descriptor := v1.Descriptor{Digest: v1.Hash{Algorithm: "sha256", Hex: strings.Repeat("1", 64)}}
	source := &ociBundleSource{check: budget.failure, cache: map[string]ociBundleResult{descriptor.Digest.String(): {raw: []byte(`{"cached":true}`)}}}
	raw, err := source.bundle(t.Context(), descriptor)
	requireSignCode(t, err, e.CodeSignatureInvalid)
	if raw != nil {
		t.Fatal("operation failure returned an earlier cached bundle")
	}
}

func TestOCIBundleLateCancellationCannotReturnSuccess(t *testing.T) {
	for _, tc := range []struct {
		name           string
		exact, version bool
	}{
		{name: "signature crypto"}, {name: "exact predicate crypto", exact: true}, {name: "version introspection", version: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			typ := CosignSignaturePredicateType
			if tc.exact {
				typ = QualificationPredicateType
			}
			stub := &cosignStub{stdout: map[string]string{"download": qualificationBundle(t, ociTestDigest, typ, ociObservedIdentity, ociTestIssuer, []byte(ociTestPredicate)), "version": cosignVersionJSON}}
			stub.onCall = func(call recordedCall) error {
				if (tc.version && call.args[0] == "version") || (!tc.version && call.args[0] == "verify-blob-attestation") {
					cancel()
				}
				return nil
			}
			signer := newStubbedSigner(t, stub, Credential{})
			if tc.exact {
				requireSignCode(t, signer.VerifyQualification(ctx, ociSubject(), []byte(ociTestPredicate)), e.CodeTimeout)
				return
			}
			record, err := signer.Verify(ctx, ociSubject(), VerifyPolicy{})
			requireSignCode(t, err, e.CodeTimeout)
			if record.SignatureVerified || len(record.Ran) != 0 {
				t.Fatal("canceled operation returned partial success")
			}
		})
	}
}
