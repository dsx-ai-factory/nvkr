// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

func TestRegistryPublicationFailureClassification(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
		codes  []transport.ErrorCode
		want   e.Code
	}{
		{"authentication", 401, nil, e.CodeRegistryAuthFailed},
		{"authorization", 403, nil, e.CodeRegistryAuthFailed},
		{"denied diagnostic", 400, []transport.ErrorCode{transport.DeniedErrorCode}, e.CodeRegistryAuthFailed},
		{"denied plus unsupported", 400, []transport.ErrorCode{transport.UnsupportedErrorCode, transport.DeniedErrorCode}, e.CodeRegistryAuthFailed},
		{"manifest rejected", 400, []transport.ErrorCode{transport.ManifestInvalidErrorCode}, e.CodeRegistryRejected},
		{"missing dependency", 400, []transport.ErrorCode{transport.ManifestBlobUnknownErrorCode}, e.CodeRegistryRejected},
		{"unsupported operation", 400, []transport.ErrorCode{transport.UnsupportedErrorCode}, e.CodeEngineCapability},
		{"ambiguous rejection", 400, []transport.ErrorCode{transport.UnsupportedErrorCode, transport.ManifestInvalidErrorCode}, e.CodeRegistryRejected},
		{"digest mismatch", 400, []transport.ErrorCode{transport.DigestInvalidErrorCode}, e.CodeVerifyFailed},
		{"size mismatch", 400, []transport.ErrorCode{transport.SizeInvalidErrorCode}, e.CodeVerifyFailed},
		{"unknown diagnostic", 400, []transport.ErrorCode{"SYNTHETIC_PRIVATE_VALUE"}, e.CodeRegistryRejected},
		{"unstructured refusal", 400, nil, e.CodeRegistryRejected},
		{"timeout response", 408, nil, e.CodeUnavailable},
		{"throttled", 429, []transport.ErrorCode{transport.TooManyRequestsErrorCode}, e.CodeUnavailable},
		{"server error", 503, nil, e.CodeUnavailable},
		{"base missing", 404, nil, e.CodeInvalidCatalog},
		{"base unsupported", 404, []transport.ErrorCode{transport.UnsupportedErrorCode}, e.CodeEngineCapability},
		{"base denied", 404, []transport.ErrorCode{transport.DeniedErrorCode}, e.CodeRegistryAuthFailed},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			response := &transport.Error{StatusCode: scenario.status}
			if strings.HasPrefix(scenario.name, "base ") {
				response.Request, _ = http.NewRequestWithContext(t.Context(), http.MethodGet, "https://registry.invalid/v2/", nil)
			}
			for _, code := range scenario.codes {
				response.Errors = append(response.Errors, transport.Diagnostic{Code: code,
					Message: "SYNTHETIC_PRIVATE_VALUE unknown media type", Detail: "SYNTHETIC_PRIVATE_VALUE"})
			}
			err := registryPublishError(t.Context(), fmt.Errorf("outer: %w", response), "publishing output %q", "chart")
			assertRegistryCode(t, err, scenario.want)
			if strings.HasPrefix(scenario.name, "base ") {
				ctx, observer := observeRegistry(t.Context())
				observer.report.LastOperation, observer.report.BaseStatus = "api-check", scenario.status
				analyzed := observer.explain(ctx, err)
				assertRegistryCode(t, analyzed, scenario.want)
				wantAction := map[e.Code]string{e.CodeInvalidCatalog: "endpoint and proxy routing", e.CodeEngineCapability: "choose an admitted alternative", e.CodeRegistryAuthFailed: "configured credential"}[scenario.want]
				if !strings.Contains(analyzed.Error(), wantAction) {
					t.Fatalf("endpoint correction overrode the structured refusal: %v", analyzed)
				}
			}
			var retained *transport.Error
			if !errors.As(err, &retained) || retained != response {
				t.Fatal("structured diagnostic cause was lost")
			}
			if strings.Contains(err.Error(), "SYNTHETIC_PRIVATE_VALUE") || strings.Contains(err.Error(), "unknown media type") {
				t.Fatal("untrusted registry content was copied into error presentation")
			}
			if e.Retryable(err) != (scenario.want == e.CodeUnavailable) {
				t.Fatalf("incorrect retry classification: %v", err)
			}
			// Readback owners may have already wrapped the registry response as
			// unavailable. Publication still classifies that structured cause.
			wrapped := e.Wrap(e.CodeUnavailable, response, "readback")
			assertRegistryCode(t, registryReadbackError(t.Context(), wrapped, "reading back output"), scenario.want)
		})
	}
	missing := &transport.Error{StatusCode: http.StatusNotFound}
	assertRegistryCode(t, registryReadbackError(t.Context(), missing, "reading back output"), e.CodeUnavailable)
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded, e.New(e.CodeVerifyFailed, "local integrity"), io.EOF} {
		want := e.CodeTimeout
		if cause == io.EOF {
			want = e.CodeUnavailable
		} else if _, ok := cause.(*e.Error); ok {
			want = e.CodeVerifyFailed
		}
		assertRegistryCode(t, registryPublishError(t.Context(), cause, "publishing output"), want)
	}
}

