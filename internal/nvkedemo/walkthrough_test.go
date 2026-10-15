// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func walkthroughFixture(t *testing.T) tenant {
	t.Helper()
	access, runner := operatorStatusFixture(t)
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$FIXTURE/events"
test "$1" = --kubeconfig
shift 2
test "$1" = --request-timeout=10s
shift
if test "$1" = -n; then shift 2; fi
case "$1 $2" in
  'get nodes') if test "$4" = wide; then printf 'private-node-address\n'; else printf '%s\n' "$NODES_JSON"; fi ;;
  'get pods') printf '%s\n' "$PODS_JSON" ;;
  'get deployments,daemonsets,statefulsets') printf '%s\n' "$WORKLOADS_JSON" ;;
  'rollout status') test "${FAIL:-}" != rollout ;;
  'get job') printf '%s\n' "$JOB_JSON" ;;
  'logs job/nvkr-smoke') printf 'private-DNS-address\nNVKE_LOCAL_WORKLOAD_PASS\n' ;;
  *) exit 90 ;;
esac
`
	if err := os.WriteFile(access.kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runner.Env = SetEnv(runner.Env, map[string]string{
		"FIXTURE":         runner.Work,
		"SITE_KUBECONFIG": access.site,
		"PODS_JSON":       `{"items":[{"metadata":{"name":"private-pod-name","namespace":"default","ownerReferences":[{"uid":"private-job-id"}]},"status":{"phase":"Succeeded","containerStatuses":[{"restartCount":0,"imageID":"` + WorkloadImage + `","state":{"terminated":{"exitCode":0}}}]}}]}`,
		"WORKLOADS_JSON":  `{"items":[{"kind":"Deployment","metadata":{"namespace":"kube-system","name":"coredns"},"spec":{"replicas":1},"status":{"readyReplicas":1,"updatedReplicas":1}},{"kind":"StatefulSet","metadata":{"namespace":"nke-system","name":"postgresql"},"spec":{"replicas":1},"status":{"readyReplicas":1,"updatedReplicas":1}}]}`,
		"JOB_JSON":        `{"metadata":{"name":"nvkr-smoke","namespace":"default","uid":"private-job-id"},"spec":{"backoffLimit":0,"template":{"spec":{"restartPolicy":"Never"}}},"status":{"active":0,"failed":0,"succeeded":1,"conditions":[{"type":"Complete","status":"True"}]}}`,
	})
	return tenant{network: "ovn", session: ManagementSession{Work: runner.Work, Tools: filepath.Dir(access.kubectl), Runner: runner}}
}

func TestWalkthroughPreservesAcceptedWorkloadImageIDForms(t *testing.T) {
	digest := strings.Split(WorkloadImage, "@")[1]
	for _, test := range []struct {
		name, imageID string
		accepted      bool
	}{
		{"bare digest", digest, true},
		{"repository at digest", WorkloadImage, true},
		{"final digest semantics", "prefix@middle@" + digest, true},
		{"wrong bare digest", "sha256:" + strings.Repeat("0", 64), false},
		{"wrong repository digest", "docker.io/library/alpine@sha256:" + strings.Repeat("0", 64), false},
		{"unsupported containerd URI", "containerd://" + digest, false},
		{"empty", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			tenant := walkthroughFixture(t)
			var job walkthroughJob
			if err := json.Unmarshal([]byte(environmentValue(tenant.session.Runner.Env, "JOB_JSON")), &job); err != nil {
				t.Fatal(err)
			}
			var response struct{ Items []walkthroughPod }
			if err := json.Unmarshal([]byte(environmentValue(tenant.session.Runner.Env, "PODS_JSON")), &response); err != nil {
				t.Fatal(err)
			}
			response.Items[0].Status.Containers[0].ImageID = test.imageID
			if actual := walkthroughJobPassed(job, response.Items); actual != test.accepted {
				t.Fatal("walkthrough differs from tenant acceptance", actual, test.accepted)
			}
		})
	}
}

func TestRecordedWalkthroughUsesBothOwnedConfigsAndExistingJobOnly(t *testing.T) {
	tenant := walkthroughFixture(t)
	var out bytes.Buffer
	if err := tenant.recordedWalkthrough(t.Context(), &out); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"Site / Kind management", "Tenant / QCOW2", "get nodes -o wide", "get pods -A", "deployments,daemonsets,statefulsets", "Observed rollout complete", "container exit=0; restarts=0", "NVKE_LOCAL_WORKLOAD_PASS", "no Job was rerun", "natural tenant deletion"} {
		if !strings.Contains(out.String(), expected) {
			t.Fatal("missing live observation", expected, &out)
		}
	}
	for _, private := range []string{"private-pod-name", "private-job-id", "private-node-address", "private-DNS-address", tenant.session.Work} {
		if strings.Contains(out.String(), private) {
			t.Fatal("private output reached presentation", private)
		}
	}
	events, err := os.ReadFile(filepath.Join(tenant.session.Work, "events"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(events), "--kubeconfig "+environmentValue(tenant.session.Runner.Env, "SITE_KUBECONFIG")) || !strings.Contains(string(events), "--kubeconfig "+filepath.Join(tenant.session.Work, "tenant-kubeconfig")) {
		t.Fatal("ambient or incorrect cluster context selected")
	}
	for _, forbidden := range []string{" apply ", " create ", " delete ", " rollout restart ", " exec "} {
		if strings.Contains(string(events), forbidden) {
			t.Fatal("walkthrough mutated cluster", forbidden)
		}
	}
	if strings.Count(string(events), "logs job/nvkr-smoke") != 1 {
		t.Fatal("existing Job log was not observed exactly once")
	}
}

func TestRecordedWalkthroughFailuresAreVisibleAndDoNotClaimCompletion(t *testing.T) {
	for _, test := range []struct{ name, key, value string }{
		{"rollout", "FAIL", "rollout"},
		{"empty nodes", "NODES_JSON", `{"items":[]}`},
		{"Job retry", "JOB_JSON", `{"metadata":{"name":"nvkr-smoke","namespace":"default","uid":"private-job-id"},"spec":{"backoffLimit":1},"status":{"succeeded":1}}`},
		{"pod restart", "PODS_JSON", `{"items":[{"metadata":{"namespace":"default"},"status":{"phase":"Succeeded","containerStatuses":[{"restartCount":1}]}}]}`},
		{"terminal controls", "WORKLOADS_JSON", `{"items":[{"kind":"Deployment","metadata":{"name":"\u001b[31m","namespace":"kube-system"}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			tenant := walkthroughFixture(t)
			tenant.session.Runner.Env = SetEnv(tenant.session.Runner.Env, map[string]string{test.key: test.value})
			var out bytes.Buffer
			if err := tenant.recordedWalkthrough(t.Context(), &out); err == nil {
				t.Fatal("failed observation accepted")
			}
			if !strings.Contains(out.String(), "Walkthrough failed") || strings.Contains(out.String(), "observations complete") || strings.ContainsRune(out.String(), '\x1b') {
				t.Fatal("failure hidden or unsafe display", &out)
			}
		})
	}
}

func TestRecordedWalkthroughCancellationPreservesCause(t *testing.T) {
	tenant := walkthroughFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var out bytes.Buffer
	if err := tenant.recordedWalkthrough(ctx, &out); !errors.Is(err, context.Canceled) {
		t.Fatal("lost cancellation", err)
	}
}
