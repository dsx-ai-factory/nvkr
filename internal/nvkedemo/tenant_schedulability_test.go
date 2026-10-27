// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real Node-list JSON exercises the same encoding/json projection as both
// tenant wait paths. These are observations, not Kubernetes scheduling proof.
func schedulableTenantJSON(controlSpec, workerSpec string) []byte {
	return fmt.Appendf(nil, `{"items":[
{"metadata":{"name":"control-plane","uid":"control-plane-uid","labels":{"node-role.kubernetes.io/control-plane":""}},"spec":%s,"status":{"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"architecture":"arm64","kubeletVersion":"v1.34.2","bootID":"control-plane-boot"}}},
{"metadata":{"name":"worker","uid":"worker-uid"},"spec":%s,"status":{"conditions":[{"type":"Ready","status":"True"}],"nodeInfo":{"architecture":"arm64","kubeletVersion":"v1.34.2","bootID":"worker-boot"}}}
]}`, controlSpec, workerSpec)
}

func TestTenantReadyNodesRequiresSchedulableWorker(t *testing.T) {
	for _, test := range []struct {
		name, control, worker string
		ready                 bool
	}{
		{"unrestricted", `{}`, `{}`, true},
		{"worker cordoned", `{}`, `{"unschedulable":true}`, false},
		{"provisioning", `{}`, `{"taints":[{"key":"nke.nvidia.com/provisioning","value":"true","effect":"NoSchedule"}]}`, false},
		{"provisioning soft", `{}`, `{"taints":[{"key":"nke.nvidia.com/provisioning","effect":"PreferNoSchedule"}]}`, false},
		{"provisioning no effect", `{}`, `{"taints":[{"key":"nke.nvidia.com/provisioning"}]}`, false},
		{"unknown hard taint", `{}`, `{"taints":[{"key":"example.com/unavailable","effect":"NoSchedule"}]}`, false},
		{"worker NoExecute", `{}`, `{"taints":[{"key":"node.kubernetes.io/unreachable","effect":"NoExecute"}]}`, false},
		{"worker soft preference", `{}`, `{"taints":[{"key":"example.com/prefer-other","effect":"PreferNoSchedule"}]}`, true},
		{"worker mixed taints", `{}`, `{"taints":[{"key":"example.com/prefer-other","effect":"PreferNoSchedule"},{"key":"example.com/unavailable","effect":"NoSchedule"}]}`, false},
		{"control-plane NoSchedule", `{"taints":[{"key":"node-role.kubernetes.io/control-plane","effect":"NoSchedule"}]}`, `{}`, true},
		{"control-plane cordoned", `{"unschedulable":true}`, `{}`, true},
		{"provisioning lookalike soft", `{}`, `{"taints":[{"key":"nke.nvidia.com/provisioning-other","effect":"PreferNoSchedule"}]}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var nodes struct{ Items []tenantNode }
			if err := json.Unmarshal(schedulableTenantJSON(test.control, test.worker), &nodes); err != nil {
				t.Fatal(err)
			}
			before, err := json.Marshal(nodes)
			if err != nil {
				t.Fatal(err)
			}
			ready, err := readyNodesForCountVersion(nodes.Items, "1.34.2", 2)
			if err != nil || ready != test.ready {
				t.Fatalf("ready=%v err=%v, want ready=%v", ready, err, test.ready)
			}
			after, err := json.Marshal(nodes)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("readiness mutated node observations: %s -> %s, %v", before, after, err)
			}
		})
	}
}

func TestTenantReadyNodesWaitsForSourceToClearProvisioning(t *testing.T) {
	for _, worker := range []string{
		`{"taints":[{"key":"nke.nvidia.com/provisioning","value":"true","effect":"NoSchedule"}]}`,
		`{"taints":[{"key":"nke.nvidia.com/provisioning","value":"true","effect":"NoSchedule"}]}`,
		`{}`,
	} {
		var nodes struct{ Items []tenantNode }
		if err := json.Unmarshal(schedulableTenantJSON(`{"taints":[{"key":"node-role.kubernetes.io/control-plane","effect":"NoSchedule"}]}`, worker), &nodes); err != nil {
			t.Fatal(err)
		}
		ready, err := readyNodesForCountVersion(nodes.Items, "1.34.2", 2)
		if err != nil || ready != (worker == `{}`) {
			t.Fatalf("source transition worker=%s: ready=%v err=%v", worker, ready, err)
		}
	}
}

func TestTenantBlockedWorkerStillRejectsInvalidNodeGraph(t *testing.T) {
	for _, name := range []string{"duplicate name", "wrong architecture", "wrong kubelet", "two control planes", "no control plane"} {
		t.Run(name, func(t *testing.T) {
			var nodes struct{ Items []tenantNode }
			if err := json.Unmarshal(schedulableTenantJSON(`{}`, `{"unschedulable":true}`), &nodes); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "duplicate name":
				nodes.Items[1].Metadata.Name = nodes.Items[0].Metadata.Name
			case "wrong architecture":
				nodes.Items[1].Status.NodeInfo.Architecture = "amd64"
			case "wrong kubelet":
				nodes.Items[1].Status.NodeInfo.KubeletVersion = "v1.34.3"
			case "two control planes":
				nodes.Items[1].Metadata.Labels = map[string]string{"node-role.kubernetes.io/control-plane": ""}
			case "no control plane":
				nodes.Items[0].Metadata.Labels = nil
			}
			if ready, err := readyNodesForCountVersion(nodes.Items, "1.34.2", 2); ready || err == nil {
				t.Fatalf("blocked worker masked invalid graph: ready=%v err=%v", ready, err)
			}
		})
	}
}

type tenantSchedulabilityProgress struct {
	observed bool
	cancel   context.CancelFunc
	onNodes  func()
}

func (*tenantSchedulabilityProgress) Write(raw []byte) (int, error) { return len(raw), nil }
func (p *tenantSchedulabilityProgress) Report(event ProgressEvent) {
	if event.Kind == TenantWaiting && event.Name == "tenant-nodes" && event.ReadyNodes == 2 {
		p.observed = true
		if p.onNodes != nil {
			p.onNodes()
		}
		if p.cancel != nil {
			p.cancel()
		}
	}
}

func TestTenantRunBlockedReadyWorkerCannotSubmitWorkload(t *testing.T) {
	for _, name := range []string{"canceled", "deadline"} {
		t.Run(name, func(t *testing.T) {
			attempt, runner, root := nativeSessionFixture(t, false, 0)
			request, err := attempt.namedTenantRequest(t.Context(), "native-demo-tenant")
			if err != nil {
				t.Fatal(err)
			}
			raw := schedulableTenantJSON(`{"taints":[{"key":"node-role.kubernetes.io/control-plane","effect":"NoSchedule"}]}`, `{"taints":[{"key":"nke.nvidia.com/provisioning","value":"true","effect":"NoSchedule"}]}`)
			raw = []byte(strings.ReplaceAll(string(raw), "v1.34.2", "v"+request.Kubernetes))
			writeTenantFixture(t, filepath.Join(root, "nodes.json"), raw, 0o600)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			progress := &tenantSchedulabilityProgress{}
			if name == "canceled" {
				// Existing progress is reported after JSON decoding and readiness
				// evaluation, so cancel exactly when the real wait rejects the worker.
				progress.cancel = cancel
			}
			runner.Progress = progress
			attempt.tenantTimeout = 8 * time.Second
			err = attempt.runTenantRequest(ctx, runner, attempt.assets.CLI, attempt.assets.HostTools, runner.Work, filepath.Join(runner.Work, "site.kubeconfig"), request)
			cause := context.Canceled
			if name == "deadline" {
				cause = context.DeadlineExceeded
				if ctx.Err() != nil {
					t.Fatalf("tenant deadline must expire independently: %v", ctx.Err())
				}
			}
			if !progress.observed || !errors.Is(err, cause) || errors.Is(err, ErrUnjoined) {
				t.Fatalf("blocked Ready worker did not remain in bounded wait: observed=%v err=%v", progress.observed, err)
			}
			events, readErr := os.ReadFile(filepath.Join(root, "events"))
			if readErr != nil || !strings.HasSuffix(string(events), "nodes\n") || strings.Contains(string(events), "workload-") {
				t.Fatalf("blocked worker advanced to workload: %s, %v", events, readErr)
			}
			for _, file := range []string{"workload.json", "tenant-result.json"} {
				if _, statErr := os.Stat(filepath.Join(runner.Work, file)); !os.IsNotExist(statErr) {
					t.Fatalf("blocked worker produced %s: %v", file, statErr)
				}
			}
		})
	}
}

func TestTenantRunObservesSourceClearedWorkerBeforeWorkload(t *testing.T) {
	attempt, runner, root := nativeSessionFixture(t, false, 0)
	request, err := attempt.namedTenantRequest(t.Context(), "native-demo-tenant")
	if err != nil {
		t.Fatal(err)
	}
	control := `{"taints":[{"key":"node-role.kubernetes.io/control-plane","effect":"NoSchedule"}]}`
	blocked := schedulableTenantJSON(control, `{"unschedulable":true,"taints":[{"key":"nke.nvidia.com/provisioning","value":"true","effect":"NoSchedule"}]}`)
	blocked = []byte(strings.ReplaceAll(string(blocked), "v1.34.2", "v"+request.Kubernetes))
	path := filepath.Join(root, "nodes.json")
	writeTenantFixture(t, path, blocked, 0o600)
	cleared := false
	progress := &tenantSchedulabilityProgress{onNodes: func() {
		if cleared {
			return
		}
		// This callback runs synchronously after the existing wait evaluates
		// the first Ready-but-unavailable worker. Only the simulated source
		// changes the next API observation; the readiness predicate does not.
		events, err := os.ReadFile(filepath.Join(root, "events"))
		if err != nil || strings.Contains(string(events), "workload-") {
			t.Fatalf("workload submitted before source cleared worker: %s %v", events, err)
		}
		available := strings.ReplaceAll(string(schedulableTenantJSON(control, `{}`)), "v1.34.2", "v"+request.Kubernetes)
		writeTenantFixture(t, path, []byte(available), 0o600)
		cleared = true
	}}
	runner.Progress = progress
	attempt.tenantTimeout = 25 * time.Second
	if err := attempt.runTenantRequest(t.Context(), runner, attempt.assets.CLI, attempt.assets.HostTools, runner.Work, filepath.Join(runner.Work, "site.kubeconfig"), request); err != nil {
		t.Fatal(err)
	}
	events, err := os.ReadFile(filepath.Join(root, "events"))
	if err != nil || !cleared || !progress.observed || strings.Count(string(events), "nodes\n") != 4 || !strings.Contains(string(events), "workload-apply\n") {
		t.Fatalf("source-clear transition did not admit workload after readiness and cloud-init observations: %s %v", events, err)
	}
}

func TestTenantNotReadyPartialNodeInfoRemainsPending(t *testing.T) {
	for _, status := range []string{"False", "Unknown"} {
		t.Run(status, func(t *testing.T) {
			var nodes struct{ Items []tenantNode }
			if err := json.Unmarshal(schedulableTenantJSON(`{}`, `{}`), &nodes); err != nil {
				t.Fatal(err)
			}
			nodes.Items[1].Status.Conditions[0].Status = status
			nodes.Items[1].Status.NodeInfo.Architecture = ""
			nodes.Items[1].Status.NodeInfo.KubeletVersion = ""
			if ready, err := readyNodesForCountVersion(nodes.Items, "1.34.2", 2); ready || err != nil {
				t.Fatalf("transient non-Ready node must remain pending: ready=%v err=%v", ready, err)
			}
		})
	}
}
