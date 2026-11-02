// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNativeSiteContainerFailureObservationKeepsPrivateFieldsClosed(t *testing.T) {
	const raw = `{"items":[{"metadata":{"namespace":"z-PRIVATE","name":"PRIVATE_POD","uid":"PRIVATE_UID"},"status":{"containerStatuses":[{"name":"PRIVATE_CONTAINER","imageID":"PRIVATE_IMAGE","ready":false,"restartCount":1,"state":{"running":{"startedAt":"PRIVATE_TIME"}},"lastState":{"terminated":{"reason":"OOMKilled","exitCode":137,"signal":9,"message":"PRIVATE_ERROR","containerID":"PRIVATE_ID"}}}]}},{"metadata":{"namespace":"a-PRIVATE","name":"PRIVATE_OTHER"},"status":{"containerStatuses":[{"ready":false,"restartCount":0,"state":{"terminated":{"reason":"PRIVATE_REASON","exitCode":2,"signal":0}}}]}}]}`
	got := nativeSitePodFailureSummary([]byte(raw))
	for _, want := range []string{
		"pod=pod-001 container=container-001 ready=false state=terminated restarts=0 termination=unknown exit=2 signal=0",
		"pod=pod-002 container=container-001 ready=false state=running restarts=1 termination=unavailable",
		"last-termination=OOMKilled last-exit=137 last-signal=9",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing bounded observation %q: %q", want, got)
		}
	}
	if strings.Contains(got, "PRIVATE") {
		t.Fatal("private Pod fields reached the public projection")
	}
	var pods struct{ Items []json.RawMessage }
	if err := json.Unmarshal([]byte(raw), &pods); err != nil {
		t.Fatal(err)
	}
	pods.Items[0], pods.Items[1] = pods.Items[1], pods.Items[0]
	reordered, err := json.Marshal(pods)
	if err != nil || nativeSitePodFailureSummary(reordered) != got {
		t.Fatal("private API ordering changed anonymized Pod ordinals", err)
	}
}

func TestNativeSiteContainerFailureObservationBoundsAndUnavailableFields(t *testing.T) {
	const podPrefix = `{"items":[{"status":{"containerStatuses":[`
	const podSuffix = `]}}]}`
	for _, tc := range []struct{ name, raw, want string }{
		{"missing", "", "observation unavailable"},
		{"invalid", "PRIVATE_JSON", "observation unavailable"},
		{"duplicate", `{"items":[],"items":[]}`, "observation unavailable"},
		{"oversized", strings.Repeat("x", (4<<20)+1), "observation unavailable"},
		{"no Pods", `{"items":[]}`, "observation unavailable"},
		{"missing containers", `{"items":[{}]}`, "pod=pod-001 containers=unavailable"},
		{"missing state", podPrefix + `{}` + podSuffix, "ready=unavailable state=unknown restarts=unavailable"},
		{"waiting", podPrefix + `{"ready":false,"restartCount":3,"state":{"waiting":{"reason":"PRIVATE_REASON"}}}` + podSuffix, "state=waiting restarts=3"},
		{"bad numbers", podPrefix + `{"restartCount":-1,"state":{"terminated":{"reason":"Error","exitCode":-1,"signal":99}},"lastState":{"terminated":{"reason":"PRIVATE_LAST","exitCode":999,"signal":-1}}}` + podSuffix, "restarts=unavailable termination=Error exit=unavailable signal=unavailable last-termination=unknown last-exit=unavailable last-signal=unavailable"},
		{"ambiguous state", podPrefix + `{"state":{"running":{},"terminated":{"reason":"OOMKilled"}}}` + podSuffix, "observation unavailable"},
		{"ambiguous previous state", podPrefix + `{"lastState":{"waiting":{},"terminated":{"reason":"OOMKilled"}}}` + podSuffix, "observation unavailable"},
		{"healthy", podPrefix + `{"ready":true,"restartCount":0,"state":{"running":{}}}` + podSuffix, "no unready or restarted containers observed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nativeSitePodFailureSummary([]byte(tc.raw))
			if !strings.Contains(got, tc.want) || strings.Contains(got, "PRIVATE") || len(got) > 32<<10 {
				t.Fatalf("unsafe or incomplete observation: %q", got)
			}
		})
	}
	for _, tc := range []struct {
		name             string
		pods, containers int
	}{
		{"Pod bound", 129, 1}, {"container bound", 1, 33}, {"output bound", 128, 32}, {"presenter bound", 8, 24},
	} {
		t.Run(tc.name, func(t *testing.T) {
			container := `{"ready":false,"restartCount":1,"state":{"running":{}},"lastState":{"terminated":{"reason":"OOMKilled","exitCode":137,"signal":9}}}`
			pod := `{"status":{"containerStatuses":[` + strings.TrimSuffix(strings.Repeat(container+",", tc.containers), ",") + `]}}`
			raw := `{"items":[` + strings.TrimSuffix(strings.Repeat(pod+",", tc.pods), ",") + `]}`
			got := nativeSitePodFailureSummary([]byte(raw))
			if !strings.Contains(got, "observation unavailable") || len(got) > 256 {
				t.Fatal("oversized observation escaped its bound")
			}
			var progress bootstrapObservationRecorder
			reportNativeBootstrapObservation(Runner{Progress: &progress}, got)
			if progress.String() != got {
				t.Fatal("bounded unavailable observation was lost at its delivery boundary")
			}
		})
	}
}
