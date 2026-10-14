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
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestRuntimeStartupConvergence(t *testing.T) {
	for _, name := range []string{"ready", "converges", "timeout", "canceled", "cancel-during-observation", "mismatch", "pending-and-mismatch", "observer-error", "unjoined"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			rows, refs := runtimeFixture()
			calls := 0
			result, err := waitRuntimePods(ctx, refs, 40*time.Millisecond, time.Millisecond, func(context.Context) (podList, error) {
				calls++
				if name == "observer-error" {
					return podList{}, errors.New("capture failed")
				}
				if name == "unjoined" {
					return podList{}, ErrUnjoined
				}
				if name == "cancel-during-observation" {
					cancel()
				}
				for i := range rows.Items {
					p := &rows.Items[i]
					if p.Metadata.UID == "csi-node-driver-registrarnode-driver-registrar" && (name == "timeout" || name == "pending-and-mismatch" || name == "converges" && calls == 1) {
						p.Status.ContainerStatuses[0].Ready = false
					} else if p.Metadata.UID == "csi-node-driver-registrarnode-driver-registrar" {
						p.Status.ContainerStatuses[0].Ready = true
					}
					if p.Metadata.UID == "temporal-servertemporal-worker" && (name == "mismatch" || name == "pending-and-mismatch") {
						p.Status.ContainerStatuses[0].ImageID = "foreign"
					}
				}
				return rows, nil
			})
			if name == "ready" || name == "converges" {
				want := 1
				if name == "converges" {
					want = 2
				}
				if err != nil || len(result) != len(runtimeImages) || calls != want {
					t.Fatalf("startup result=%d calls=%d err=%v", len(result), calls, err)
				}
				return
			}
			var coded *nvkrerr.Error
			if err == nil || result != nil || !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed {
				t.Fatalf("startup failure lost code or returned results: %v", err)
			}
			if name == "timeout" {
				var readiness *runtimeReadinessError
				if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &readiness) || calls < 2 {
					t.Fatalf("timeout lost final readiness: calls=%d %v", calls, err)
				}
			} else if name == "canceled" {
				if calls != 0 || !errors.Is(err, context.Canceled) {
					t.Fatal("canceled context performed work", calls, err)
				}
			} else if calls != 1 {
				t.Fatal("terminal failure retried", calls, err)
			}
			if name == "unjoined" && !errors.Is(err, ErrUnjoined) {
				t.Fatal("lost child join obligation", err)
			}
		})
	}
}

func TestTemporalFailureRetainsFrontendAndServiceDiagnostics(t *testing.T) {
	for _, scenario := range []string{"exact", "ambiguous", "frontend-logs-fail"} {
		t.Run(scenario, func(t *testing.T) {
			work, tools := privateTestDir(t), privateTestDir(t)
			worker := sitePod{Metadata: podMetadata{Name: "private-worker", UID: "worker-uid", Namespace: "temporal"}}
			frontend := sitePod{Metadata: podMetadata{Name: "private-frontend", UID: "frontend-uid", Namespace: "temporal"}}
			pods := podList{Items: []sitePod{frontend}}
			if scenario == "ambiguous" {
				pods.Items = append(pods.Items, frontend)
			}
			workerJSON, _ := json.Marshal(worker)
			frontendJSON, _ := json.Marshal(pods)
			calls := filepath.Join(tools, "calls")
			script := `#!/bin/sh
printf '%s\n' "$*" >> "$CALLS"
case "$*" in
 *" get pod/private-worker "*) printf '%s' "$WORKER";;
 *" get pods "*) printf '%s' "$FRONTEND";;
 *" get service/temporal-frontend "*|*" get endpointslices "*) printf '{"items":[]}' ;;
 *" logs "*)
   printf private-runtime-log
   case "$*" in *private-frontend*) [ "$SCENARIO" != frontend-logs-fail ] || exit 8;; esac;;
 *) exit 42;;
esac
`
			if err := os.WriteFile(filepath.Join(tools, "kubectl"), []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			writer := &recordedProgress{}
			session := ManagementSession{Tools: tools, Work: work, Runner: Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin", "WORKER=" + string(workerJSON), "FRONTEND=" + string(frontendJSON), "CALLS=" + calls, "SCENARIO=" + scenario}, Progress: writer, output: writer}}
			cause := &runtimeReadinessError{dependency: "runtime-temporal-server", pod: worker.Metadata, container: "temporal-worker"}
			result := retainRuntimeFailure(t.Context(), session, cause)
			if !errors.Is(result, cause) || writer.Len() != 0 || len(writer.events) != 0 || strings.Contains(result.Error(), "private-worker") || strings.Contains(result.Error(), "private-runtime-log") {
				t.Fatal("diagnostics lost primary cause or leaked private data", result)
			}
			for _, phase := range []string{"runtime-failure-current", "runtime-failure-previous", "runtime-failure-frontend-service", "runtime-failure-frontend-endpointslices", "runtime-failure-frontend-identity", "runtime-failure-frontend-current", "runtime-failure-frontend-previous"} {
				info, err := os.Stat(filepath.Join(work, phase+".log"))
				if scenario == "ambiguous" && (phase == "runtime-failure-frontend-current" || phase == "runtime-failure-frontend-previous") {
					if !os.IsNotExist(err) {
						t.Fatal("ambiguous frontend logs collected")
					}
					continue
				}
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatal("missing or non-private diagnostic", phase, err)
				}
			}
		})
	}
}
