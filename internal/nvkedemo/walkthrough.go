// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const walkthroughTimeout = 3 * time.Minute

// These projections deliberately omit node addresses, pod names, credentials,
// provider messages and raw logs. Kubernetes owns response decoding through
// encoding/json; this presentation is not a readiness or qualification receipt.
type walkthroughMetadata struct {
	Name, Namespace, UID string
	Owners               []struct{ UID string } `json:"ownerReferences"`
}

type walkthroughPod struct {
	Metadata walkthroughMetadata
	Status   struct {
		Phase      string
		Containers []struct {
			Ready        bool
			RestartCount int
			ImageID      string
			State        struct{ Terminated *struct{ ExitCode int } }
		} `json:"containerStatuses"`
	}
}

type walkthroughWorkload struct {
	Kind     string
	Metadata walkthroughMetadata
	Spec     struct{ Replicas *int }
	Status   struct {
		Replicas, ReadyReplicas, UpdatedReplicas                    int
		DesiredNumberScheduled, NumberReady, UpdatedNumberScheduled int
	}
}

type walkthroughJob struct {
	Metadata walkthroughMetadata
	Spec     struct {
		BackoffLimit *int
		Template     struct {
			Spec struct{ RestartPolicy string }
		}
	}
	Status struct {
		Active, Failed, Succeeded int
		Conditions                []struct{ Type, Status string }
	}
}

