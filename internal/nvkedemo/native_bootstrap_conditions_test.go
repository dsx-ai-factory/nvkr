// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"fmt"
	"strings"
	"testing"
)

func TestNativeBootstrapConditionsKeepOnlyClosedObservations(t *testing.T) {
	const cluster = `{"clusters":[{"id":"PRIVATE_ID","name":"PRIVATE_NAME","conditions":{"provisioning":{"status":"False","message":"Workflow failed: failed to resolve the CNI install mode: PRIVATE_TOKEN"},"provisioned":{"status":"False"},"PRIVATE_CONDITION":{"status":"True"}}}]}`
	for _, tc := range []struct{ name, collection, raw, want string }{
		{"cluster failure", "clusters", cluster, "Bootstrap controller observation: clusters[1] provisioning=False\nBootstrap source failure context: cni-mode\nBootstrap controller observation: clusters[1] provisioned=False\n"},
		{"nodes", "nodes", `{"nodes":[{"name":"PRIVATE_NODE","conditions":{"readyToJoin":{"status":"True","message":"PRIVATE_CREDENTIAL"},"nodeAgentReady":{"status":"False"}}}]}`, "Bootstrap controller observation: nodes[1] readyToJoin=True\nBootstrap controller observation: nodes[1] nodeAgentReady=False\n"},
		{"unknown status", "clusters", strings.ReplaceAll(cluster, "False", "PRIVATE_STATUS"), ""},
		{"unknown collection", "PRIVATE_COLLECTION", cluster, ""},
		{"wrong shape", "clusters", `{"clusters":{}}`, ""},
		{"malformed", "clusters", `{`, ""},
		{"no objects", "nodes", `{"nodes":[]}`, ""},
		{"excess objects", "clusters", `{"clusters":[` + strings.TrimSuffix(strings.Repeat(`{},`, 9), ",") + `]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeBootstrapConditionSummary(tc.collection, []byte(tc.raw)); got != tc.want || strings.Contains(got, "PRIVATE_") {
				t.Fatalf("unsafe or incorrect observation: %q", got)
			}
		})
	}
	var fields []string
	for i := range 33 {
		fields = append(fields, fmt.Sprintf(`"private-%d":{"status":"True"}`, i))
	}
	if got := nativeBootstrapConditionSummary("clusters", []byte(`{"clusters":[{"conditions":{`+strings.Join(fields, ",")+`}}]}`)); got != "" {
		t.Fatal("unbounded condition map was presented")
	}
}

func TestNativeBootstrapFailureContextNeverReturnsRawDetails(t *testing.T) {
	for _, tc := range []struct{ phrase, label string }{
		{"failed to resolve the CNI install mode:", "cni-mode"},
		{"failed to install cluster networking:", "cluster-networking"},
		{"failed to initialize Kubernetes control plane:", "control-plane-init"},
		{"failed to wait for a control plane node to be ready to join:", "control-plane-ready-to-join"},
		{"failed to configure initial node KubeVip:", "initial-node-kube-vip"},
		{"failed to create CAPI resources:", "capi-resources"},
		{"failed to mark initial control plane node as joined:", "control-plane-joined-record"},
		{"failed to install kube-local-dns:", "kube-local-dns"},
		{"failed to label system namespaces for Pod Security Admission:", "system-namespace-labels"},
		{"failed to mark cluster as provisioned:", "cluster-provisioned-record"},
		{"PRIVATE_UNKNOWN_FAILURE", "unclassified"},
	} {
		t.Run(tc.label, func(t *testing.T) {
			got := nativeBootstrapFailureContext("Workflow failed: " + tc.phrase + " PRIVATE_TOKEN")
			if got != tc.label || strings.Contains(got, "PRIVATE_") {
				t.Fatalf("unsafe or incorrect context: %q", got)
			}
			if got := nativeBootstrapFailureContext(tc.phrase); got != "" {
				t.Fatal("ordinary condition text became a workflow failure")
			}
		})
	}
}

func TestNativeBootstrapInitializationObservationsKeepDetailsPrivate(t *testing.T) {
	const prefix = "Workflow failed: failed to initialize Kubernetes control plane: "
	for _, tc := range []struct{ name, message, want string }{
		{"agent status", prefix + "failed to get kubeadm status: PRIVATE_TOKEN", "Bootstrap initialization observation: agent-status\n"},
		{"init RPC", prefix + "failed to execute kubeadm init on node PRIVATE_NAME: PRIVATE_TOKEN", "Bootstrap initialization observation: init-rpc\n"},
		{"kubelet failure", prefix + "kubeadm init failed: error execution phase wait-control-plane: failed while waiting for the kubelet to start PRIVATE_TOKEN", "Bootstrap initialization observation: kubeadm-command\nBootstrap kubeadm phase observation: wait-control-plane\nBootstrap kubeadm wait observation: kubelet-start-wait\n"},
		{"control plane failure", prefix + "kubeadm init failed: error execution phase wait-control-plane: failed while waiting for the control plane to start PRIVATE_TOKEN", "Bootstrap initialization observation: kubeadm-command\nBootstrap kubeadm phase observation: wait-control-plane\nBootstrap kubeadm wait observation: control-plane-start-wait\n"},
		{"legacy CNI", prefix + "CNI install failed: PRIVATE_TOKEN", "Bootstrap initialization observation: legacy-cni\n"},
		{"unclassified", prefix + "PRIVATE_UNKNOWN_FAILURE", ""},
		{"ordinary condition", "failed to initialize Kubernetes control plane: PRIVATE_TOKEN", ""},
		{"other phase", "Workflow failed: failed to install cluster networking: kubeadm init failed: PRIVATE_TOKEN", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeBootstrapInitializationSummary(tc.message); got != tc.want || strings.Contains(got, "PRIVATE_") {
				t.Fatalf("unsafe or incorrect observation: %q", got)
			}
		})
	}
}

func TestNativeBootstrapKubeadmPhasesKeepConsolePrivate(t *testing.T) {
	for _, phase := range []string{
		"preflight", "certs", "kubeconfig", "etcd", "control-plane", "kubelet-start",
		"wait-control-plane", "upload-config", "upload-certs", "mark-control-plane",
		"bootstrap-token", "kubelet-finalize", "addon", "show-join-command",
	} {
		for _, suffix := range []string{": PRIVATE_TOKEN", "/PRIVATE_CHILD: PRIVATE_TOKEN"} {
			t.Run(phase+suffix, func(t *testing.T) {
				console := "PRIVATE_REQUEST PRIVATE_SECRET\nerror execution phase " + phase + suffix
				want := "Bootstrap kubeadm phase observation: " + phase + "\n"
				if got := nativeBootstrapKubeadmSummary(console); got != want || strings.Contains(got, "PRIVATE_") {
					t.Fatalf("unsafe or incorrect console observation: %q", got)
				}
			})
		}
	}
	for _, console := range []string{"PRIVATE_SECRET", "error execution phase PRIVATE_PHASE: PRIVATE_TOKEN", "error execution phase preflight-extra: PRIVATE_TOKEN"} {
		if got := nativeBootstrapKubeadmSummary(console); got != "" {
			t.Fatalf("unknown console text became an observation: %q", got)
		}
	}
}

func TestNativeBootstrapKubeadmPreflightKeepsReasonsPrivate(t *testing.T) {
	for _, check := range []string{
		"CRI", "ContainerRuntimeVersion", "SystemVerification", "KubernetesVersion",
		"KubeletVersion", "Swap", "ImagePull", "NumCPU", "Mem", "IsPrivilegedUser",
	} {
		t.Run(check, func(t *testing.T) {
			console := "PRIVATE_REQUEST [ERROR " + check + "]: PRIVATE_CREDENTIAL"
			want := "Bootstrap kubeadm preflight observation: " + check + "\n"
			if got := nativeBootstrapKubeadmSummary(console); got != want {
				t.Fatalf("unsafe or incorrect preflight observation: %q", got)
			}
		})
	}
	if got := nativeBootstrapKubeadmSummary("[ERROR PRIVATE_UNKNOWN]: PRIVATE_TOKEN"); got != "" {
		t.Fatalf("unknown preflight reason was presented: %q", got)
	}
}
