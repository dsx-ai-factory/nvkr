// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNativeCloudInitFailureObservationKeepsRawValuesPrivate(t *testing.T) {
	const raw = `{"metadata":{"name":"PRIVATE_NODE","uid":"PRIVATE_UID"},"status":{"phase":"Failed","reason":"PRIVATE_POD_REASON","containerStatuses":[{"name":"status","imageID":"PRIVATE_IMAGE","containerID":"PRIVATE_CONTAINER","restartCount":0,"state":{"terminated":{"exitCode":2,"signal":0,"reason":"Error","message":"PRIVATE_TERMINATION"}}}]}}`
	stdout := []byte(`{"status":"done","extended_status":"degraded done","errors":[],"recoverable_errors":{"PRIVATE_KEY":["PRIVATE_ERROR","PRIVATE_TOKEN"]},"detail":"PRIVATE_DETAILS"}`)
	got := nativeCloudInitFailureSummary([]byte(raw), stdout)
	for _, want := range []string{"phase=Failed reason=unknown containers=1", "termination=Error exit=2 signal=0 restarts=0", "json=valid status=done extended=degraded-done errors=0 recoverable=2 recoverable-groups=1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing safe field %q in %q", want, got)
		}
	}
	if strings.Contains(got, "PRIVATE") || len(got) > 1024 {
		t.Fatal("raw private values reached the bounded observation")
	}
}

func TestNativeCloudInitFailureObservationDistinguishesUnavailableEvidence(t *testing.T) {
	for _, tc := range []struct{ name, pod, stdout, want string }{
		{"deadline before container", `{"status":{"phase":"Failed","reason":"DeadlineExceeded"}}`, "", "phase=Failed reason=DeadlineExceeded containers=0 termination=unavailable exit=unavailable"},
		{"missing Pod", "", "", "pod-json=unavailable"},
		{"invalid Pod", "PRIVATE_POD", "", "pod-json=invalid"},
		{"oversized Pod", strings.Repeat("x", (64<<10)+1), "", "pod-json=oversized"},
		{"duplicate Pod", `{"status":{"phase":"Failed"},"status":{"phase":"PRIVATE_PHASE"}}`, "", "pod-json=invalid phase=unknown"},
		{"evicted", `{"status":{"phase":"Failed","reason":"Evicted"}}`, "", "reason=Evicted"},
		{"missing container status", `{"status":{"phase":"Succeeded"}}`, "", "phase=Succeeded reason=unknown containers=0"},
		{"unknown phase", `{"status":{"phase":"PRIVATE_PHASE","reason":"PRIVATE_REASON"}}`, "", "phase=unknown reason=unknown"},
		{"OOM", `{"status":{"phase":"Failed","containerStatuses":[{"state":{"terminated":{"exitCode":137,"signal":9,"reason":"OOMKilled"}}}]}}`, "", "termination=OOMKilled exit=137 signal=9 restarts=unavailable"},
		{"unknown termination", `{"status":{"phase":"Failed","containerStatuses":[{"restartCount":-1,"state":{"terminated":{"exitCode":-1,"signal":99,"reason":"PRIVATE_TERMINATION"}}}]}}`, "", "termination=unknown exit=unavailable signal=unavailable restarts=unavailable"},
		{"no output", `{}`, "", "json=unavailable status=unavailable"},
		{"invalid JSON", `{}`, "PRIVATE_TRACEBACK", "json=invalid status=unavailable"},
		{"oversized JSON", `{}`, strings.Repeat("x", (64<<10)+1), "json=oversized status=unavailable"},
		{"duplicate JSON", `{}`, `{"status":"done","status":"PRIVATE_STATUS"}`, "json=invalid status=unavailable"},
		{"missing collections", `{}`, `{"status":"done","extended_status":"done"}`, "json=valid status=done extended=done errors=unavailable recoverable=unavailable recoverable-groups=unavailable"},
		{"null nested collection", `{}`, `{"recoverable_errors":{"PRIVATE_KEY":null}}`, "recoverable=unavailable recoverable-groups=1"},
		{"empty collections", `{}`, cleanNativeCloudInit, "errors=0 recoverable=0 recoverable-groups=0"},
		{"unknown status", `{}`, `{"status":"PRIVATE_STATUS","extended_status":"PRIVATE_EXTENDED"}`, "json=valid status=unknown extended=unknown"},
		{"error status", `{}`, `{"status":"error","extended_status":"error - done","errors":["PRIVATE_ERROR"],"recoverable_errors":{}}`, "status=error extended=error-done errors=1 recoverable=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nativeCloudInitFailureSummary([]byte(tc.pod), []byte(tc.stdout))
			if !strings.Contains(got, tc.want) || strings.Contains(got, "PRIVATE") || len(got) > 1024 {
				t.Fatalf("unsafe or incomplete observation: %q; wanted %q", got, tc.want)
			}
		})
	}
}

func TestNativeCloudInitFailurePreservesCauseAndIgnoresOrdinaryWriters(t *testing.T) {
	original := errors.New("PRIVATE_COMMAND_ERROR")
	writer := &recordedProgress{}
	err := nativeCloudInitProbe(t.Context(), privateTestDir(t), "private-namespace", "private-pod", "private-node", "example.invalid/private@sha256:"+strings.Repeat("a", 64), Runner{Progress: writer},
		func(context.Context, string, ...string) ([]byte, error) { return nil, original })
	if !errors.Is(err, original) || writer.Len() != 0 || len(writer.events) != 0 {
		t.Fatal("diagnostics changed the cause or reached an ordinary writer")
	}
}