// recordedWalkthrough runs after the unchanged workload/DNS checks and before
// natural deletion. Failure remains failure and still reaches mandatory owned
// cleanup. It never creates, reapplies or retries a workload.
func (t *tenant) recordedWalkthrough(ctx context.Context, out io.Writer) (result error) {
	if ctx == nil || out == nil {
		return nvkrerr.New(nvkrerr.CodeUsage, "recorded walkthrough requires a context and output writer")
	}
	bounded, cancel := context.WithTimeout(ctx, walkthroughTimeout)
	defer cancel()
	var text strings.Builder
	fmt.Fprintf(&text, "\nRecorded Kubernetes walkthrough · %s\nLive read-only observations of this invocation, not a signed Release or qualification.\nPrivate kubeconfig locators select each cluster explicitly; node/pod identities, addresses and raw responses remain in private diagnostics.\n", time.Now().UTC().Format(time.RFC3339))
	defer func() {
		if text.Len() > 64<<10 {
			result = errors.Join(result, walkthroughInvalid())
			text.Reset()
			text.WriteString("Walkthrough presentation exceeded its 64 KiB bound; private diagnostics retained.\n")
		}
		if result != nil {
			text.WriteString("Walkthrough failed; this recording is not accepted. Mandatory owned cleanup still runs.\n")
		} else {
			text.WriteString("Walkthrough observations complete; natural tenant deletion and joined cleanup follow.\n")
		}
		result = errors.Join(result, writeWalkthroughPages(bounded, out, text.String()))
	}()
	for _, cluster := range []struct {
		name, config string
		site         bool
	}{
		{"Site / Kind management", environmentValue(t.session.Runner.Env, "SITE_KUBECONFIG"), true},
		{"Tenant / QCOW2", filepath.Join(t.session.Work, "tenant-kubeconfig"), false},
	} {
		if !filepath.IsAbs(cluster.config) {
			return nvkrerr.New(nvkrerr.CodePreflightFailed, "walkthrough requires the exact owned cluster kubeconfig")
		}
		fmt.Fprintf(&text, "\n%s\n", cluster.name)
		capture := func(args ...string) ([]byte, error) {
			fmt.Fprintf(&text, "$ kubectl --kubeconfig <%s private kubeconfig> %s\n", strings.ToLower(strings.Fields(cluster.name)[0]), strings.Join(args, " "))
			raw, err := t.capture(bounded, t.kubectl(cluster.config, append([]string{"--request-timeout=10s"}, args...)...), "walkthrough", 30*time.Second)
			if err != nil {
				cause := operatorCancellation(err)
				if errors.Is(err, ErrUnjoined) {
					cause = errors.Join(cause, ErrUnjoined)
				}
				return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, cause, "walkthrough observation failed; private diagnostics retained")
			}
			return raw, nil
		}
		if _, err := capture("get", "nodes", "-o", "wide"); err != nil {
			return err
		}
		raw, err := capture("get", "nodes", "-o", "json")
		if err != nil {
			return err
		}
		var nodes struct{ Items []tenantNode }
		if err := json.Unmarshal(raw, &nodes); err != nil {
			return walkthroughInvalid()
		}
		rows, ready, valid := operatorNodeRows(nodes.Items)
		if !valid || len(rows) == 0 || len(rows) > 32 || ready != len(rows) {
			return walkthroughInvalid()
		}
		fmt.Fprintf(&text, "Nodes: %d/%d Ready\nROLE  READY  ARCH  KUBERNETES\n", ready, len(rows))
		for _, row := range rows {
			fmt.Fprintln(&text, row)
		}
		raw, err = capture("get", "pods", "-A", "-o", "json")
		if err != nil {
			return err
		}
		podRows, err := walkthroughPodRows(raw)
		if err != nil {
			return err
		}
		text.WriteString(podRows)
		raw, err = capture("get", "deployments,daemonsets,statefulsets", "--all-namespaces", "-o", "json")
		if err != nil {
			return err
		}
		var workloads struct{ Items []walkthroughWorkload }
		if err := json.Unmarshal(raw, &workloads); err != nil || len(workloads.Items) > 64 {
			return walkthroughInvalid()
		}
		slices.SortFunc(workloads.Items, func(a, b walkthroughWorkload) int {
			return strings.Compare(a.Kind+"/"+a.Metadata.Namespace+"/"+a.Metadata.Name, b.Kind+"/"+b.Metadata.Namespace+"/"+b.Metadata.Name)
		})
		text.WriteString("KIND  NAMESPACE  NAME  READY/DESIRED  UPDATED\n")
		for _, workload := range workloads.Items {
			if !walkthroughName(workload.Metadata.Namespace) || !walkthroughName(workload.Metadata.Name) {
				return walkthroughInvalid()
			}
			var desired, ready, updated int
			switch workload.Kind {
			case "Deployment", "StatefulSet":
				desired = 1
				if workload.Spec.Replicas != nil {
					desired = *workload.Spec.Replicas
				}
				ready, updated = workload.Status.ReadyReplicas, workload.Status.UpdatedReplicas
			case "DaemonSet":
				desired, ready, updated = workload.Status.DesiredNumberScheduled, workload.Status.NumberReady, workload.Status.UpdatedNumberScheduled
			default:
				return walkthroughInvalid()
			}
			if desired < 0 || ready < 0 || updated < 0 {
				return walkthroughInvalid()
			}
			fmt.Fprintf(&text, "%s  %s  %s  %d/%d  %d\n", workload.Kind, workload.Metadata.Namespace, workload.Metadata.Name, ready, desired, updated)
			if !walkthroughSelectedRollout(cluster.site, t.network, workload) {
				continue
			}
			resource := strings.ToLower(workload.Kind) + "/" + workload.Metadata.Name
			if _, err := capture("-n", workload.Metadata.Namespace, "rollout", "status", resource, "--timeout=20s"); err != nil {
				return err
			}
			fmt.Fprintf(&text, "Observed rollout complete: %s/%s\n", workload.Metadata.Namespace, resource)
		}
		if !cluster.site {
			raw, err = capture("-n", "default", "get", "job", "nvkr-smoke", "-o", "json")
			if err != nil {
				return err
			}
			var job walkthroughJob
			if err := json.Unmarshal(raw, &job); err != nil {
				return walkthroughInvalid()
			}
			raw, err = capture("-n", "default", "get", "pods", "-l", "job-name=nvkr-smoke", "-o", "json")
			if err != nil {
				return err
			}
			var pods struct{ Items []walkthroughPod }
			if err := json.Unmarshal(raw, &pods); err != nil || !walkthroughJobPassed(job, pods.Items) {
				return walkthroughInvalid()
			}
			text.WriteString("Existing nvkr-smoke: Complete=True; succeeded=1; failed=0; backoffLimit=0; restartPolicy=Never; container exit=0; restarts=0.\n")
			raw, err = capture("-n", "default", "logs", "job/nvkr-smoke", "--tail=40", "--limit-bytes=16384")
			if err != nil {
				return err
			}
			if !slices.Contains(strings.Split(strings.TrimSpace(string(raw)), "\n"), "NVKE_LOCAL_WORKLOAD_PASS") {
				return walkthroughInvalid()
			}
			text.WriteString("Existing bounded Job log: NVKE_LOCAL_WORKLOAD_PASS\nThe already-accepted Job ran the source-owned ARM64 check and cluster DNS lookup; no Job was rerun. Raw DNS addresses stay private.\n")
		}
	}
	return nil
}

// walkthroughPodRows reuses the bounded Kubernetes pod projection for recorded
// legacy and native observations. Raw identities and addresses remain private.
func walkthroughPodRows(raw []byte) (string, error) {
	var pods struct{ Items []walkthroughPod }
	if err := json.Unmarshal(raw, &pods); err != nil || len(pods.Items) == 0 || len(pods.Items) > 128 {
		return "", walkthroughInvalid()
	}
	slices.SortFunc(pods.Items, func(a, b walkthroughPod) int {
		return strings.Compare(a.Metadata.Namespace+"/"+a.Metadata.Name, b.Metadata.Namespace+"/"+b.Metadata.Name)
	})
	var text strings.Builder
	text.WriteString("NAMESPACE  POD  PHASE  READY  RESTARTS\n")
	for index, pod := range pods.Items {
		if !walkthroughName(pod.Metadata.Namespace) || !slices.Contains([]string{"Pending", "Running", "Succeeded", "Failed", "Unknown"}, pod.Status.Phase) || len(pod.Status.Containers) > 32 {
			return "", walkthroughInvalid()
		}
		ready, restarts := 0, 0
		for _, container := range pod.Status.Containers {
			if container.RestartCount < 0 {
				return "", walkthroughInvalid()
			}
			if container.Ready {
				ready++
			}
			restarts += container.RestartCount
		}
		fmt.Fprintf(&text, "%s  pod-%03d  %s  %d/%d  %d\n", pod.Metadata.Namespace, index+1, pod.Status.Phase, ready, len(pod.Status.Containers), restarts)
	}
	return text.String(), nil
}

