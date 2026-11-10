// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
)

// Registry publication preserves caller deadlines and otherwise bounds its own
// work. The returned cleanup owns only a deadline installed by this boundary.
func registryPublicationContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if _, bounded := ctx.Deadline(); bounded {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}

// Classify responses, not error strings or presumed registry versions. A refusal
// never authorizes rewriting an already identified manifest or dropping evidence.
func registryPublishError(ctx context.Context, err error, operation string, args ...any) error {
	return registryOperationError(ctx, err, fmt.Sprintf(operation, args...), true)
}

// A successful base response with an unrecognized API (for example HTML) is
// endpoint evidence for this operation, not a reason to retry protocol failures.
func registryHasInvalidBase(ctx context.Context) bool {
	observer, ok := ctx.Value(registryObserverKey{}).(*registryObserver)
	if !ok {
		return false
	}
	report := observer.snapshot()
	return report.BaseStatus == http.StatusOK && report.API == "unknown"
}

func registryOperationError(ctx context.Context, err error, operation string, publishing bool) error {
	if observer, ok := ctx.Value(registryObserverKey{}).(*registryObserver); ok && observer.readOnly {
		publishing = false
	}
	suffix := ""
	if publishing {
		suffix = "; destination may contain partial uploads"
	}
	var coded *e.Error
	if errors.As(err, &coded) {
		// A presentation observer must not reclassify or duplicate an error
		// already issued by this boundary (including after child cleanup).
		switch coded.Err.(type) {
		case registryResponseError, registryNetworkError:
			return err
		}
		if coded.Code != e.CodeUnavailable {
			var response *transport.Error
			if errors.As(err, &response) {
				return e.Wrap(coded.Code, registryResponseError{response}, "%s", coded.Msg)
			}
			return err
		}
		// Preserve the owning operation's uncertainty and no-rollback guidance.
		operation += "; " + coded.Msg
	}
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, registryNetworkError{errors.Join(ctx.Err(), err)}, "%s%s", operation, suffix)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return e.Wrap(e.CodeTimeout, registryNetworkError{err}, "%s%s", operation, suffix)
	}
	var response *transport.Error
	if !errors.As(err, &response) {
		code := e.CodeUnavailable
		if registryHasInvalidBase(ctx) {
			code = e.CodeInvalidCatalog
		}
		return e.Wrap(code, registryNetworkError{err}, "%s%s", operation, suffix)
	}
	code := registryResponseCode(response)
	return e.Wrap(code, registryResponseError{response}, "%s%s", operation, suffix)
}

// Network client errors can embed credential-bearing token-service URLs. Keep
// their typed causes, while presenting only a stable, non-sensitive category.
type registryNetworkError struct{ cause error }

func (failure registryNetworkError) Unwrap() error { return failure.cause }
func (failure registryNetworkError) Error() string {
	var dns *net.DNSError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var certificate x509.CertificateInvalidError
	switch {
	case errors.Is(failure.cause, context.Canceled):
		return "registry request canceled"
	case errors.Is(failure.cause, context.DeadlineExceeded):
		return "registry request deadline exceeded"
	case errors.As(failure.cause, &dns):
		return "registry DNS lookup failed"
	case errors.As(failure.cause, &authority), errors.As(failure.cause, &hostname), errors.As(failure.cause, &certificate):
		return "registry TLS certificate validation failed"
	default:
		return "registry connection or response failed"
	}
}

func registryReadbackError(ctx context.Context, err error, operation string, args ...any) error {
	var coded *e.Error
	if errors.As(err, &coded) {
		switch coded.Err.(type) {
		case registryResponseError, registryNetworkError:
			return err // Preserve the first owning readback classification and message.
		}
	}
	if ctx.Err() == nil && (!errors.As(err, &coded) || coded.Code == e.CodeUnavailable) {
		var response *transport.Error
		// Absence during readback can be temporary visibility. It neither
		// accepts the publication nor establishes an unsupported representation.
		if errors.As(err, &response) && response.StatusCode == http.StatusNotFound && registryResponseCode(response) == e.CodeRegistryRejected {
			message := fmt.Sprintf(operation, args...)
			if coded != nil {
				message += "; " + coded.Msg
			}
			return e.Wrap(e.CodeUnavailable, registryResponseError{response}, "%s; publication readback is incomplete", message)
		}
	}
	return registryPublishError(ctx, err, operation, args...)
}

func registryResponseCode(response *transport.Error) e.Code {
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return e.CodeRegistryAuthFailed
	}
	for _, diagnostic := range response.Errors {
		if diagnostic.Code == transport.UnauthorizedErrorCode || diagnostic.Code == transport.DeniedErrorCode {
			return e.CodeRegistryAuthFailed
		}
	}
	if response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
		return e.CodeUnavailable
	}
	if response.StatusCode == http.StatusNotFound && len(response.Errors) == 0 && response.Request != nil &&
		response.Request.Method == http.MethodGet && response.Request.URL != nil && response.Request.URL.Path == "/v2/" {
		return e.CodeInvalidCatalog
	}
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		if len(response.Errors) == 1 {
			switch response.Errors[0].Code {
			case transport.UnsupportedErrorCode:
				return e.CodeEngineCapability
			case transport.DigestInvalidErrorCode, transport.SizeInvalidErrorCode:
				return e.CodeVerifyFailed
			}
		}
		return e.CodeRegistryRejected
	}
	return e.CodeUnavailable
}

// Keep the typed cause for callers while presenting only bounded, recognized
// diagnostics. Registry messages/details and request URLs can contain secrets.
type registryResponseError struct{ cause *transport.Error }

func (failure registryResponseError) Unwrap() error { return failure.cause }

func (failure registryResponseError) Error() string {
	known := map[string]bool{}
	for _, diagnostic := range failure.cause.Errors {
		code := "UNRECOGNIZED"
		switch diagnostic.Code {
		case transport.BlobUnknownErrorCode, transport.BlobUploadInvalidErrorCode, transport.BlobUploadUnknownErrorCode,
			transport.DigestInvalidErrorCode, transport.ManifestBlobUnknownErrorCode, transport.ManifestInvalidErrorCode,
			transport.ManifestUnknownErrorCode, transport.ManifestUnverifiedErrorCode, transport.NameInvalidErrorCode,
			transport.NameUnknownErrorCode, transport.SizeInvalidErrorCode, transport.TagInvalidErrorCode,
			transport.UnauthorizedErrorCode, transport.DeniedErrorCode, transport.UnsupportedErrorCode,
			transport.TooManyRequestsErrorCode, transport.UnknownErrorCode, transport.UnavailableErrorCode:
			code = string(diagnostic.Code)
		}
		known[code] = true
	}
	codes := make([]string, 0, len(known))
	for code := range known {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return fmt.Sprintf("registry HTTP %d (%s); diagnostic codes [%s]", failure.cause.StatusCode,
		http.StatusText(failure.cause.StatusCode), strings.Join(codes, ", "))
}
