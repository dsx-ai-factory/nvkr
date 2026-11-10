// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	orasremote "oras.land/oras-go/v2/registry/remote"
	orasauth "oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

// RegistryReport describes responses observed within one operation, not a
// conformance certificate or authority to change a publication representation.
// Unknown is deliberately distinct from a refusal or an observed successful API.
type RegistryReport struct {
	API                    string `json:"api"`
	CredentialSource       string `json:"credentialSource"`
	AdvertisedAPIVersion   string `json:"advertisedAPIVersion"`
	OCIDistributionVersion string `json:"ociDistributionVersion"`
	BaseStatus             int    `json:"baseStatus,omitempty"`
	AuthChallenge          bool   `json:"authChallengeObserved"`
	ManifestUpload         string `json:"manifestUpload"`
	BlobUpload             string `json:"blobUpload"`
	ReferrersAPI           string `json:"referrersAPI"`
	ArtifactTypeAcceptance string `json:"artifactTypeAcceptance"`
	LastOperation          string `json:"lastOperation"`
	LastStatus             int    `json:"lastStatus,omitempty"`
	ManifestMediaType      string `json:"manifestMediaType,omitempty"`
}

func unknownRegistryReport() RegistryReport {
	return RegistryReport{API: "unknown", CredentialSource: "unknown", AdvertisedAPIVersion: "absent", OCIDistributionVersion: "unknown",
		ManifestUpload: "unknown", BlobUpload: "unknown", ReferrersAPI: "unknown", ArtifactTypeAcceptance: "unknown", LastOperation: "unknown", ManifestMediaType: "unknown"}
}

// RegistryFailure exposes bounded observations and a recommended next action.
// Unwrap retains the original coded and typed causes; diagnostics grant no trust.
type RegistryFailure struct {
	Report RegistryReport
	Action string
	cause  error
}

func (failure *RegistryFailure) Unwrap() error { return failure.cause }
func (failure *RegistryFailure) Error() string {
	return fmt.Sprintf("%v; registry API=%s (advertised=%s), OCI version=%s, request=%s, HTTP=%d, manifest media=%s; %s",
		failure.cause, failure.Report.API, failure.Report.AdvertisedAPIVersion, failure.Report.OCIDistributionVersion,
		failure.Report.LastOperation, failure.Report.LastStatus, failure.Report.ManifestMediaType, failure.Action)
}

type registryObserverKey struct{}
type registryAttachmentKey struct{}
type registryObserver struct {
	mu       sync.Mutex
	report   RegistryReport
	readOnly bool
}

func observeRegistry(ctx context.Context) (context.Context, *registryObserver) {
	observer := &registryObserver{report: unknownRegistryReport()}
	if ctx == nil {
		return ctx, observer
	}
	return context.WithValue(ctx, registryObserverKey{}, observer), observer
}

func (observer *registryObserver) snapshot() RegistryReport {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.report
}

// Observe the pinned client's normal traffic. Never read or replace request or
// response bodies, add requests, change retry policy, or retain credentials/URLs.
func (observer *registryObserver) transport(base http.RoundTripper) http.RoundTripper {
	return registryObservationTransport{base: base, observer: observer}
}

type registryObservationTransport struct {
	base     http.RoundTripper
	observer *registryObserver
}

func (observed registryObservationTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := observed.base.RoundTrip(request)
	stage := registryRequestStage(request)
	observed.observer.mu.Lock()
	defer observed.observer.mu.Unlock()
	report := &observed.observer.report
	report.LastOperation, report.LastStatus = stage, 0
	if response == nil {
		return response, err
	}
	report.LastStatus = response.StatusCode
	if stage == "api-check" {
		report.API = "unknown"
		report.BaseStatus = response.StatusCode
		report.AuthChallenge = report.AuthChallenge || response.StatusCode == http.StatusUnauthorized && response.Header.Get("WWW-Authenticate") != ""
		if version := response.Header.Get("Docker-Distribution-API-Version"); version != "" {
			report.AdvertisedAPIVersion = "unrecognized"
			if version == "registry/2.0" {
				report.AdvertisedAPIVersion = version
			}
		}
		contentType, _, contentTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
		validContentType := contentTypeErr == nil || response.Header.Get("Content-Type") == ""
		if response.StatusCode == http.StatusOK && validContentType && contentType != "text/html" || response.StatusCode == http.StatusUnauthorized && report.AdvertisedAPIVersion == "registry/2.0" {
			report.API = "distribution-v2"
		}
	}
	if stage == "manifest-upload" {
		report.ManifestMediaType = knownManifestMediaType(request.Header.Get("Content-Type"))
		if response.StatusCode == http.StatusCreated {
			report.ManifestUpload = "observed"
		}
	}
	if stage == "blob-upload" && (response.StatusCode == http.StatusCreated || response.StatusCode == http.StatusAccepted) {
		report.BlobUpload = "observed"
	}
	contentType, _, contentTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if stage == "referrer-discovery" && response.StatusCode == http.StatusOK && contentTypeErr == nil && contentType == MediaOCIIndex {
		report.ReferrersAPI = "observed"
	}
	return response, err
}

