// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"fmt"
	"testing"
)

func nativeTopologyNodes(count int) []tenantNode {
	nodes := make([]tenantNode, count)
	for i := range nodes {
		nodes[i].Metadata.Name = fmt.Sprintf("owned-%d", i)
		nodes[i].Metadata.UID = fmt.Sprintf("node-uid-%d", i)
		nodes[i].Metadata.Labels = map[string]string{}
		nodes[i].Status.NodeInfo.Architecture = "arm64"
		nodes[i].Status.NodeInfo.KubeletVersion = "v1.34.2"
		nodes[i].Status.NodeInfo.BootID = fmt.Sprintf("boot-id-%d", i)
		nodes[i].Status.Conditions = append(nodes[i].Status.Conditions, struct{ Type, Status string }{"Ready", "True"})
	}
	if count > 0 {
		nodes[0].Metadata.Labels["node-role.kubernetes.io/control-plane"] = ""
	}
	return nodes
}

func TestNativeRequestedTopologyUsesOneWorkerPool(t *testing.T) {
	for _, subnet := range []string{"192.168.64.0/24", GuestCIDR} {
		for _, tc := range []struct {
			workers, nodes int
			conformance    bool
		}{{0, 2, false}, {1, 2, false}, {2, 3, false}, {0, 3, true}, {2, 3, true}} {
			t.Run(fmt.Sprintf("%s/workers-%d/conformance-%v", subnet, tc.workers, tc.conformance), func(t *testing.T) {
				attempt := nativeCAPITenantFixture(t, subnet, "ovn")
				if tc.conformance {
					attempt.conformance = &nativeConformanceConfig{}
				}
				var err error
				attempt.tenantNodes, err = selectedNativeTenantNodes(tc.workers, tc.conformance)
				if err != nil {
					t.Fatal(err)
				}
				request, err := attempt.namedTenantRequest(t.Context(), "native-demo-tenant")
				if err != nil {
					t.Fatal(err)
				}
				expected := tc.nodes
				if request.ControlPlane.Count != 1 || len(request.NodePools) != 1 || request.NodePools[0].Count != expected-1 || request.NodePools[0].ControlPlane || request.NodePools[0].Name != "worker" || attempt.tenantNodeCount() != expected || nativeGuestLimit(&attempt) != expected+1 {
					t.Fatalf("wrong native topology: %+v", request)
				}
				binding, err := newNativeCAPITenantOwnership(request)
				if err != nil || binding.worker.Count != expected-1 {
					t.Fatalf("ownership lost requested worker count: %+v %v", binding, err)
				}
			})
		}
	}
}

func TestNativeReadinessRequiresEveryRequestedSchedulableWorkerAndDistinctBoot(t *testing.T) {
	for _, count := range []int{2, 3} {
		for _, mode := range []string{"exact", "missing", "extra", "duplicate name", "missing UID", "duplicate UID", "missing boot", "duplicate boot", "worker cordoned", "worker NoSchedule", "worker NoExecute", "worker provisioning", "control-plane taint"} {
			t.Run(fmt.Sprintf("%d/%s", count, mode), func(t *testing.T) {
				nodes := nativeTopologyNodes(count)
				index := count - 1
				switch mode {
				case "missing":
					nodes = nodes[:index]
				case "extra":
					nodes = append(nodes, nodes[index])
				case "duplicate name":
					nodes[index].Metadata.Name = nodes[0].Metadata.Name
				case "missing UID":
					nodes[index].Metadata.UID = ""
				case "duplicate UID":
					nodes[index].Metadata.UID = nodes[0].Metadata.UID
				case "missing boot":
					nodes[index].Status.NodeInfo.BootID = ""
				case "duplicate boot":
					nodes[index].Status.NodeInfo.BootID = nodes[0].Status.NodeInfo.BootID
				case "worker cordoned":
					nodes[index].Spec.Unschedulable = true
				case "worker NoSchedule", "worker NoExecute", "worker provisioning", "control-plane taint":
					key, effect := "synthetic.test/blocked", "NoSchedule"
					if mode == "worker NoExecute" {
						effect = "NoExecute"
					}
					if mode == "worker provisioning" {
						key, effect = "nke.nvidia.com/provisioning", "PreferNoSchedule"
					}
					if mode == "control-plane taint" {
						index = 0
						key = "node-role.kubernetes.io/control-plane"
					}
					nodes[index].Spec.Taints = append(nodes[index].Spec.Taints, struct {
						Key    string `json:"key"`
						Effect string `json:"effect"`
					}{key, effect})
				}
				ready, err := readyNativeTenantNodes(nodes, "1.34.2", count)
				want := mode == "exact" || mode == "control-plane taint"
				if ready != want || want && err != nil {
					t.Fatalf("ready=%v err=%v", ready, err)
				}
				if mode == "extra" && err == nil {
					t.Fatal("extra nodes must fail closed")
				}
				if mode == "control-plane taint" && len(nodes[0].Spec.Taints) != 1 {
					t.Fatal("readiness mutated control-plane scheduling")
				}
			})
		}
	}
}

func TestNativeCleanupBoundsMatchRequestedTopology(t *testing.T) {
	for _, limit := range []int{3, 4} {
		for _, mode := range []string{"exact", "extra", "duplicate"} {
			t.Run(fmt.Sprintf("%d/%s", limit, mode), func(t *testing.T) {
				rows := make([]nativeInstance, limit)
				for i := range rows {
					rows[i].ID = fmt.Sprintf("owned-%d", i)
				}
				if mode == "extra" {
					rows = append(rows, nativeInstance{ID: "unexpected"})
				}
				if mode == "duplicate" {
					rows[len(rows)-1].ID = rows[0].ID
				}
				owned, err := nativeCleanupRows(rows, "owned-0", limit)
				if mode == "exact" && (err != nil || len(owned) != limit) || mode != "exact" && err == nil {
					t.Fatalf("owned=%v err=%v", owned, err)
				}
			})
		}
	}
}
