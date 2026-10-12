// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRuntimeReadinessNamesContainerWithoutPrivateSubject(t *testing.T) {
	for _, terminated := range []bool{false, true} {
		t.Run(map[bool]string{false: "not-ready", true: "crashed"}[terminated], func(t *testing.T) {
			rows, refs := runtimeFixture()
			for i := range rows.Items {
				pod := &rows.Items[i]
				if pod.Metadata.UID != "temporal-servertemporal-worker" {
					continue
				}
				pod.Metadata.Name = "private-worker-pod"
				pod.Status.ContainerStatuses[0].Ready = false
				if terminated {
					pod.Status.ContainerStatuses[0].State.Running = nil
					exit := 1
					pod.Status.ContainerStatuses[0].State.Terminated = &struct{ ExitCode *int }{&exit}
				}
			}
			_, err := validateRuntimePods(rows, refs)
			var failed *runtimeReadinessError
			if !errors.As(err, &failed) || failed.pod.Name != "private-worker-pod" || failed.container != "temporal-worker" {
				t.Fatalf("failure subject lost: %v", err)
			}
			if !strings.Contains(err.Error(), "temporal-worker") || strings.Contains(err.Error(), "private-worker-pod") {
				t.Fatalf("public failure missing container or leaking subject: %v", err)
			}
			if terminated && !strings.Contains(err.Error(), "exit code 1") {
				t.Fatal("crash exit status lost")
			}
		})
	}
}

func TestRuntimeFailureRetainsPrivateLogsBeforeCleanup(t *testing.T) {
	for _, scenario := range []string{"exact", "changed-uid", "get-failed", "logs-failed", "canceled-work", "missing-subject"} {
		t.Run(scenario, func(t *testing.T) {
			work, tools := privateTestDir(t), privateTestDir(t)
			pod := sitePod{Metadata: podMetadata{UID: "private-uid", Namespace: "temporal", Name: "private-pod"}}
			failed := &runtimeReadinessError{dependency: "runtime-temporal-server", pod: pod.Metadata, container: "temporal-worker"}
			if scenario == "changed-uid" {
				pod.Metadata.UID = "replacement-uid"
			}
			if scenario == "missing-subject" {
				failed.pod.Name = ""
			}
			raw, err := json.Marshal(pod)
			if err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(tools, "calls")
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$CALLS"
case "$*" in
  *" get "*)
    [ "$SCENARIO" != get-failed ] || exit 7
    printf '%s' "$POD";;
  *" logs "*)
    case "$*" in
      *--previous=true*) printf private-previous;;
      *) printf private-current;;
    esac
    printf private-stderr >&2
    [ "$SCENARIO" != logs-failed ] || exit 8;;
  *) exit 42;;
esac
`
			if err := os.WriteFile(filepath.Join(tools, "kubectl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			writer := &recordedProgress{}
			session := ManagementSession{Tools: tools, Work: work, Runner: Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin", "POD=" + string(raw), "CALLS=" + calls, "SCENARIO=" + scenario, "SITE_KUBECONFIG=" + filepath.Join(work, "private-kube")}, Progress: writer, output: writer}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled-work" {
				cancel()
			}
			result := retainRuntimeFailure(ctx, session, failed)
			if !errors.Is(result, failed) || errors.Is(result, context.DeadlineExceeded) {
				t.Fatalf("diagnostics changed the primary failure: %v", result)
			}
			if writer.Len() != 0 || len(writer.events) != 0 || strings.Contains(result.Error(), "private-pod") || strings.Contains(result.Error(), "private-stderr") {
				t.Fatal("private diagnostics leaked to public output")
			}
			collected := scenario == "exact" || scenario == "logs-failed" || scenario == "canceled-work"
			for _, phase := range []string{"current", "previous"} {
				path := filepath.Join(work, "runtime-failure-"+phase+".log")
				body, err := os.ReadFile(path)
				if !collected {
					if !os.IsNotExist(err) {
						t.Fatal("logs collected without the original pod identity")
					}
					continue
				}
				if err != nil || !strings.Contains(string(body), "private-"+phase) || !strings.Contains(string(body), "private-stderr") {
					t.Fatalf("container diagnostics not retained: %q %v", body, err)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatal("diagnostic file is not private")
				}
			}
			if collected {
				commands, err := os.ReadFile(calls)
				if err != nil || strings.Count(string(commands), "\n") != 3 || !strings.Contains(string(commands), "--limit-bytes=262144") || !strings.Contains(string(commands), "--tail=100") || !strings.Contains(string(commands), "--request-timeout=5s") {
					t.Fatalf("unbounded or repeated diagnostics: %s %v", commands, err)
				}
			}
		})
	}
}

func TestRuntimeDiagnosticFailurePreservesPrimaryClassification(t *testing.T) {
	primary := errors.New("primary readiness failure")
	for _, diagnostic := range []error{context.DeadlineExceeded, context.Canceled, ErrUnjoined} {
		result := runtimeDiagnosticFailure(primary, diagnostic)
		if !errors.Is(result, primary) || errors.Is(result, ErrUnjoined) != errors.Is(diagnostic, ErrUnjoined) || errors.Is(result, context.DeadlineExceeded) || errors.Is(result, context.Canceled) {
			t.Fatalf("diagnostic result replaced primary failure or lost unjoined ownership: %v", result)
		}
	}
}