// These labels describe requests already constructed by the owning client;
// they do not parse or validate repository names or authorize a capability.
func registryRequestStage(request *http.Request) string {
	if request.URL == nil {
		return "transport"
	}
	path := request.URL.Path
	manifest := strings.LastIndex(path, "/manifests/")
	referrers := strings.LastIndex(path, "/referrers/")
	blobs := strings.LastIndex(path, "/blobs/")
	switch {
	case path == "/v2/":
		return "api-check"
	case !strings.HasPrefix(path, "/v2/"):
		return "authentication-or-redirect"
	case referrers >= 0 && referrers > manifest && referrers > blobs:
		return "referrer-discovery"
	case manifest >= 0 && manifest > blobs:
		if request.Method == http.MethodPut {
			if attachment, _ := request.Context().Value(registryAttachmentKey{}).(bool); attachment {
				return "subject-attachment"
			}
			return "manifest-upload"
		}
		return "manifest-readback"
	case blobs >= 0 && strings.HasPrefix(path[blobs:], "/blobs/uploads/"):
		return "blob-upload"
	case blobs >= 0:
		return "blob-readback"
	default:
		return "registry-request"
	}
}

func knownManifestMediaType(value string) string {
	switch value {
	case MediaOCIManifest, MediaOCIIndex, "application/vnd.docker.distribution.manifest.v2+json", "application/vnd.docker.distribution.manifest.list.v2+json":
		return value
	default:
		return "unknown"
	}
}

func (observer *registryObserver) explain(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	report := observer.snapshot()
	if report.LastOperation == "unknown" {
		return err // Admission/local errors occurred before registry I/O.
	}
	// Existing owners have already classified and sanitized their boundary.
	// Preserve the whole wrapper, including immutable locators and uncertainty
	// guidance; observing a readback 404 must not reclassify it as a push refusal.
	var coded *e.Error
	_ = errors.As(err, &coded)
	var response *transport.Error
	var network registryNetworkError
	registryCause := errors.As(err, &response) || errors.As(err, &network)
	if !registryCause && (coded == nil || (coded.Code != e.CodeVerifyFailed && coded.Code != e.CodeInvalidCatalog && coded.Code != e.CodeEngineCapability && coded.Code != e.CodeTimeout)) {
		return err // Local failures do not inherit the last registry response.
	}
	if coded == nil {
		err = registryOperationError(ctx, err, "registry operation failed", !observer.readOnly)
		_ = errors.As(err, &coded)
	}
	if errors.As(err, &response) && response.Request != nil {
		report.LastOperation, report.LastStatus = registryRequestStage(response.Request), response.StatusCode
		report.ManifestMediaType = "unknown"
		if report.LastOperation == "manifest-upload" || report.LastOperation == "subject-attachment" {
			report.ManifestMediaType = knownManifestMediaType(response.Request.Header.Get("Content-Type"))
		}
	}
	action := "Check the endpoint, TLS trust, DNS and connectivity; retry transient failures with the same identified bytes."
	if coded != nil {
		switch coded.Code {
		case e.CodeRegistryAuthFailed:
			action = "Check the configured credential and permissions for the exact repository; authentication refusal does not establish an unsupported API."
		case e.CodeRegistryRejected:
			action = "Check the rejected operation and repository policy."
			if report.LastOperation == "manifest-upload" {
				action = "The manifest was rejected; the response does not establish which field is incompatible. Check manifest media type and repository policy; an explicit compatible representation requires rebuilding before signing, never rewriting this publication."
			}
			if report.LastOperation == "subject-attachment" {
				action = "The required evidence attachment was rejected. Check the signed artifact profile and repository policy; do not drop evidence or substitute unsigned documents."
			}
		case e.CodeEngineCapability:
			action = "The requested operation was explicitly refused as unsupported; choose an admitted alternative before constructing a new publication."
		case e.CodeVerifyFailed:
			action = "Check exact manifest/blob digests, sizes and readback; do not accept or rewrite mismatched bytes."
		case e.CodeInvalidCatalog:
			action = "Check the named tag and repository tag policy; no rollback or automatic retagging is implied."
		case e.CodeTimeout:
			action = "The operation was canceled or timed out; reconcile partial uploads before retrying the same identified publication."
		}
		if observer.readOnly && (coded.Code == e.CodeTimeout || coded.Code == e.CodeUnavailable) {
			action = "Check the endpoint, TLS trust, DNS and connectivity, then retry the bounded read-only operation; no upload was attempted."
		}
	}
	if !registryCause {
		// Keep useful owner-specific guidance without attributing local errors
		// to the registry or attaching the status of an unrelated request.
		return fmt.Errorf("%w; %s", err, action)
	}
	if coded != nil && coded.Code == e.CodeInvalidCatalog && report.API == "unknown" &&
		(report.BaseStatus == http.StatusOK || report.LastOperation == "api-check" && report.BaseStatus != 0) {
		action = "The response did not establish the V2 registry API; check the registry endpoint and proxy routing. This does not establish an older supported API."
	}
	if response != nil && registryResponseCode(response) == e.CodeRegistryAuthFailed {
		action = "Check the configured credential and permissions for the exact repository; authentication refusal does not establish an unsupported API."
	}
	return &RegistryFailure{Report: report, Action: action, cause: err}
}

