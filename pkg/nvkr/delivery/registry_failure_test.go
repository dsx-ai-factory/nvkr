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
	"strings"
	"sync/atomic"
	"testing"

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
	} {
		t.Run(scenario.name, func(t *testing.T) {
			response := &transport.Error{StatusCode: scenario.status}
			for _, code := range scenario.codes {
				response.Errors = append(response.Errors, transport.Diagnostic{Code: code,
					Message: "SYNTHETIC_PRIVATE_VALUE unknown media type", Detail: "SYNTHETIC_PRIVATE_VALUE"})
			}
			err := registryPublishError(t.Context(), fmt.Errorf("outer: %w", response), "publishing output %q", "chart")
			assertRegistryCode(t, err, scenario.want)
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
