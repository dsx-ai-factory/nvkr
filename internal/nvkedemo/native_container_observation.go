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

// These are presentation projections, never container or execution receipts.
// Kubernetes owns these status fields; arbitrary messages and identities stay private.
type nativeObservedTermination struct {
	ExitCode, Signal *int
	Reason           string
}

func nativeObservationLabel(value string, allowed ...string) string {
	if slices.Contains(allowed, value) {
		return strings.ReplaceAll(strings.ReplaceAll(value, " - ", "-"), " ", "-")
	}
	return "unknown"
}

func nativeObservationNumber(value *int, maximum int) string {
	if value == nil || *value < 0 || *value > maximum {
		return "unavailable"
	}
	return strconv.Itoa(*value)
}

func nativeTerminationObservation(value *nativeObservedTermination) (reason, exit, signal string) {
	if value == nil {
		return "unavailable", "unavailable", "unavailable"
	}
	return nativeObservationLabel(value.Reason, "Completed", "Error", "OOMKilled", "ContainerCannotRun", "StartError"), nativeObservationNumber(value.ExitCode, 255), nativeObservationNumber(value.Signal, 64)
}

// Decode the already captured failure snapshot separately from acceptance and
// walkthrough projections. Pod ordinals use the same private sort as the table.
// No extra request, retry, raw reason, container name or identity is published.
func nativeSitePodFailureSummary(raw []byte) string {
	type state struct {
		Running    *struct{}
		Waiting    *struct{}
		Terminated *nativeObservedTermination
	}
	type pod struct {
		Metadata walkthroughMetadata
		Status   struct {
			Containers []struct {
				Ready        *bool
				RestartCount *int
				State        state
				LastState    state
			} `json:"containerStatuses"`
		}
	}
	const unavailable = "Site container failure observation unavailable; original failure unchanged.\n"
	var pods struct{ Items []pod }
	if len(raw) == 0 || len(raw) > 4<<20 || canonical.ValidateJSONDocument(raw) != nil || json.Unmarshal(raw, &pods) != nil || len(pods.Items) == 0 || len(pods.Items) > 128 {
		return unavailable
	}
	slices.SortFunc(pods.Items, func(a, b pod) int {
		return strings.Compare(a.Metadata.Namespace+"/"+a.Metadata.Name, b.Metadata.Namespace+"/"+b.Metadata.Name)
	})
	var text strings.Builder
	for index, item := range pods.Items {
		if len(item.Status.Containers) > 32 {
			return unavailable
		}
		if len(item.Status.Containers) == 0 {
			fmt.Fprintf(&text, "Site container failure observation (not acceptance): pod=pod-%03d containers=unavailable\n", index+1)
		}
		for containerIndex, container := range item.Status.Containers {
			for _, observed := range []state{container.State, container.LastState} {
				states := 0
				if observed.Running != nil {
					states++
				}
				if observed.Waiting != nil {
					states++
				}
				if observed.Terminated != nil {
					states++
				}
				if states > 1 {
					return unavailable
				}
			}
			if container.Ready != nil && *container.Ready && container.RestartCount != nil && *container.RestartCount == 0 && container.State.Running != nil && container.LastState.Terminated == nil {
				continue
			}
			ready := "unavailable"
			if container.Ready != nil {
				ready = strconv.FormatBool(*container.Ready)
			}
			current := "unknown"
			switch {
			case container.State.Running != nil && container.State.Waiting == nil && container.State.Terminated == nil:
				current = "running"
			case container.State.Waiting != nil && container.State.Running == nil && container.State.Terminated == nil:
				current = "waiting"
			case container.State.Terminated != nil && container.State.Running == nil && container.State.Waiting == nil:
				current = "terminated"
			}
			reason, exit, signal := nativeTerminationObservation(container.State.Terminated)
			lastReason, lastExit, lastSignal := nativeTerminationObservation(container.LastState.Terminated)
			fmt.Fprintf(&text, "Site container failure observation (not acceptance): pod=pod-%03d container=container-%03d ready=%s state=%s restarts=%s termination=%s exit=%s signal=%s last-termination=%s last-exit=%s last-signal=%s\n", index+1, containerIndex+1, ready, current, nativeObservationNumber(container.RestartCount, 1_000_000), reason, exit, signal, lastReason, lastExit, lastSignal)
			if text.Len() > 32<<10 {
				return unavailable
			}
		}
	}
	if text.Len() == 0 {
		return "Site container failure observation (not acceptance): no unready or restarted containers observed.\n"
	}
	return text.String()
}