func TestRegistryPublicationRejectionReachesPublicBoundary(t *testing.T) {
	for _, diagnostic := range []struct {
		code string
		want e.Code
	}{{"MANIFEST_INVALID", e.CodeRegistryRejected}, {"UNSUPPORTED", e.CodeEngineCapability}, {"DENIED", e.CodeRegistryAuthFailed}} {
		t.Run(diagnostic.code, func(t *testing.T) {
			var attempts atomic.Int32
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/") {
					attempts.Add(1)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = fmt.Fprintf(w, `{"errors":[{"code":%q,"message":"unknown media type"}]}`, diagnostic.code)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(server.Close)
			root, expected := releasePublicationFixture(t)
			ref, err := Push(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", RegistryOptions{PlainHTTP: true, Anonymous: true})
			assertRegistryCode(t, err, diagnostic.want)
			var analyzed *RegistryFailure
			if !errors.As(err, &analyzed) || analyzed.Report.API != "distribution-v2" || analyzed.Report.LastOperation != "manifest-upload" ||
				analyzed.Report.OCIDistributionVersion != "unknown" || analyzed.Report.LastStatus != http.StatusBadRequest || analyzed.Action == "" {
				t.Fatalf("public publisher did not analyze the actual protocol failure: %v", err)
			}
			if diagnostic.code == "MANIFEST_INVALID" && !strings.Contains(analyzed.Action, "does not establish which field") {
				t.Fatalf("missing manifest rejection guidance: %s", analyzed.Action)
			}
			if ref != "" || e.Retryable(err) {
				t.Fatalf("permanent refusal returned success or authorized retry: %s %v", ref, err)
			}
			if attempts.Load() != 1 {
				t.Fatalf("permanent refusal attempted another manifest: %d writes", attempts.Load())
			}
			inspection, err := Inspect(t.Context(), root)
			if err != nil || inspection.RootDigest != expected {
				t.Fatalf("rejection changed identified local bytes: %v", err)
			}
		})
	}
}

func TestRegistryMetadataPublicationClassifiesAndRedactsFailures(t *testing.T) {
	for _, stage := range []string{"root", "member"} {
		for _, test := range []struct {
			name, diagnostic string
			status           int
			want             e.Code
		}{
			{"unauthorized", "DENIED", http.StatusUnauthorized, e.CodeRegistryAuthFailed},
			{"forbidden", "DENIED", http.StatusForbidden, e.CodeRegistryAuthFailed},
			{"manifest invalid", "MANIFEST_INVALID", http.StatusBadRequest, e.CodeRegistryRejected},
		} {
			t.Run(stage+"/"+test.name, func(t *testing.T) {
				root, digest := releasePublicationFixture(t)
				metadata := filepath.Join(t.TempDir(), "metadata")
				if _, err := ExportMetadata(t.Context(), root, metadata); err != nil {
					t.Fatal(err)
				}
				var active atomic.Bool
				var refusals atomic.Int32
				handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					isRoot := strings.HasSuffix(r.URL.Path, "/manifests/"+digest)
					if active.Load() && stage == "root" && isRoot && r.Method == http.MethodHead {
						http.NotFound(w, r)
						return
					}
					if active.Load() && strings.Contains(r.URL.Path, "/manifests/") &&
						(stage == "root" && isRoot && r.Method == http.MethodPut || stage == "member" && !isRoot) {
						refusals.Add(1)
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(test.status)
						_, _ = fmt.Fprintf(w, `{"errors":[{"code":%q,"message":"PRIVATE_SENTINEL\u001b[31m\n::error::PRIVATE_SENTINEL","detail":{"value":"PRIVATE_SENTINEL"}}]}`, test.diagnostic)
						return
					}
					handler.ServeHTTP(w, r)
				}))
				defer server.Close()
				repo := strings.TrimPrefix(server.URL, "http://") + "/product/releases"
				options := RegistryOptions{PlainHTTP: true, Anonymous: true}
				if _, err := Push(t.Context(), root, repo, options); err != nil {
					t.Fatal(err)
				}
				active.Store(true)
				ref, err := PushMetadata(t.Context(), metadata, repo, options)
				assertRegistryCode(t, err, test.want)
				var failure *RegistryFailure
				var response *transport.Error
				if ref != "" || e.Retryable(err) || !errors.As(err, &failure) || !errors.As(err, &response) ||
					strings.Contains(err.Error(), "PRIVATE_SENTINEL") || strings.Contains(err.Error(), server.URL) {
					t.Fatalf("unsafe or misclassified metadata publication failure: %s %v", ref, err)
				}
				if refusals.Load() != 1 || failure.Report.LastStatus != test.status {
					t.Fatalf("unexpected retry or status: %d %+v", refusals.Load(), failure.Report)
				}
			})
		}
	}
}