func writeWalkthroughPages(ctx context.Context, out io.Writer, text string) error {
	if len(text) > 64<<10 {
		return walkthroughInvalid()
	}
	delay := time.Duration(0)
	if pager, ok := out.(OperatorStatusPager); ok {
		delay = min(3*time.Second, max(time.Duration(0), pager.OperatorStatusPageDuration()))
	}
	if ctx.Err() != nil {
		return errors.Join(writeOperatorStatus(out, OperatorStatus{text: text, walkthrough: true}), nvkrerr.Wrap(nvkrerr.CodeTimeout, ctx.Err(), "walkthrough presentation canceled; owned cleanup still required"))
	}
	if delay == 0 {
		return writeOperatorStatus(out, OperatorStatus{text: text, walkthrough: true})
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	const pageRows = 12
	pages := (len(lines) + pageRows - 1) / pageRows
	cluster, command := "", ""
	for start := 0; start < len(lines); start += pageRows {
		var page strings.Builder
		fmt.Fprintf(&page, "Walkthrough page %d/%d · observed before cleanup\n", start/pageRows+1, pages)
		if cluster != "" {
			fmt.Fprintln(&page, cluster)
		}
		if command != "" {
			fmt.Fprintln(&page, command)
		}
		for _, line := range lines[start:min(start+pageRows, len(lines))] {
			if line == "Site / Kind management" || line == "Site / native QCOW2" || line == "Tenant / QCOW2" {
				cluster, command = line, ""
			}
			if strings.HasPrefix(line, "$ kubectl ") {
				command = line
			}
			fmt.Fprintln(&page, line)
		}
		if err := writeOperatorStatus(out, OperatorStatus{text: page.String(), walkthrough: true}); err != nil {
			return err
		}
		if err := pause(ctx, delay); err != nil {
			return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "walkthrough presentation canceled; owned cleanup still required")
		}
	}
	return nil
}

func walkthroughName(name string) bool { return operatorArgument(name) && len(name) <= 128 }

func walkthroughInvalid() error {
	return nvkrerr.New(nvkrerr.CodePreflightFailed, "walkthrough response did not confirm its bounded observation; private diagnostics retained")
}

func walkthroughSelectedRollout(site bool, network string, workload walkthroughWorkload) bool {
	if workload.Metadata.Namespace == "kube-system" {
		if workload.Kind == "Deployment" && workload.Metadata.Name == "coredns" {
			return true
		}
		if workload.Kind == "DaemonSet" && workload.Metadata.Name == "node-local-dns" && !site {
			return true
		}
		if workload.Kind == "DaemonSet" && workload.Metadata.Name == siteOVSName && site && network != "kind-calico" {
			return true
		}
	}
	if site && network != "kind-calico" && workload.Metadata.Namespace == "ovn-kubernetes" {
		return workload.Kind == "Deployment" && workload.Metadata.Name == "ovnkube-control-plane" || workload.Kind == "DaemonSet" && workload.Metadata.Name == "ovnkube-node"
	}
	return !site && network == "kind-calico" && workload.Metadata.Namespace == "calico-system" && workload.Kind == "DaemonSet" && workload.Metadata.Name == "calico-node"
}

func walkthroughJobPassed(job walkthroughJob, pods []walkthroughPod) bool {
	if job.Metadata.Name != "nvkr-smoke" || job.Metadata.Namespace != "default" || job.Metadata.UID == "" || job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 || job.Spec.Template.Spec.RestartPolicy != "Never" || job.Status.Active != 0 || job.Status.Failed != 0 || job.Status.Succeeded != 1 || len(pods) != 1 {
		return false
	}
	complete := 0
	for _, condition := range job.Status.Conditions {
		if condition.Type == "Complete" && condition.Status == "True" {
			complete++
		}
		if condition.Type == "Failed" && condition.Status == "True" {
			return false
		}
	}
	pod := pods[0]
	if complete != 1 || pod.Metadata.Namespace != "default" || pod.Status.Phase != "Succeeded" || len(pod.Status.Containers) != 1 {
		return false
	}
	owner := false
	for _, reference := range pod.Metadata.Owners {
		owner = owner || reference.UID == job.Metadata.UID
	}
	container := pod.Status.Containers[0]
	// Preserve tenant.run's accepted ImageID forms: exact bare digest or the
	// final digest after '@'. This observation must not narrow that owner check.
	imageParts := strings.Split(container.ImageID, "@")
	return owner && container.RestartCount == 0 && container.State.Terminated != nil && container.State.Terminated.ExitCode == 0 && imageParts[len(imageParts)-1] == strings.Split(WorkloadImage, "@")[1]
}
