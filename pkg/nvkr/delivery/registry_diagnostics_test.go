// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

func TestRegistryCheckObservesAPINotFeatureVersion(t *testing.T) {
	for _, test := range []struct {
		name, header, contentType, api, advertised string
		status                                     int
		code                                       e.Code
	}{
		{"optional header absent", "", "application/json", "distribution-v2", "absent", 200, ""},
		{"v2 advertised", "registry/2.0", "application/json", "distribution-v2", "registry/2.0", 200, ""},
		{"unrecognized header", "PRIVATE_SENTINEL", "application/json", "distribution-v2", "unrecognized", 200, ""},
		{"challenge is not feature support", "registry/2.0", "application/json", "distribution-v2", "registry/2.0", 401, e.CodeRegistryAuthFailed},
		{"denied is not unsupported", "", "application/json", "unknown", "absent", 403, e.CodeRegistryAuthFailed},
		{"missing endpoint", "", "application/json", "unknown", "absent", 404, e.CodeInvalidCatalog},
		{"proxy html", "", "text/html", "unknown", "absent", 200, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet || r.URL.Path != "/v2/" {
					t.Error("registry check performed more than a read-only base check")
				}
				w.Header().Set("Content-Type", test.contentType)
				if test.header != "" {
					w.Header().Set("Docker-Distribution-API-Version", test.header)
				}
				if test.status == http.StatusUnauthorized {
					w.Header().Set("WWW-Authenticate", `Basic realm="synthetic"`)
				}
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, `{"errors":[{"code":"UNAUTHORIZED","message":"PRIVATE_SENTINEL"}]}`)
			}))
			defer server.Close()
			report, err := CheckRegistry(t.Context(), strings.TrimPrefix(server.URL, "http://")+"/project", RegistryOptions{Anonymous: true, PlainHTTP: true})
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertRegistryCode(t, err, test.code)
				if test.status == http.StatusForbidden {
					var original *errcode.ErrorResponse
					if !errors.As(err, &original) {
						t.Fatal("original typed ORAS response was lost")
					}
				}
				if strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
					t.Fatal("diagnostic disclosed untrusted response content")
				}
				if strings.Contains(err.Error(), "partial uploads") {
					t.Fatal("read-only check claimed to upload content")
				}
				if test.code == e.CodeInvalidCatalog && (e.Retryable(err) || !strings.Contains(err.Error(), "endpoint and proxy routing")) {
					t.Fatalf("missing endpoint correction guidance or permanent classification: %v", err)
				}
			}
			if report.API != test.api || report.AdvertisedAPIVersion != test.advertised || report.OCIDistributionVersion != "unknown" ||
				report.ManifestUpload != "unknown" || report.BlobUpload != "unknown" || report.ReferrersAPI != "unknown" || report.ArtifactTypeAcceptance != "unknown" {
				t.Fatalf("base response invented capabilities: %+v", report)
			}
			if requests.Load() != 1 {
				t.Fatalf("unexpected probes/retries: %d", requests.Load())
			}
		})
	}
}