func TestRegistryPublicationDeadlineOwnership(t *testing.T) {
	for _, timeout := range []time.Duration{20 * time.Minute, 30 * time.Minute} {
		for _, bounded := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/bounded=%t", timeout, bounded), func(t *testing.T) {
				parent := t.Context()
				if bounded {
					var cancel context.CancelFunc
					parent, cancel = context.WithTimeout(parent, time.Minute)
					defer cancel()
				}
				before := time.Now()
				child, cancel := registryPublicationContext(parent, timeout)
				deadline, ok := child.Deadline()
				if !ok {
					t.Fatal("publication is unbounded")
				}
				if expected, exists := parent.Deadline(); exists {
					if !deadline.Equal(expected) {
						t.Fatal("caller deadline was changed")
					}
				} else if deadline.Before(before.Add(timeout)) || deadline.After(time.Now().Add(timeout)) {
					t.Fatal("publication did not install its documented default deadline")
				}
				cancel()
				if parent.Err() != nil || !bounded && child.Err() == nil {
					t.Fatal("cleanup canceled the caller or failed to release its own deadline")
				}
			})
		}
	}
}

func TestRegistryMetadataMemberCancellationIsReadOnly(t *testing.T) {
	root, _ := releasePublicationFixture(t)
	inspection, err := Inspect(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	metadata := filepath.Join(t.TempDir(), "metadata")
	if _, err := ExportMetadata(t.Context(), root, metadata); err != nil {
		t.Fatal(err)
	}
	member := inspection.Root.Objects[0]
	var active, reached atomic.Bool
	var writes atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if active.Load() {
			if request.Method == http.MethodPut || request.Method == http.MethodPost || request.Method == http.MethodDelete {
				writes.Add(1)
			}
			if strings.HasSuffix(request.URL.Path, "/manifests/"+member.DescriptorDigest) {
				reached.Store(true)
				cancel()
				<-request.Context().Done()
				return
			}
		}
		handler.ServeHTTP(w, request)
	}))
	defer server.Close()
	repository := strings.TrimPrefix(server.URL, "http://") + "/product/releases"
	options := RegistryOptions{PlainHTTP: true, Anonymous: true}
	if _, err := Push(t.Context(), root, repository, options); err != nil {
		t.Fatal(err)
	}
	active.Store(true)
	ref, err := PushMetadata(ctx, metadata, repository, options)
	assertRegistryCode(t, err, e.CodeTimeout)
	if ref != "" || !reached.Load() || writes.Load() != 0 || !errors.Is(err, context.Canceled) ||
		!strings.Contains(err.Error(), "reading back metadata member \""+member.Name+"\"") ||
		!strings.Contains(err.Error(), "no upload was attempted") || strings.Contains(err.Error(), "partial uploads") {
		t.Fatalf("member cancellation lost its read-only phase or operation: %s %v (writes %d)", ref, err, writes.Load())
	}
}

func TestRegistryReadOnlyFailureDoesNotClaimPartialUploads(t *testing.T) {
	for _, cause := range []error{io.EOF, &transport.Error{StatusCode: http.StatusServiceUnavailable}} {
		t.Run(fmt.Sprintf("%T", cause), func(t *testing.T) {
			ctx, observer := observeRegistry(t.Context())
			observer.readOnly = true
			observer.report.LastOperation = "manifest-readback"
			err := observer.explain(ctx, registryReadbackError(ctx, cause, "reading back metadata member %q", "tools"))
			assertRegistryCode(t, err, e.CodeUnavailable)
			if !strings.Contains(err.Error(), "no upload was attempted") || strings.Contains(err.Error(), "partial uploads") {
				t.Fatalf("read-only failure claimed a write: %v", err)
			}
		})
	}
}

func TestRegistryHTMLBaseKeepsExplicitResponseClassification(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusServiceUnavailable, http.StatusRequestTimeout, http.StatusTooManyRequests, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ctx, observer := observeRegistry(t.Context())
			observer.report.BaseStatus = http.StatusOK
			observer.report.LastOperation = "manifest-readback"
			response := &transport.Error{StatusCode: status}
			err := registryReadbackError(ctx, response, "reading member")
			want := e.CodeUnavailable
			if status == http.StatusUnauthorized {
				want = e.CodeRegistryAuthFailed
			}
			assertRegistryCode(t, err, want)
		})
	}
}
