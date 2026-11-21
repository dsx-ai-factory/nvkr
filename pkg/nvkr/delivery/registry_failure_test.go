// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
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
	}{{"MANIFEST_INVALID", e.CodeRegistryRejected}, {"UNSUPPORTED", e.CodeEngineCapability}, {"DENIED", e.CodeRegistryAuthFailed}, {"DIGEST_INVALID", e.CodeVerifyFailed}, {"SIZE_INVALID", e.CodeVerifyFailed}} {
		t.Run(diagnostic.code, func(t *testing.T) {
			var attempts, blobWrites atomic.Int32
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPatch && strings.Contains(r.URL.Path, "/blobs/uploads/") {
					blobWrites.Add(1)
				}
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
			safe := SafeRegistryPublishError(t.Context(), err)
			assertRegistryCode(t, safe, diagnostic.want)
			if !strings.Contains(safe.Error(), "destination may contain partial uploads") || !errors.Is(safe, err) || blobWrites.Load() == 0 {
				t.Fatalf("actual write refusal lost publication uncertainty or cause: blob writes=%d err=%v", blobWrites.Load(), safe)
			}
			if diagnostic.want == e.CodeVerifyFailed && !strings.Contains(safe.Error(), "expected digest") {
				t.Fatalf("integrity refusal lost fixed guidance: %v", safe)
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
				safe := SafeRegistryPublishError(t.Context(), err)
				if strings.Contains(safe.Error(), "partial uploads") != (stage == "root") {
					t.Fatalf("metadata warning did not follow its own write phase: %v", safe)
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

func TestRegistryMetadataReadAfterWriteKeepsPublicationUncertainty(t *testing.T) {
	for _, cause := range []error{io.ErrUnexpectedEOF, context.Canceled} {
		t.Run(cause.Error(), func(t *testing.T) {
			progress := &registryPublicationProgress{}
			progress.writeAttempted.Store(true)
			ctx := context.WithValue(t.Context(), registryPublicationProgressKey{}, progress)
			ctx, observer := observeRegistry(ctx)
			observer.readOnly = true // A nested metadata member read follows evidence writes.
			observer.report.LastOperation = "manifest-readback"
			err := uncertainRegistryPublication(observer.explain(ctx, registryReadbackError(ctx, cause, "reading metadata member")))
			var uncertain registryPublicationUncertain
			if strings.Contains(err.Error(), "no upload was attempted") || !errors.As(err, &uncertain) || !strings.Contains(SafeRegistryPublishError(ctx, err).Error(), "partial uploads") {
				t.Fatalf("post-write metadata failure lost publication uncertainty: %v", err)
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

func TestSafeRegistryErrorsKeepLocalGuidanceAndRedactRemoteDetails(t *testing.T) {
	for _, boundary := range []struct {
		name string
		wrap func(context.Context, error) error
	}{{"publish", SafeRegistryPublishError}, {"readback", SafeRegistryReadbackError}} {
		for _, failure := range []struct {
			code     e.Code
			guidance string
		}{{e.CodeUsage, "publication settings"}, {e.CodeVerifyFailed, "expected digest"}, {e.CodeTrustPolicyMissing, "admitted scope"}} {
			t.Run(boundary.name+"/"+string(failure.code), func(t *testing.T) {
				original := e.New(failure.code, "PRIVATE_SENTINEL caller value")
				got := boundary.wrap(t.Context(), original)
				assertRegistryCode(t, got, failure.code)
				if !errors.Is(got, original) || !strings.Contains(got.Error(), failure.guidance) || strings.Contains(got.Error(), "PRIVATE_SENTINEL") || strings.Contains(got.Error(), "partial uploads") {
					t.Fatalf("lost local guidance or redaction: %v", got)
				}
			})
		}
		t.Run(boundary.name+"/remote", func(t *testing.T) {
			original := &transport.Error{StatusCode: http.StatusForbidden, Errors: []transport.Diagnostic{{Code: transport.DeniedErrorCode, Message: "PRIVATE_SENTINEL", Detail: "PRIVATE_SENTINEL"}}}
			got := boundary.wrap(t.Context(), original)
			assertRegistryCode(t, got, e.CodeRegistryAuthFailed)
			var retained *transport.Error
			if !errors.As(got, &retained) || retained != original || strings.Contains(got.Error(), "PRIVATE_SENTINEL") {
				t.Fatalf("remote detail was disclosed or cause was lost: %v", got)
			}
		})
	}
}

func TestSafeRegistryErrorsKeepWrappedCategoryGuidance(t *testing.T) {
	for _, boundary := range []struct {
		name string
		wrap func(context.Context, error) error
	}{{"publish", SafeRegistryPublishError}, {"readback", SafeRegistryReadbackError}} {
		for _, failure := range []struct {
			code     e.Code
			guidance string
		}{{e.CodeUsage, "publication settings"}, {e.CodeVerifyFailed, "expected digest"}, {e.CodeTrustPolicyMissing, "admitted scope"}} {
			t.Run(boundary.name+"/"+string(failure.code), func(t *testing.T) {
				cause := &os.PathError{Op: "open", Path: "/PRIVATE_SENTINEL", Err: os.ErrPermission}
				original := e.Wrap(failure.code, cause, "PRIVATE_SENTINEL caller message")
				got := boundary.wrap(t.Context(), original)
				assertRegistryCode(t, got, failure.code)
				var retained *os.PathError
				if !errors.Is(got, original) || !errors.As(got, &retained) || retained != cause ||
					!strings.Contains(got.Error(), failure.guidance) || strings.Contains(got.Error(), "PRIVATE_SENTINEL") || strings.Contains(got.Error(), "partial uploads") {
					t.Fatalf("lost wrapped guidance, cause or privacy: %v", got)
				}
			})
		}
	}
}

func TestSafeRegistryMissingLayoutFailsBeforeNetwork(t *testing.T) {
	for _, state := range []string{"missing layout", "index is directory"} {
		t.Run(state, func(t *testing.T) {
			var connections, requests atomic.Int64
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateNew {
					connections.Add(1)
				}
			}
			server.Start()
			defer server.Close()
			layout := filepath.Join(t.TempDir(), "PRIVATE_LAYOUT_SENTINEL")
			if state == "index is directory" {
				if err := os.MkdirAll(filepath.Join(layout, "index.json"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			ref, original := PushWithOptions(t.Context(), layout, strings.TrimPrefix(server.URL, "http://")+"/release", PushOptions{Registry: RegistryOptions{Anonymous: true, PlainHTTP: true}})
			assertRegistryCode(t, original, e.CodeVerifyFailed)
			var coded *e.Error
			if ref != "" || !errors.As(original, &coded) || coded.Err == nil || !strings.Contains(original.Error(), "PRIVATE_LAYOUT_SENTINEL") {
				t.Fatalf("fixture did not exercise a wrapped local layout refusal: ref=%q error=%v", ref, original)
			}
			for _, wrap := range []func(context.Context, error) error{SafeRegistryPublishError, SafeRegistryReadbackError} {
				got := wrap(t.Context(), original)
				assertRegistryCode(t, got, e.CodeVerifyFailed)
				if !errors.Is(got, original) || !strings.Contains(got.Error(), "expected digest") || strings.Contains(got.Error(), "PRIVATE_LAYOUT_SENTINEL") ||
					strings.Contains(got.Error(), server.URL) || strings.Contains(got.Error(), "partial uploads") {
					t.Fatalf("actual local refusal lost guidance, cause or privacy: %v", got)
				}
				if state == "missing layout" && !errors.Is(got, os.ErrNotExist) {
					t.Fatal("missing-layout filesystem cause was lost")
				}
			}
			if connections.Load() != 0 || requests.Load() != 0 {
				t.Fatalf("local layout refusal reached the registry: connections=%d requests=%d", connections.Load(), requests.Load())
			}
		})
	}
}

func TestSafeRegistryErrorsDoNotInferWritePhaseFromCause(t *testing.T) {
	for _, boundary := range []struct {
		name, message string
		wrap          func(context.Context, error) error
	}{{"publish", "registry publication failed", SafeRegistryPublishError}, {"readback", "registry readback failed", SafeRegistryReadbackError}} {
		for _, failure := range []struct {
			name  string
			cause error
			code  e.Code
		}{
			{"authorization", &transport.Error{StatusCode: http.StatusForbidden, Errors: []transport.Diagnostic{{Code: transport.DeniedErrorCode, Message: "PRIVATE_SENTINEL", Detail: "PRIVATE_SENTINEL"}}}, e.CodeRegistryAuthFailed},
			{"availability", &transport.Error{StatusCode: http.StatusServiceUnavailable, Errors: []transport.Diagnostic{{Code: transport.UnavailableErrorCode, Message: "PRIVATE_SENTINEL"}}}, e.CodeUnavailable},
			{"unavailable without cause", e.New(e.CodeUnavailable, "PRIVATE_SENTINEL"), e.CodeUnavailable},
		} {
			t.Run(boundary.name+"/"+failure.name, func(t *testing.T) {
				got := boundary.wrap(t.Context(), failure.cause)
				assertRegistryCode(t, got, failure.code)
				if !errors.Is(got, failure.cause) || !strings.Contains(got.Error(), boundary.message) ||
					strings.Contains(got.Error(), "PRIVATE_SENTINEL") || strings.Contains(got.Error(), "partial uploads") {
					t.Fatalf("remote uncertainty or privacy changed: %v", got)
				}
			})
		}
	}
}

func TestSafeRegistryIntegrityUncertaintyIsIndependentOfCode(t *testing.T) {
	for _, code := range []e.Code{e.CodeVerifyFailed, e.CodeUnverifiedRef} {
		for _, uploaded := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/uploaded=%v", code, uploaded), func(t *testing.T) {
				cause := &os.PathError{Op: "read", Path: "/PRIVATE_SENTINEL", Err: os.ErrPermission}
				original := e.Wrap(code, cause, "PRIVATE_SENTINEL")
				var input error = original
				if uploaded {
					input = uncertainRegistryPublication(input)
				}
				for _, wrap := range []func(context.Context, error) error{SafeRegistryPublishError, SafeRegistryReadbackError} {
					got := wrap(t.Context(), input)
					assertRegistryCode(t, got, code)
					if !errors.Is(got, cause) || strings.Contains(got.Error(), "PRIVATE_SENTINEL") || !strings.Contains(got.Error(), "expected digest") || strings.Contains(got.Error(), "partial uploads") != uploaded {
						t.Fatalf("phase, category, cause or privacy lost: %v", got)
					}
				}
			})
		}
	}
}

func TestSafeRegistryLocalIntegrityAfterUploadKeepsUncertainty(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrupt=%v", corrupt), func(t *testing.T) {
			root, expected := releasePublicationFixture(t)
			inspection, err := Inspect(t.Context(), root)
			if err != nil {
				t.Fatal(err)
			}
			var output, evidence PublishedObject
			for _, object := range inspection.Root.Objects {
				if object.Output != nil {
					output = object
				}
				if object.Evidence != nil {
					evidence = object
				}
			}
			if output.DescriptorDigest == "" || evidence.DescriptorDigest == "" {
				t.Fatal("fixture requires an output followed by evidence")
			}
			var writes atomic.Int64
			var changed atomic.Bool
			mutationErrors := make(chan error, 1)
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut && r.URL.Path == "/v2/candidate/tools/manifests/"+output.DescriptorDigest {
					writes.Add(1)
					if corrupt && changed.CompareAndSwap(false, true) {
						path := fixtureBlobPath(root, evidence.DescriptorDigest)
						raw, mutationErr := os.ReadFile(path)
						if mutationErr == nil {
							raw[len(raw)/2] ^= 1
							mutationErr = os.WriteFile(path, raw, 0o600)
						}
						if mutationErr != nil {
							mutationErrors <- mutationErr
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			ref, original := PushWithOptions(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/candidate/releases", PushOptions{Registry: RegistryOptions{PlainHTTP: true, Anonymous: true}})
			select {
			case mutationErr := <-mutationErrors:
				t.Fatal(mutationErr)
			default:
			}
			if writes.Load() == 0 || changed.Load() != corrupt {
				t.Fatal("fixture did not exercise the prior output upload")
			}
			if !corrupt {
				if original != nil || !strings.HasSuffix(ref, "@"+expected) {
					t.Fatalf("unchanged publication control failed: %v", original)
				}
				return
			}
			assertRegistryCode(t, original, e.CodeVerifyFailed)
			var remote *transport.Error
			if ref != "" || errors.As(original, &remote) {
				t.Fatal("fixture did not reach local integrity refusal after upload")
			}
			safe := SafeRegistryPublishError(t.Context(), original)
			assertRegistryCode(t, safe, e.CodeVerifyFailed)
			if !errors.Is(safe, original) || !strings.Contains(safe.Error(), "expected digest") || !strings.Contains(safe.Error(), "destination may contain partial uploads") || strings.Contains(safe.Error(), root) || strings.Contains(safe.Error(), server.URL) {
				t.Fatalf("local post-upload failure lost phase, guidance, cause or privacy: %v", safe)
			}
		})
	}
}

func TestRegistryPublishWarningsFollowActualWritePhase(t *testing.T) {
	for _, kind := range []string{"release", "document", "disk"} {
		for _, phase := range []string{"pre-write", "write"} {
			t.Run(kind+"/"+phase, func(t *testing.T) {
				var writes atomic.Int64
				base := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if phase == "pre-write" {
						http.NotFound(w, r)
						return
					}
					if r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodPut {
						writes.Add(1)
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = w.Write([]byte(`{"errors":[{"code":"DIGEST_INVALID","message":"PRIVATE_SENTINEL"}]}`))
						return
					}
					base.ServeHTTP(w, r)
				}))
				defer server.Close()
				repo := strings.TrimPrefix(server.URL, "http://") + "/phase"
				options := RegistryOptions{PlainHTTP: true, Anonymous: true}
				var original error
				switch kind {
				case "release":
					root, _ := releasePublicationFixture(t)
					_, original = Push(t.Context(), root, repo, options)
				case "document":
					_, original = PushDocumentArtifact(t.Context(), repo, "application/vnd.nvkr.test-document.v1+json", []byte(`{"fixture":"exact bytes"}`), options)
				case "disk":
					root, payload, manifest := diskFixture(t, "linux/arm64")
					owner, err := OpenDiskArtifact(t.Context(), root, manifest, "linux/arm64", int64(len(payload)))
					if err != nil {
						t.Fatal(err)
					}
					defer owner.Close()
					_, original = PushDiskArtifact(t.Context(), owner, repo, t.TempDir(), options)
				}
				if original == nil {
					t.Fatal("fixture did not refuse publication")
				}
				safe := SafeRegistryPublishError(t.Context(), original)
				attempted := writes.Load() != 0
				if attempted != (phase == "write") || strings.Contains(safe.Error(), "partial uploads") != attempted ||
					strings.Contains(original.Error(), "partial uploads") != attempted || !errors.Is(safe, original) || strings.Contains(safe.Error(), "PRIVATE_SENTINEL") {
					t.Fatalf("write phase %v did not match safe warning: %v", attempted, safe)
				}
			})
		}
	}
}

func TestRegistryPrewriteCancellationDoesNotClaimPartialUploads(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var reads, writes atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writes.Add(1)
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v2/" {
			reads.Add(1)
			cancel()
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			select {
			case <-r.Context().Done():
			case <-timer.C:
				t.Error("API read did not observe caller cancellation")
			}
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	root, _ := releasePublicationFixture(t)
	reference, original := PushWithOptions(ctx, root, strings.TrimPrefix(server.URL, "http://")+"/phase", PushOptions{
		Registry: RegistryOptions{PlainHTTP: true, Anonymous: true},
	})
	assertRegistryCode(t, original, e.CodeTimeout)
	if reference != "" || reads.Load() != 1 || writes.Load() != 0 || !errors.Is(original, context.Canceled) {
		t.Fatalf("fixture did not cancel the first API read before upload: reads=%d writes=%d err=%v", reads.Load(), writes.Load(), original)
	}
	for _, failure := range []error{original, SafeRegistryPublishError(t.Context(), original)} {
		assertRegistryCode(t, failure, e.CodeTimeout)
		if !errors.Is(failure, context.Canceled) || strings.Contains(failure.Error(), "partial uploads") || strings.Contains(failure.Error(), "reconcile") {
			t.Fatalf("pre-write cancellation invented upload recovery or lost its cause: %v", failure)
		}
	}
}

func TestDocumentAndDiskCancellationPreservesPublicationPhase(t *testing.T) {
	for _, kind := range []string{"document", "disk"} {
		for _, afterWrite := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/after-write=%t", kind, afterWrite), func(t *testing.T) {
				var artifact *DiskArtifact
				if kind == "disk" {
					root, payload, manifest := diskFixture(t, "linux/arm64")
					var err error
					artifact, err = OpenDiskArtifact(t.Context(), root, manifest, "linux/arm64", int64(len(payload)))
					if err != nil {
						t.Fatal(err)
					}
					defer artifact.Close()
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				var posts, manifests, canceled atomic.Int64
				base := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						posts.Add(1)
					}
					manifest := r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/manifests/")
					if manifest {
						manifests.Add(1)
					}
					if afterWrite && manifest || !afterWrite && r.Method == http.MethodGet && r.URL.Path == "/v2/" {
						if _, err := io.Copy(io.Discard, r.Body); err != nil {
							t.Errorf("reading controlled registry request: %v", err)
							return
						}
						canceled.Add(1)
						cancel()
						timer := time.NewTimer(5 * time.Second)
						defer timer.Stop()
						select {
						case <-r.Context().Done():
						case <-timer.C:
							t.Error("registry request did not observe caller cancellation")
						}
						return
					}
					base.ServeHTTP(w, r)
				}))
				defer func() {
					cancel()
					server.CloseClientConnections()
					server.Close()
				}()
				repo := strings.TrimPrefix(server.URL, "http://") + "/cancellation"
				options := RegistryOptions{PlainHTTP: true, Anonymous: true}
				var original error
				if kind == "document" {
					_, original = PushDocumentArtifact(ctx, repo, "application/vnd.nvkr.test-document.v1+json", []byte(`{"fixture":"exact bytes"}`), options)
				} else {
					_, original = PushDiskArtifact(ctx, artifact, repo, t.TempDir(), options)
				}
				assertRegistryCode(t, original, e.CodeTimeout)
				var failure *RegistryFailure
				if !errors.Is(original, context.Canceled) || !errors.As(original, &failure) || canceled.Load() != 1 {
					t.Fatalf("cancellation lost its cause or registry observation: %v", original)
				}
				if (posts.Load() > 0) != afterWrite || (manifests.Load() == 1) != afterWrite {
					t.Fatalf("cancellation occurred in the wrong phase: posts=%d manifests=%d", posts.Load(), manifests.Load())
				}
				if strings.Contains(failure.Action, "reconcile partial uploads") != afterWrite || strings.Contains(original.Error(), "partial uploads") != afterWrite {
					t.Fatalf("timeout advice lost the actual publication phase: %v", original)
				}
			})
		}
	}
}

func TestRegistryLocallyRefusedWriteIsNotUploadProgress(t *testing.T) {
	progress := &registryPublicationProgress{}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "http://registry.invalid/v2/release/blobs/uploads/", strings.NewReader("synthetic body"))
	if err != nil {
		t.Fatal(err)
	}
	response, err := registryTransportWithProgress(t.Context(), RegistryOptions{IsolatedTransport: true}, progress).RoundTrip(request)
	if response != nil {
		_ = response.Body.Close()
	}
	assertRegistryCode(t, err, e.CodeUsage)
	if progress.writeAttempted.Load() {
		t.Fatal("HTTPS admission refusal was counted as a network write")
	}
}