func TestRegistryCheckAuthenticatesWithoutWriteProbe(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v2/" {
			t.Error("unexpected write or repository probe")
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "synthetic-user" || password != "synthetic-password" {
			w.Header().Set("WWW-Authenticate", `Basic realm="synthetic"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	report, err := CheckRegistry(t.Context(), strings.TrimPrefix(server.URL, "http://")+"/project", RegistryOptions{
		Username: "synthetic-user", Password: "synthetic-password", PlainHTTP: true,
	})
	if err != nil || !report.AuthChallenge || report.API != "distribution-v2" || requests.Load() != 2 {
		t.Fatalf("credential negotiation failed: %+v %v requests=%d", report, err, requests.Load())
	}
}

func TestRegistryCheckBearerChallengeUsesPinnedAuthClient(t *testing.T) {
	var endpoint string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("unexpected mutation")
		}
		switch r.URL.Path {
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"token":"synthetic-token","expires_in":60}`)
		case "/v2/":
			if r.Header.Get("Authorization") != "Bearer synthetic-token" {
				w.Header().Set("WWW-Authenticate", `Bearer realm="`+endpoint+`/token",service="synthetic"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Error("unexpected repository probe")
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	endpoint = server.URL
	report, err := CheckRegistry(t.Context(), strings.TrimPrefix(server.URL, "http://")+"/project", RegistryOptions{Anonymous: true, PlainHTTP: true})
	if err != nil || !report.AuthChallenge || report.API != "distribution-v2" || report.ManifestUpload != "unknown" {
		t.Fatalf("bearer API discovery failed: %+v %v", report, err)
	}
}

func TestRegistryCheckRejectsHTMLAfterAuthentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if _, _, authenticated := request.BasicAuth(); !authenticated {
			w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
			w.Header().Set("WWW-Authenticate", `Basic realm="synthetic"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	report, err := CheckRegistry(t.Context(), strings.TrimPrefix(server.URL, "http://")+"/project", RegistryOptions{
		Username: "synthetic-user", Password: "synthetic-password", PlainHTTP: true,
	})
	assertRegistryCode(t, err, e.CodeInvalidCatalog)
	if report.API != "unknown" || !report.AuthChallenge {
		t.Fatalf("earlier challenge masked a later proxy response: %+v", report)
	}
}

func TestRegistryObservationFailureUsesExactFailedRequest(t *testing.T) {
	for _, test := range []struct{ path, stage string }{
		{"/v2/project/manifests/sha256:abc", "manifest-upload"},
		{"/v2/project/blobs/uploads/id", "blob-upload"},
		{"/v2/project/referrers/sha256:abc", "referrer-discovery"},
		{"/v2/referrers/project/manifests/sha256:abc", "manifest-upload"},
	} {
		t.Run(test.stage+test.path, func(t *testing.T) {
			ctx, observer := observeRegistry(t.Context())
			// A concurrent success must not overwrite the failed request's stage.
			observer.report.LastOperation = "manifest-readback"
			observer.report.ManifestMediaType = MediaOCIIndex
			request, _ := http.NewRequestWithContext(ctx, http.MethodPut, "https://registry.invalid"+test.path, nil)
			request.Header.Set("Content-Type", MediaOCIManifest)
			response := &transport.Error{Request: request, StatusCode: 400, Errors: []transport.Diagnostic{{Code: transport.ManifestInvalidErrorCode}}}
			err := observer.explain(ctx, response)
			assertRegistryCode(t, err, e.CodeRegistryRejected)
			var failure *RegistryFailure
			if !errors.As(err, &failure) || failure.Report.LastOperation != test.stage || failure.Report.LastStatus != 400 {
				t.Fatalf("wrong operation analysis: %v", err)
			}
			if test.stage == "manifest-upload" && failure.Report.ManifestMediaType != MediaOCIManifest {
				t.Fatal("concurrent manifest media type replaced the failed request's media type")
			}
		})
	}
}

func TestRegistryDiagnosticPreservesTypedNetworkCauseWithoutURL(t *testing.T) {
	cause := &url.Error{Op: "Get", URL: "https://token.invalid/?secret=PRIVATE_SENTINEL", Err: &net.DNSError{Err: "PRIVATE_SENTINEL", Name: "PRIVATE_SENTINEL"}}
	err := registryPublishError(t.Context(), cause, "publishing")
	assertRegistryCode(t, err, e.CodeUnavailable)
	var dns *net.DNSError
	if !errors.As(err, &dns) || strings.Contains(err.Error(), "PRIVATE_SENTINEL") || !strings.Contains(err.Error(), "DNS") {
		t.Fatalf("unsafe or untyped connection diagnostic: %v", err)
	}
	for _, test := range []struct {
		ctx  context.Context
		code e.Code
	}{{nil, e.CodeUsage}, {canceledRegistryContext(t), e.CodeTimeout}} {
		_, err := CheckRegistry(test.ctx, "registry.invalid/project", RegistryOptions{Anonymous: true})
		assertRegistryCode(t, err, test.code)
	}
}

func TestRegistryDiagnosticRedactsTimeoutAndTLSCauses(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
		code  e.Code
	}{{"deadline", context.DeadlineExceeded, e.CodeTimeout}, {"cancel", context.Canceled, e.CodeTimeout},
		{"certificate", x509.UnknownAuthorityError{Cert: &x509.Certificate{}}, e.CodeUnavailable}} {
		t.Run(test.name, func(t *testing.T) {
			cause := &url.Error{Op: "Get", URL: "https://token.invalid/?secret=PRIVATE_SENTINEL", Err: test.cause}
			err := registryPublishError(t.Context(), cause, "publishing")
			assertRegistryCode(t, err, test.code)
			var original *url.Error
			if strings.Contains(err.Error(), "PRIVATE_SENTINEL") || !errors.As(err, &original) || original != cause {
				t.Fatalf("unsafe or lost typed network cause: %v", err)
			}
		})
	}
	ctx := canceledRegistryContext(t)
	response := &transport.Error{StatusCode: http.StatusBadRequest}
	err := registryPublishError(ctx, response, "publishing")
	assertRegistryCode(t, err, e.CodeTimeout)
	var original *transport.Error
	if !errors.As(err, &original) || original != response || !errors.Is(err, context.Canceled) {
		t.Fatal("deadline handling discarded the original response")
	}
}

func TestRegistryCheckHonorsCallerDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) { <-request.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, err := CheckRegistry(ctx, strings.TrimPrefix(server.URL, "http://")+"/project", RegistryOptions{Anonymous: true, PlainHTTP: true})
	assertRegistryCode(t, err, e.CodeTimeout)
	if !strings.Contains(err.Error(), "no upload was attempted") {
		t.Fatalf("missing read-only deadline guidance: %v", err)
	}
	if strings.Contains(err.Error(), "partial uploads") {
		t.Fatal("read-only timeout claimed partial uploads")
	}
}

func TestRegistryObservationDistinguishesBlobAndEvidenceFailures(t *testing.T) {
	for _, test := range []struct {
		stage      string
		status     int
		diagnostic string
		code       e.Code
		action     string
	}{
		{"blob-upload", http.StatusForbidden, "DENIED", e.CodeRegistryAuthFailed, "configured credential"},
		{"subject-attachment", http.StatusForbidden, "DENIED", e.CodeRegistryAuthFailed, "configured credential"},
		{"referrer-discovery", http.StatusForbidden, "DENIED", e.CodeRegistryAuthFailed, "configured credential"},
		{"subject-attachment", http.StatusBadRequest, "MANIFEST_INVALID", e.CodeRegistryRejected, "do not drop evidence"},
	} {
		t.Run(test.stage+"/"+test.diagnostic, func(t *testing.T) {
			stage := test.stage
			request := releasePublicationRequestWithRepresentation(t, "", "", OCINVKRCompatibleV1)
			prepared, attested := filepath.Join(t.TempDir(), "prepared"), filepath.Join(t.TempDir(), "attested")
			if _, err := BuildPublicationRoot(t.Context(), prepared, request); err != nil {
				t.Fatal(err)
			}
			result, err := AttestPreparedEvidence(t.Context(), prepared, attested, fixtureMetadataAttester)
			if err != nil {
				t.Fatal(err)
			}
			inspection, err := Inspect(t.Context(), attested)
			if err != nil {
				t.Fatal(err)
			}
			evidence := ""
			for _, object := range inspection.Root.Objects {
				if object.Evidence != nil {
					evidence = object.DescriptorDigest
				}
			}
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				refuse := stage == "blob-upload" && r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/blobs/uploads/") ||
					stage == "subject-attachment" && r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/manifests/"+evidence) ||
					stage == "referrer-discovery" && strings.Contains(r.URL.Path, "/referrers/")
				if refuse {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(test.status)
					_, _ = fmt.Fprintf(w, `{"errors":[{"code":%q,"message":"PRIVATE_SENTINEL"}]}`, test.diagnostic)
					return
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			ref, err := Push(t.Context(), attested, strings.TrimPrefix(server.URL, "http://")+"/product/releases", RegistryOptions{PlainHTTP: true, Anonymous: true})
			assertRegistryCode(t, err, test.code)
			var failure *RegistryFailure
			if ref != "" || !errors.As(err, &failure) || failure.Report.LastOperation != stage || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
				t.Fatalf("publication failure analysis: ref=%s err=%v", ref, err)
			}
			if !strings.Contains(failure.Action, test.action) {
				t.Fatalf("wrong stage-specific guidance: %s", failure.Action)
			}
			after, err := Inspect(t.Context(), attested)
			if err != nil || after.RootDigest != result.RootDigest {
				t.Fatal("diagnostics rewrote identified evidence or membership", err)
			}
		})
	}
}

func canceledRegistryContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	return ctx
}

