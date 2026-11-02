// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/siteidentity"
)

// Observations never authorize completion or cleanup. In particular, a joined
// local exit cannot establish that the remote mutation joined.
func nativeIdentityCommandObservation(err error) string {
	command, exit := "failed", "unavailable"
	switch {
	case errors.Is(err, ErrUnjoined):
		command = "unjoined"
	case errors.Is(err, context.Canceled):
		command = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		command = "deadline"
	case err == nil:
		command, exit = "succeeded", "0"
	default:
		if code := CommandExitCode(err); code >= 0 && code <= 255 {
			exit = strconv.Itoa(code)
		}
	}
	return fmt.Sprintf("command=%s exit=%s", command, exit)
}

// Keep source errors and compared identities private. A classified rejection is
// still the original failure; it cannot authorize waiting, reselection or cleanup.
func nativeIdentityRecheckObservation(err error) string {
	cause, field, exit := "unclassified", "unavailable", "unavailable"
	var changed *nativeIdentityRuntimeChanged
	switch {
	case err == nil:
		cause = "unavailable"
	case errors.Is(err, ErrUnjoined):
		cause = "unjoined"
	case errors.Is(err, context.Canceled):
		cause = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		cause = "deadline"
	case errors.As(err, &changed):
		cause = "context-changed"
		field = nativeObservationLabel(changed.field, "controller-pod", "server-pod", "controller-container", "server-container", "config-object", "config-bytes", "host-boot", "config-projection")
	case errors.Is(err, errNativeIdentityContainerUnavailable):
		cause = "container-unavailable"
	case errors.Is(err, errNativeIdentityContainerMissing):
		cause = "container-missing"
	case nativeIdentityReadinessPending(err):
		cause = "unready"
	default:
		if code := CommandExitCode(err); code >= 0 && code <= 255 {
			cause, exit = "command-failed", strconv.Itoa(code)
		}
	}
	return fmt.Sprintf("Site identity runtime recheck observation (not acceptance): cause=%s field=%s exit=%s\n", cause, field, exit)
}

// Decode with the same bounded, exact codec as completion acceptance. Only
// closed fields are projected; no source error, identity or response text is
// copied. Unknown values stay unknown and cannot widen the acceptance contract.
func nativeIdentityResultObservation(raw []byte, commandErr error) string {
	envelope, completion, failure := "missing", "unavailable", "unavailable"
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	var result siteidentity.Result
	switch {
	case len(raw) > 64<<10:
		envelope = "oversized"
	case len(raw) == 0:
	case canonical.UnmarshalExact(raw, &result) != nil:
		envelope = "invalid"
	case result.SchemaVersion != siteidentity.SchemaVersion:
		envelope = "unsupported-schema"
	default:
		envelope, completion, failure = "valid", "unknown", "unknown"
		switch result.Completion {
		case siteidentity.NotStarted, siteidentity.JoinedFailure, siteidentity.JoinedSuccess, siteidentity.Unknown:
			completion = string(result.Completion)
		}
		switch result.FailureCode {
		case "":
			failure = "none"
		case nvkrerr.CodeUsage, nvkrerr.CodeInternal, nvkrerr.CodeEngineCapability, nvkrerr.CodeUnavailable, nvkrerr.CodeTimeout, nvkrerr.CodePreflightFailed, nvkrerr.CodeVerifyFailed:
			failure = string(result.FailureCode)
		}
	}
	return fmt.Sprintf("Site identity helper observation (not acceptance): %s envelope=%s completion=%s failure-code=%s\n", nativeIdentityCommandObservation(commandErr), envelope, completion, failure)
}