func TestSafeRegistryCompletedPushReadbackGuidance(t *testing.T) {
	for _, code := range []e.Code{e.CodeVerifyFailed, e.CodeUnavailable} {
		t.Run(string(code), func(t *testing.T) {
			cause := &os.PathError{Op: "read", Path: "/PRIVATE_SENTINEL", Err: os.ErrPermission}
			original := e.Wrap(code, cause, "PRIVATE_SENTINEL")
			standalone := SafeRegistryReadbackError(t.Context(), original)
			completed := SafeRegistryPublishedReadbackError(t.Context(), original)
			assertRegistryCode(t, standalone, code)
			assertRegistryCode(t, completed, code)
			if strings.Contains(standalone.Error(), "push completed") || !strings.Contains(completed.Error(), "push completed, but readback did not verify") ||
				!errors.Is(completed, original) || strings.Contains(completed.Error(), "PRIVATE_SENTINEL") || strings.Contains(completed.Error(), "partial uploads") {
				t.Fatalf("completed push state, typed cause or redaction lost: %v", completed)
			}
		})
	}
}

func TestDiskCleanupFailureRetainsActualPublicationPhase(t *testing.T) {
	for _, uploaded := range []bool{false, true} {
		t.Run(fmt.Sprintf("uploaded=%v", uploaded), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			root, payload, manifest := diskFixture(t, "linux/arm64")
			owner, err := OpenDiskArtifact(ctx, root, manifest, "linux/arm64", int64(len(payload)))
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			var writes atomic.Int64
			base := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !uploaded {
					http.NotFound(w, r)
					return
				}
				if r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodPut {
					writes.Add(1)
				}
				base.ServeHTTP(w, r)
			}))
			defer server.Close()
			cause := &os.PathError{Op: "remove", Path: "/PRIVATE_SENTINEL", Err: os.ErrPermission}
			cleanupCalls := 0
			remove := func(path string) error {
				cleanupCalls++
				if err := os.RemoveAll(path); err != nil {
					return err
				}
				return cause
			}
			repo := strings.TrimPrefix(server.URL, "http://") + "/cleanup"
			ref, original := pushDiskArtifactWithCleanup(ctx, owner, repo, t.TempDir(), RegistryOptions{Anonymous: true, PlainHTTP: true}, remove)
			code := e.CodeInvalidCatalog
			if uploaded {
				code = e.CodeImmutableViolation
			}
			assertRegistryCode(t, original, code)
			if cleanupCalls != 1 || (writes.Load() != 0) != uploaded || ref != repo+"@"+owner.Inspection().ManifestDigest ||
				!errors.Is(original, cause) || errors.Is(original, context.Canceled) || ctx.Err() != nil {
				t.Fatalf("cleanup lost phase, reference, typed cause or caller deadline: writes=%d calls=%d ref=%q err=%v", writes.Load(), cleanupCalls, ref, original)
			}
			safe := SafeRegistryPublishError(ctx, original)
			assertRegistryCode(t, safe, code)
			if !errors.Is(safe, cause) || strings.Contains(safe.Error(), "PRIVATE_SENTINEL") || strings.Contains(safe.Error(), "partial uploads") != uploaded {
				t.Fatalf("cleanup error lost actual upload warning, cause or privacy: %v", safe)
			}
		})
	}
}