// CheckRegistry performs only a bounded base API/authentication check, using
// the existing credential owner and ORAS authentication implementation. It does
// not test push permissions, artifact types, referrers, or mutate a repository.
func CheckRegistry(ctx context.Context, repository string, options RegistryOptions) (RegistryReport, error) {
	report := unknownRegistryReport()
	if err := deliveryContext(ctx); err != nil {
		return report, err
	}
	if err := options.Validate(); err != nil {
		return report, err
	}
	repo, err := name.NewRepository(repository, name.StrictValidation)
	if err != nil || repo.Name() != repository || strings.Contains(repository, "://") || strings.ContainsAny(repository[strings.LastIndex(repository, "/")+1:], ":@") {
		return report, e.New(e.CodeUsage, "registry check requires a canonical untagged repository")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	ctx, observer := observeRegistry(ctx)
	observer.readOnly = true
	observer.report.CredentialSource = "ambient"
	if options.Anonymous {
		observer.report.CredentialSource = "anonymous"
	} else if options.Password != "" {
		observer.report.CredentialSource = "explicit"
	}
	registry, err := orasremote.NewRegistry(repo.RegistryStr())
	if err != nil {
		return report, e.Wrap(e.CodeUsage, err, "parsing registry check endpoint")
	}
	registry.PlainHTTP = options.PlainHTTP
	registry.Client = &orasauth.Client{Client: &http.Client{Transport: observer.transport(remote.DefaultTransport)},
		Credential: func(ctx context.Context, host string) (orasauth.Credential, error) {
			if host != repo.RegistryStr() || options.Anonymous {
				return orasauth.EmptyCredential, nil
			}
			if options.Password != "" {
				return orasauth.Credential{Username: options.Username, Password: options.Password}, nil
			}
			authenticator, err := authn.Resolve(ctx, options.AmbientKeychain(), repo.Registry)
			if err != nil {
				return orasauth.EmptyCredential, e.New(e.CodeRegistryAuthFailed, "resolving registry credential failed")
			}
			credential, err := authn.Authorization(ctx, authenticator)
			if err != nil {
				return orasauth.EmptyCredential, e.New(e.CodeRegistryAuthFailed, "reading registry credential failed")
			}
			return orasauth.Credential{Username: credential.Username, Password: credential.Password,
				RefreshToken: credential.IdentityToken, AccessToken: credential.RegistryToken}, nil
		}}
	err = registry.Ping(ctx)
	originalErr := err
	var response *errcode.ErrorResponse
	if errors.As(err, &response) {
		// Normalize the pinned client's typed diagnostics, never its error text.
		converted := &transport.Error{StatusCode: response.StatusCode}
		for _, diagnostic := range response.Errors {
			converted.Errors = append(converted.Errors, transport.Diagnostic{Code: transport.ErrorCode(diagnostic.Code)})
		}
		err = converted
	} else if err != nil {
		// ORAS may return a credential-resolution error after observing a
		// challenge, rather than an ErrorResponse. Classify the observed HTTP
		// refusal without inspecting that error's text or exposing its cause.
		status := observer.snapshot().LastStatus
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			err = &transport.Error{StatusCode: status}
		}
	}
	observed := observer.snapshot()
	// The pinned ORAS Registry.Ping returns ErrNotFound for every base 404
	// without parsing its body. Do not pretend those diagnostics were observed.
	if observed.LastOperation == "api-check" && observed.API == "unknown" && (err == nil || observed.BaseStatus == http.StatusNotFound) {
		err = e.Wrap(e.CodeInvalidCatalog, registryResponseError{&transport.Error{StatusCode: observed.BaseStatus}},
			"base response did not establish the V2 registry API; check the endpoint and proxy")
	}
	if err != nil {
		err = registryOperationError(ctx, err, "checking registry API (read-only)", false)
	}
	explained := observer.explain(ctx, err)
	if originalErr != nil {
		explained = registryCheckFailure{presented: explained, original: originalErr}
	}
	return observer.snapshot(), explained
}

type registryCheckFailure struct{ presented, original error }

func (failure registryCheckFailure) Error() string { return failure.presented.Error() }
func (failure registryCheckFailure) Unwrap() []error {
	return []error{failure.presented, failure.original}
}
