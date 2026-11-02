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