func TestRegistryObservationMissingBaseIsInvalidEndpoint(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	root, _ := releasePublicationFixture(t)
	ref, err := Push(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", RegistryOptions{PlainHTTP: true, Anonymous: true})
	assertRegistryCode(t, err, e.CodeInvalidCatalog)
	if ref != "" {
		t.Fatal("missing endpoint produced a success reference")
	}
}

func TestRegistryObservationHTMLBaseIsInvalidEndpoint(t *testing.T) {
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPut || request.Method == http.MethodPost || request.Method == http.MethodDelete {
			writes.Add(1)
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, "<html>PRIVATE_SENTINEL login page</html>")
	}))
	defer server.Close()
	root, digest := releasePublicationFixture(t)
	ref, err := Push(t.Context(), root, strings.TrimPrefix(server.URL, "http://")+"/product/releases", RegistryOptions{PlainHTTP: true, Anonymous: true})
	assertRegistryCode(t, err, e.CodeInvalidCatalog)
	if ref != "" || e.Retryable(err) || !strings.Contains(err.Error(), "endpoint and proxy routing") ||
		strings.Contains(err.Error(), "PRIVATE_SENTINEL") || writes.Load() != 0 {
		t.Fatalf("HTML proxy was accepted, retried or given the wrong action: %s %v (writes %d)", ref, err, writes.Load())
	}
	inspection, err := Inspect(t.Context(), root)
	if err != nil || inspection.RootDigest != digest {
		t.Fatalf("endpoint rejection changed local publication bytes: %v", err)
	}
}

