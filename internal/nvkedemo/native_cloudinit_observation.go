// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
)

// Keep observational fields separate from nativeCloudInitPod: extending that
// acceptance projection would change its existing after-stdout equality check.
type nativeCloudInitFailurePod struct {
	Status struct {
		Phase, Reason string
		Containers    []struct {
			RestartCount *int
			State        struct {
				Terminated *nativeObservedTermination
			}
		} `json:"containerStatuses"`
	}
}

// Describe only closed labels and bounded numbers from the failed probe. Raw
// Pod fields, host output, error values and identities remain private. This
// projection neither changes acceptance nor authorizes a retry.
func nativeCloudInitFailureSummary(podRaw, stdout []byte) string {
	var pod nativeCloudInitFailurePod
	podJSON := "unavailable"
	if len(podRaw) > 64<<10 {
		podJSON = "oversized"
	} else if len(podRaw) > 0 {
		podJSON = "invalid"
		if canonical.ValidateJSONDocument(podRaw) == nil && json.Unmarshal(podRaw, &pod) == nil {
			podJSON = "valid"
		} else {
			pod = nativeCloudInitFailurePod{}
		}
	}
	phase := nativeObservationLabel(pod.Status.Phase, "Pending", "Running", "Succeeded", "Failed", "Unknown")
	reason := nativeObservationLabel(pod.Status.Reason, "DeadlineExceeded", "Evicted", "Shutdown", "Preempting", "UnexpectedAdmissionError")
	termination, exit, signal, restarts := "unavailable", "unavailable", "unavailable", "unavailable"
	if len(pod.Status.Containers) == 1 {
		container := pod.Status.Containers[0]
		restarts = nativeObservationNumber(container.RestartCount, 1_000_000)
		termination, exit, signal = nativeTerminationObservation(container.State.Terminated)
	}
	jsonStatus, status, extended, failures, recoverable, groups := "unavailable", "unavailable", "unavailable", "unavailable", "unavailable", "unavailable"
	if len(stdout) > 64<<10 {
		jsonStatus = "oversized"
	} else if len(strings.TrimSpace(string(stdout))) > 0 {
		jsonStatus = "invalid"
		if wire, err := nativeCloudInitDocument(stdout); err == nil {
			jsonStatus = "valid"
			status = nativeObservationLabel(wire.Status, "not started", "running", "done", "disabled", "error")
			extended = nativeObservationLabel(wire.ExtendedStatus, "not started", "running", "done", "disabled", "degraded done", "degraded running", "error - done", "error - running", "error - not started")
			if wire.Errors != nil {
				failures = strconv.Itoa(len(wire.Errors))
			}
			if wire.RecoverableErrors != nil {
				groups = strconv.Itoa(len(wire.RecoverableErrors))
				count := 0
				complete := true
				for _, values := range wire.RecoverableErrors {
					complete = complete && values != nil
					count += len(values)
				}
				if complete {
					recoverable = strconv.Itoa(count)
				}
			}
		}
	}
	return fmt.Sprintf("Cloud-init failure observation (not acceptance): pod-json=%s phase=%s reason=%s containers=%d termination=%s exit=%s signal=%s restarts=%s json=%s status=%s extended=%s errors=%s recoverable=%s recoverable-groups=%s\n",
		podJSON, phase, reason, len(pod.Status.Containers), termination, exit, signal, restarts, jsonStatus, status, extended, failures, recoverable, groups)
}
