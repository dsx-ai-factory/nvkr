// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/kubeowned"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
)

type nativeCloudInitMetadata struct {
	Name, Namespace, UID string
	DeletionTimestamp    *string
}

type nativeCloudInitPod struct {
	Metadata nativeCloudInitMetadata
	Spec     json.RawMessage
	Status   struct {
		Phase      string
		Containers []struct {
			Name, ImageID, ContainerID string
			RestartCount               *int
			State                      struct {
				Terminated *struct{ ExitCode *int }
			}
			VolumeMounts []struct{ Name, MountPath, RecursiveReadOnly string }
		} `json:"containerStatuses"`
	}
}

// Project only the authored execution fields; Kubernetes owns schema validation
// and defaults unrelated fields such as terminationMessagePath and resources.
type nativeCloudInitSpec struct {
	NodeName, RestartPolicy             string
	ActiveDeadlineSeconds               int
	AutomountServiceAccountToken        *bool
	HostPID, HostIPC, HostNetwork       bool
	InitContainers, EphemeralContainers []json.RawMessage
	Containers                          []struct {
		Name, Image, ImagePullPolicy, RestartPolicy, WorkingDir string
		Command, Args                                           []string
		Env, EnvFrom, VolumeDevices                             []json.RawMessage
		Lifecycle, StartupProbe, LivenessProbe, ReadinessProbe  json.RawMessage
		SecurityContext                                         map[string]any
		VolumeMounts                                            []struct {
			Name, MountPath, RecursiveReadOnly, MountPropagation, SubPath, SubPathExpr string
			ReadOnly                                                                   bool
		}
	}
	Volumes []struct {
		Name     string
		HostPath *struct{ Path, Type string }
	}
}

type nativeCloudInitNode struct{ Name, UID, BootID string }

