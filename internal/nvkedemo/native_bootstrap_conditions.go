// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"fmt"
	"strings"
)

// These are observations of the selected source API, not readiness or failure
// authority. Arbitrary condition messages and response identities stay private.
func nativeBootstrapConditionSummary(collection string, raw []byte) string {
	type condition struct {
		Status  TenantConditionStatus `json:"status"`
		Message string                `json:"message"`
	}
	type row struct {
		Conditions map[string]condition `json:"conditions"`
	}
	var response struct {
		Clusters []row `json:"clusters"`
		Nodes    []row `json:"nodes"`
	}
	if json.Unmarshal(raw, &response) != nil {
		return ""
	}
	rows := response.Clusters
	names := []string{"provisioning", "provisioned", "deleting", "updateFailed"}
	switch collection {
	case "clusters":
	case "nodes":
		rows = response.Nodes
		names = []string{"pending", "provisioningClaimed", "provisioning", "readyToJoin", "joined", "provisioned", "nodeAgentReady", "kubernetesReady", "deleting"}
	default:
		return ""
	}
	// The owned bootstrap has one cluster and one site node. Extra or malformed
	// responses are diagnostic loss, never permission to adopt another object.
	if len(rows) == 0 || len(rows) > 8 {
		return ""
	}
	var result strings.Builder
	for index, item := range rows {
		if len(item.Conditions) > 32 {
			return ""
		}
		for _, name := range names {
			value, ok := item.Conditions[name]
			status := presentedTenantCondition(value.Status)
			if !ok || status == "" {
				continue
			}
			fmt.Fprintf(&result, "Bootstrap controller observation: %s[%d] %s=%s\n", collection, index+1, name, status)
			if context := nativeBootstrapFailureContext(value.Message); context != "" {
				fmt.Fprintf(&result, "Bootstrap source failure context: %s\n", context)
				result.WriteString(nativeBootstrapInitializationSummary(value.Message))
			}
		}
	}
	return result.String()
}

// Source workflow wrappers are deliberately matched only as literal phrases,
// without decoding Temporal errors or asserting a root cause. Only these static
// labels leave private storage; arbitrary error details are never returned.
func nativeBootstrapFailureContext(message string) string {
	if !strings.HasPrefix(message, "Workflow failed: ") {
		return ""
	}
	for _, item := range []struct{ phrase, label string }{
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
	} {
		if strings.Contains(message, item.phrase) {
			return item.label
		}
	}
	return "unclassified"
}

// These literal source wrappers and kubeadm phase names are observations only.
// The selected NVKE agent retains kubeadm's failure line in CurrentMessage;
// neither that message nor its arguments or credentials may be published.
func nativeBootstrapInitializationSummary(message string) string {
	if nativeBootstrapFailureContext(message) != "control-plane-init" {
		return ""
	}
	var result strings.Builder
	for _, item := range []struct{ phrase, label string }{
		{"failed to get kubeadm status:", "agent-status"},
		{"failed to render kubeadm init config:", "config-render"},
		{"failed to prepare kubeadm files on node ", "prerequisite-files"},
		{"failed to execute kubeadm init on node ", "init-rpc"},
		{"kubeadm init failed:", "kubeadm-command"},
		{"CNI install failed:", "legacy-cni"},
		{"failed to write kubeadm prerequisite files:", "agent-prerequisites"},
		{"failed to write init config file", "agent-config"},
		{"failed to parse join strings", "join-output"},
	} {
		if strings.Contains(message, item.phrase) {
			fmt.Fprintf(&result, "Bootstrap initialization observation: %s\n", item.label)
		}
	}
	result.WriteString(nativeBootstrapKubeadmSummary(message))
	return result.String()
}

// Kubernetes v1.36.3's init phase owners and wait-control-plane wrappers own
// these names. Match closed literal phrases only; arbitrary console text and
// nested error fields never leave private storage. A match is not root-cause
// proof and an unknown version or failure may yield no observation.
func nativeBootstrapKubeadmSummary(message string) string {
	var result strings.Builder
	for _, phase := range []string{
		"preflight", "certs", "kubeconfig", "etcd", "control-plane", "kubelet-start",
		"wait-control-plane", "upload-config", "upload-certs", "mark-control-plane",
		"bootstrap-token", "kubelet-finalize", "addon", "show-join-command",
	} {
		prefix := "error execution phase " + phase
		if strings.Contains(message, prefix+":") || strings.Contains(message, prefix+"/") {
			fmt.Fprintf(&result, "Bootstrap kubeadm phase observation: %s\n", phase)
		}
	}
	for _, check := range []string{
		"CRI", "ContainerRuntimeVersion", "SystemVerification", "KubernetesVersion",
		"KubeletVersion", "Swap", "ImagePull", "NumCPU", "Mem", "IsPrivilegedUser",
	} {
		if strings.Contains(message, "[ERROR "+check+"]") {
			fmt.Fprintf(&result, "Bootstrap kubeadm preflight observation: %s\n", check)
		}
	}
	for _, item := range []struct{ phrase, label string }{
		{"failed while waiting for the kubelet to start", "kubelet-start-wait"},
		{"failed while waiting for the control plane to start", "control-plane-start-wait"},
	} {
		if strings.Contains(message, item.phrase) {
			fmt.Fprintf(&result, "Bootstrap kubeadm wait observation: %s\n", item.label)
		}
	}
	return result.String()
}
