// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"fmt"
	"slices"
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
				Terminated *struct {
					ExitCode, Signal *int
					Reason           string
				}
			}
		} `json:"containerStatuses"`
	}
}

// Describe only closed labels and bounded numbers from the failed probe. Raw
// Pod fields, host output, error values and identities remain private. This
// projection neither changes acceptance nor authorizes a retry.
func nativeCloudInitFailureSummary(podRaw, stdout []byte) string {
	closed := func(value string, allowed ...string) string {
		if slices.Contains(allowed, value) {
			return strings.ReplaceAll(strings.ReplaceAll(value, " - ", "-"), " ", "-")
		}
		return "unknown"
	}
	number := func(value *int, maximum int) string {
		if value == nil || *value < 0 || *value > maximum {
			return "unavailable"
		}
		return strconv.Itoa(*value)
	}
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
	phase := closed(pod.Status.Phase, "Pending", "Running", "Succeeded", "Failed", "Unknown")
	reason := closed(pod.Status.Reason, "DeadlineExceeded", "Evicted", "Shutdown", "Preempting", "UnexpectedAdmissionError")
	termination, exit, signal, restarts := "unavailable", "unavailable", "unavailable", "unavailable"
	if len(pod.Status.Containers) == 1 {
		container := pod.Status.Containers[0]
		restarts = number(container.RestartCount, 1_000_000)
		if container.State.Terminated != nil {
			terminated := container.State.Terminated
			termination = closed(terminated.Reason, "Completed", "Error", "OOMKilled", "ContainerCannotRun", "StartError")
			exit, signal = number(terminated.ExitCode, 255), number(terminated.Signal, 64)
		}
	}
	jsonStatus, status, extended, failures, recoverable, groups := "unavailable", "unavailable", "unavailable", "unavailable", "unavailable", "unavailable"
	if len(stdout) > 64<<10 {
		jsonStatus = "oversized"
	} else if len(strings.TrimSpace(string(stdout))) > 0 {
		jsonStatus = "invalid"
		if wire, err := nativeCloudInitDocument(stdout); err == nil {
			jsonStatus = "valid"
			status = closed(wire.Status, "not started", "running", "done", "disabled", "error")
			extended = closed(wire.ExtendedStatus, "not started", "running", "done", "disabled", "degraded done", "degraded running", "error - done", "error - running", "error - not started")
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