func nativeCloudInitStatus(raw []byte) error {
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return err
	}
	// Interpret only cloud-init's documented status fields. The host's actual
	// command exit, independently observed from the Pod, is required as well.
	var wire struct {
		Status            string                       `json:"status"`
		ExtendedStatus    string                       `json:"extended_status"`
		Errors            []json.RawMessage            `json:"errors"`
		RecoverableErrors map[string][]json.RawMessage `json:"recoverable_errors"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return err
	}
	if wire.Status != "done" || wire.ExtendedStatus != "done" || wire.Errors == nil || wire.RecoverableErrors == nil || len(wire.Errors) != 0 || len(wire.RecoverableErrors) != 0 {
		return errors.New("actual cloud-init did not report clean completion")
	}
	return nil
}

// Observe a completed boot after Kubernetes readiness, without --wait: cloud-init
// retries failed systemctl queries indefinitely in wait mode. A read-only chroot
// cannot promise access to the host systemd bus. The actual command must still
// exit zero and report clean completion; running or degraded status is rejected.
func nativeCloudInitPodManifest(namespace, name, node, image string) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]string{"namespace": namespace, "name": name},
		"spec": map[string]any{"nodeName": node, "restartPolicy": "Never", "activeDeadlineSeconds": 180,
			"automountServiceAccountToken": false,
			"containers": []any{map[string]any{"name": "status", "image": image, "imagePullPolicy": "Never",
				"command": []string{"/bin/busybox", "chroot", "/host", "/usr/bin/env", "SYSTEMD_IGNORE_CHROOT=1", "PYTHONDONTWRITEBYTECODE=1", "/usr/bin/cloud-init", "status", "--format=json"},
				"securityContext": map[string]any{"runAsUser": 0, "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
					"capabilities": map[string]any{"drop": []string{"ALL"}, "add": []string{"SYS_CHROOT"}}, "seccompProfile": map[string]string{"type": "RuntimeDefault"}},
				"volumeMounts": []any{map[string]any{"name": "host", "mountPath": "/host", "readOnly": true, "recursiveReadOnly": "Enabled", "mountPropagation": "None"}}}},
			"volumes": []any{map[string]any{"name": "host", "hostPath": map[string]string{"path": "/", "type": "Directory"}}}}}
}

// runNativeCloudInit observes the installed host command on each exact fresh
// node. The Pods have no API token, network need, host namespaces or writeable
// host mount. Their CLI output is diagnostic evidence, never release authority.
func runNativeCloudInit(ctx context.Context, work, kubeconfig, kubectl, kubernetes, image string, expectedCount int, expected []tenantNode,
	capture func(context.Context, []string, string, time.Duration) ([]byte, error)) (result error) {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if _, err := workloadImageDigest(image); err != nil {
		return err
	}
	ready, err := readyNativeTenantNodes(expected, kubernetes, expectedCount)
	if err != nil || !ready {
		return errors.Join(err, errors.New("cloud-init observation requires the exact requested fresh Ready tenant nodes"))
	}
	command := func(args ...string) []string {
		return append([]string{kubectl, "--kubeconfig", kubeconfig, "--request-timeout=20s"}, args...)
	}
	get := func(ctx context.Context, phase string, args ...string) ([]byte, error) {
		return capture(ctx, command(args...), "cloud-init-"+phase, 30*time.Second)
	}
	identity := func(ctx context.Context, phase string) ([]nativeCloudInitNode, string, error) {
		raw, err := get(ctx, phase+"-nodes", "get", "nodes", "-o", "json")
		if err != nil {
			return nil, "", err
		}
		var nodes struct{ Items []tenantNode }
		if err := json.Unmarshal(raw, &nodes); err != nil {
			return nil, "", err
		}
		ready, err := readyNativeTenantNodes(nodes.Items, kubernetes, expectedCount)
		if err != nil || !ready {
			return nil, "", errors.Join(err, errors.New("cloud-init check requires the same requested Ready nodes"))
		}
		var selected []nativeCloudInitNode
		for _, node := range nodes.Items {
			if !safeTenantIdentity(node.Metadata.UID) || !safeTenantIdentity(node.Status.NodeInfo.BootID) || !slices.ContainsFunc(expected, func(n tenantNode) bool {
				return n.Metadata.Name == node.Metadata.Name && n.Metadata.UID == node.Metadata.UID && n.Status.NodeInfo.BootID == node.Status.NodeInfo.BootID
			}) {
				return nil, "", errors.New("cloud-init node identity differs from fresh readiness observation")
			}
			selected = append(selected, nativeCloudInitNode{node.Metadata.Name, node.Metadata.UID, node.Status.NodeInfo.BootID})
		}
		slices.SortFunc(selected, func(a, b nativeCloudInitNode) int { return strings.Compare(a.Name, b.Name) })
		raw, err = get(ctx, phase+"-cluster", "get", "namespace", "kube-system", "-o", "json")
		if err != nil {
			return nil, "", err
		}
		var cluster struct{ Metadata nativeCloudInitMetadata }
		if err := json.Unmarshal(raw, &cluster); err != nil || cluster.Metadata.Name != "kube-system" || !safeTenantIdentity(cluster.Metadata.UID) {
			return nil, "", errors.New("cloud-init tenant API identity unavailable")
		}
		return selected, cluster.Metadata.UID, nil
	}
	nodes, clusterID, err := identity(ctx, "before")
	if err != nil {
		return err
	}
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return err
	}
	namespace := "nvkr-cloud-init-" + hex.EncodeToString(random[:])
	namespacePath := "/api/v1/namespaces/" + namespace
	manifest := filepath.Join(work, "cloud-init-namespace.json")
	if err := privateJSON(manifest, map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": namespace, "labels": map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}}}); err != nil {
		return err
	}
	raw, err := get(ctx, "namespace-create", "create", "-f", manifest, "-o", "json")
	if err != nil {
		return err
	}
	var ns struct{ Metadata nativeCloudInitMetadata }
	if err := json.Unmarshal(raw, &ns); err != nil || ns.Metadata.Name != namespace || !safeTenantIdentity(ns.Metadata.UID) {
		return errors.New("created cloud-init namespace UID unavailable; cleanup ownership not established")
	}
	defer func() {
		if errors.Is(result, ErrUnjoined) {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		deletes := 0
		result = errors.Join(result, kubeowned.Delete(cleanup, []kubeowned.Binding{{Path: namespacePath, UID: ns.Metadata.UID}},
			func(ctx context.Context, _ string) (kubeowned.Observation, error) {
				raw, err := get(ctx, "namespace-cleanup-get", "get", "namespace", namespace, "--ignore-not-found=true", "-o", "json")
				if err != nil || len(strings.TrimSpace(string(raw))) == 0 {
					return kubeowned.Observation{}, err
				}
				var observed struct{ Metadata nativeCloudInitMetadata }
				if err := json.Unmarshal(raw, &observed); err != nil || !safeTenantIdentity(observed.Metadata.UID) {
					return kubeowned.Observation{}, errors.New("cloud-init cleanup UID unavailable")
				}
				return kubeowned.Observation{UID: observed.Metadata.UID, Deleting: observed.Metadata.DeletionTimestamp != nil}, nil
			}, func(ctx context.Context, resource string, body []byte) error {
				deletes++
				path := filepath.Join(work, fmt.Sprintf("cloud-init-delete-%03d.json", deletes))
				if err := privateWrite(path, body); err != nil {
					return err
				}
				_, err := get(ctx, "namespace-cleanup-delete", "delete", "--raw="+resource, "--filename="+path)
				return err
			}, time.Second))
	}()
	for i, node := range nodes {
		name := fmt.Sprintf("status-%d", i)
		if err := nativeCloudInitProbe(ctx, work, namespace, name, node.Name, image, get); err != nil {
			return err
		}
	}
	after, observedCluster, err := identity(ctx, "after")
	if err != nil || observedCluster != clusterID || !reflect.DeepEqual(nodes, after) {
		return errors.Join(err, errors.New("tenant API or node boot identity changed during cloud-init observation"))
	}
	return nil
}

func nativeCloudInitProbe(ctx context.Context, work, namespace, name, node, image string,
	get func(context.Context, string, ...string) ([]byte, error)) (result error) {
	manifest := nativeCloudInitPodManifest(namespace, name, node, image)
	path := filepath.Join(work, "cloud-init-"+name+".json")
	if err := privateJSON(path, manifest); err != nil {
		return err
	}
	raw, err := get(ctx, name+"-create", "create", "-f", path, "-o", "json")
	if err != nil {
		return err
	}
	var created nativeCloudInitPod
	if err := json.Unmarshal(raw, &created); err != nil || created.Metadata.Name != name || created.Metadata.Namespace != namespace || !safeTenantIdentity(created.Metadata.UID) {
		return errors.New("cloud-init Pod identity unavailable")
	}
	logsCaptured := false
	defer func() {
		if logsCaptured || errors.Is(result, ErrUnjoined) {
			return
		}
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		stdout, err := get(recovery, name+"-failure-stdout", "logs", name, "-n", namespace, "--container=status", "--pod-running-timeout=5s")
		if len(stdout) > 0 {
			err = errors.Join(err, privateWrite(filepath.Join(work, "cloud-init-"+name+"-stdout.json"), stdout))
		}
		result = errors.Join(result, err)
	}()
	// Kubernetes may default fields, but it may not substitute the authored
	// container, command, capabilities, mount or exact node selection.
	var selected, expected nativeCloudInitSpec
	if err := json.Unmarshal(created.Spec, &selected); err != nil {
		return err
	}
	requested, err := json.Marshal(manifest["spec"])
	if err != nil {
		return err
	}
	if err := json.Unmarshal(requested, &expected); err != nil || !reflect.DeepEqual(selected, expected) {
		return errors.New("created cloud-init Pod differs from authored diagnostic")
	}
	var observed nativeCloudInitPod
	for {
		raw, err := get(ctx, name+"-observe", "get", "pod", name, "-n", namespace, "-o", "json")
		if err != nil {
			return err
		}
		if err := json.Unmarshal(raw, &observed); err != nil || observed.Metadata.Name != name || observed.Metadata.Namespace != namespace || observed.Metadata.UID != created.Metadata.UID || observed.Metadata.DeletionTimestamp != nil || !sameInfraManifest(observed.Spec, created.Spec) {
			return errors.New("cloud-init Pod identity or execution changed")
		}
		if observed.Status.Phase == "Succeeded" || observed.Status.Phase == "Failed" {
			break
		}
		if err := pause(ctx, time.Second); err != nil {
			return err
		}
	}
	stdout, logErr := get(ctx, name+"-stdout", "logs", name, "-n", namespace, "--container=status", "--pod-running-timeout=5s")
	logsCaptured = true
	if len(stdout) > 0 {
		if err := privateWrite(filepath.Join(work, "cloud-init-"+name+"-stdout.json"), stdout); err != nil {
			return errors.Join(logErr, err)
		}
	}
	if logErr != nil {
		return logErr
	}
	if observed.Status.Phase != "Succeeded" || len(observed.Status.Containers) != 1 {
		return errors.New("actual host cloud-init command did not succeed")
	}
	container := observed.Status.Containers[0]
	if container.Name != "status" || container.RestartCount == nil || *container.RestartCount != 0 || container.State.Terminated == nil || container.State.Terminated.ExitCode == nil || *container.State.Terminated.ExitCode != 0 || !strings.HasPrefix(container.ContainerID, "containerd://") ||
		strings.Split(container.ImageID, "@")[len(strings.Split(container.ImageID, "@"))-1] != strings.Split(image, "@")[1] || len(container.VolumeMounts) != 1 || container.VolumeMounts[0].Name != "host" || container.VolumeMounts[0].MountPath != "/host" || container.VolumeMounts[0].RecursiveReadOnly != "Enabled" {
		return errors.New("cloud-init command lacks exact image, zero-retry exit or recursive read-only mount evidence")
	}
	raw, err = get(ctx, name+"-after-stdout", "get", "pod", name, "-n", namespace, "-o", "json")
	if err != nil {
		return err
	}
	var after nativeCloudInitPod
	if err := json.Unmarshal(raw, &after); err != nil || after.Metadata.Name != name || after.Metadata.Namespace != namespace || after.Metadata.UID != created.Metadata.UID || !sameInfraManifest(after.Spec, created.Spec) || !reflect.DeepEqual(after.Status, observed.Status) || after.Metadata.DeletionTimestamp != nil {
		return errors.New("cloud-init execution identity changed during stdout capture")
	}
	return nativeCloudInitStatus(stdout)
}