func TestRegistryCheckDocumentsOpaque404Diagnostics(t *testing.T) {
	for _, diagnostic := range []string{"DENIED", "UNSUPPORTED"} {
		t.Run(diagnostic, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				if request.Method != http.MethodGet || request.URL.Path != "/v2/" {
					t.Error("check made a non-base request")
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprintf(w, `{"errors":[{"code":%q,"message":"PRIVATE_SENTINEL"}]}`, diagnostic)
			}))
			defer server.Close()
			report, err := CheckRegistry(t.Context(), strings.TrimPrefix(server.URL, "http://")+"/project", RegistryOptions{PlainHTTP: true, Anonymous: true})
			assertRegistryCode(t, err, e.CodeInvalidCatalog)
			if requests.Load() != 1 || report.API != "unknown" || e.Retryable(err) ||
				!strings.Contains(err.Error(), "endpoint and proxy routing") || strings.Contains(err.Error(), diagnostic) || strings.Contains(err.Error(), "PRIVATE_SENTINEL") {
				t.Fatalf("check inferred diagnostics the pinned client does not expose: %+v %v", report, err)
			}
		})
	}
}

func TestRegistryObservationKeepsLocalFailuresSeparate(t *testing.T) {
	for _, test := range []struct {
		name   string
		code   e.Code
		action string
	}{
		{"scratch cleanup", e.CodeImmutableViolation, ""},
		{"local availability", e.CodeUnavailable, ""},
		{"local integrity", e.CodeVerifyFailed, "Check exact manifest/blob digests"},
		{"local tag validation", e.CodeInvalidCatalog, "Check the named tag"},
		{"local capability", e.CodeEngineCapability, "choose an admitted alternative"},
		{"local timeout", e.CodeTimeout, "reconcile partial uploads before retrying"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, observer := observeRegistry(t.Context())
			observer.report.LastOperation = "manifest-upload"
			observer.report.LastStatus = http.StatusCreated
			cause := errors.New("local filesystem failure")
			local := e.Wrap(test.code, cause, "%s", test.name)
			err := observer.explain(ctx, local)
			assertRegistryCode(t, err, test.code)
			var registryFailure *RegistryFailure
			if errors.As(err, &registryFailure) || strings.Contains(err.Error(), "HTTP=") || strings.Contains(err.Error(), "connectivity") {
				t.Fatalf("local failure was attributed to the registry: %v", err)
			}
			if !errors.Is(err, cause) || !errors.Is(err, local) || !strings.Contains(err.Error(), test.action) {
				t.Fatalf("local cause or owner guidance was lost: %v", err)
			}
			if test.action == "" && err != local {
				t.Fatalf("local error without registry guidance was changed: %v", err)
			}
		})
	}
}

func TestRegistryObservationRetainsRegistryFailureJoinedWithCleanup(t *testing.T) {
	ctx, observer := observeRegistry(t.Context())
	observer.report.LastOperation = "manifest-readback"
	observer.report.LastStatus = http.StatusNotFound
	response := &transport.Error{StatusCode: http.StatusNotFound}
	registryErr := registryReadbackError(ctx, response, "reading back immutable publication")
	cleanup := e.New(e.CodeImmutableViolation, "removing disk upload scratch")
	err := observer.explain(ctx, errors.Join(registryErr, cleanup))
	assertRegistryCode(t, err, e.CodeUnavailable)
	var failure *RegistryFailure
	if !errors.As(err, &failure) || !errors.Is(err, response) || !errors.Is(err, cleanup) ||
		!strings.Contains(err.Error(), "publication readback is incomplete") {
		t.Fatalf("registry or cleanup cause was lost: %v", err)
	}
}
