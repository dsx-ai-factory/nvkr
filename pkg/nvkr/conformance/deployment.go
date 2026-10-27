// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
	"github.com/google/go-containerregistry/pkg/name"
)

// DeploymentTarget selects a named authenticated image and exact API replica set.
// It does not claim coverage of other outputs or independent host attestation.
type DeploymentTarget struct {
	Output           string `json:"output"`
	Namespace        string `json:"namespace"`
	LabelSelector    string `json:"labelSelector"`
	Container        string `json:"container"`
	RequiredReplicas int    `json:"requiredReplicas"`
}

func validateTargetShape(targets []DeploymentTarget) error {
	if len(targets) == 0 || len(targets) > 64 {
		return e.New(e.CodeUsage, "conformance requires 1..64 explicit deployment targets")
	}
	seen := map[string]bool{}
	for _, t := range targets {
		for _, value := range []string{t.Output, t.Namespace, t.LabelSelector, t.Container} {
			if !safetext.Valid(value, 4096, false) || strings.TrimSpace(value) != value {
				return e.New(e.CodeUsage, "deployment target fields must be explicit safe text")
			}
		}
		key := t.Namespace + "/" + t.LabelSelector + "/" + t.Container
		if seen[key] || t.RequiredReplicas <= 0 || t.RequiredReplicas > 10000 {
			return e.New(e.CodeUsage, "deployment targets require unique selectors and bounded positive replicas")
		}
		seen[key] = true
	}
	return nil
}
func selectOutputs(description lifecycle.ArtifactCheckDescription, targets []DeploymentTarget) (map[string]lifecycle.ArtifactCheckOutput, error) {
	selected := map[string]lifecycle.ArtifactCheckOutput{}
	for _, target := range targets {
		var matches []lifecycle.ArtifactCheckOutput
		for _, output := range description.Outputs {
			if output.Name == target.Output {
				matches = append(matches, output)
			}
		}
		if len(matches) != 1 {
			return nil, failure("deployment target must select one exact authenticated named output")
		}
		output := matches[0]
		if output.Kind != lifecycle.OutputOCI {
			return nil, e.New(e.CodeEngineCapability, "deployment target supports only native OCI images")
		}
		ref, err := name.NewDigest(output.Reference, name.StrictValidation)
		if err != nil || ref.Name() != output.Reference || !safetext.Valid(output.Reference, 1024, false) || !canonical.IsDigest(ref.DigestStr()) || ref.DigestStr() != output.Subject {
			return nil, failure("authenticated image reference and subject disagree")
		}
		if output.ImageIndex != nil {
			if err = output.ImageIndex.Validate(); err != nil {
				return nil, err
			}
		} else if output.Platform != "linux/amd64" && output.Platform != "linux/arm64" {
			return nil, e.New(e.CodeEngineCapability, "deployment target requires an authenticated native Linux platform")
		}
		selected[target.Output] = output
	}
	return selected, nil
}

type apiMetadata struct {
	Name              string  `json:"name"`
	Namespace         string  `json:"namespace"`
	UID               string  `json:"uid"`
	DeletionTimestamp *string `json:"deletionTimestamp"`
}
type deploymentPod struct {
	Metadata apiMetadata `json:"metadata"`
	Spec     struct {
		NodeName   string `json:"nodeName"`
		Containers []struct {
			Name  string `json:"name"`
			Image string `json:"image"`
		} `json:"containers"`
	} `json:"spec"`
	Status struct {
		Phase             string `json:"phase"`
		ContainerStatuses []struct {
			Name         string `json:"name"`
			Ready        bool   `json:"ready"`
			ImageID      string `json:"imageID"`
			ContainerID  string `json:"containerID"`
			RestartCount *int64 `json:"restartCount"`
			State        struct {
				Running *struct{} `json:"running"`
			} `json:"state"`
		} `json:"containerStatuses"`
	} `json:"status"`
}
type deploymentNode struct {
	Metadata apiMetadata `json:"metadata"`
	Status   struct {
		NodeInfo struct {
			OperatingSystem         string `json:"operatingSystem"`
			Architecture            string `json:"architecture"`
			OSImage                 string `json:"osImage"`
			KernelVersion           string `json:"kernelVersion"`
			ContainerRuntimeVersion string `json:"containerRuntimeVersion"`
			KubeletVersion          string `json:"kubeletVersion"`
			MachineID               string `json:"machineID"`
			BootID                  string `json:"bootID"`
			SystemUUID              string `json:"systemUUID"`
		} `json:"nodeInfo"`
	} `json:"status"`
}
type deploymentObservation struct {
	Target                                                     int
	KubeSystemNamespaceUID                                     string
	PodUID, ContainerID, NodeUID, Platform, Reference, ImageID string
	Restarts                                                   int64
}

func nativeReference(output lifecycle.ArtifactCheckOutput, platform string) (string, error) {
	digest := output.Subject
	if output.ImageIndex == nil {
		if output.Platform != platform {
			return "", failure("deployed node platform does not match native image")
		}
	} else {
		digest = ""
		for _, child := range output.ImageIndex.Children {
			if child.Platform == platform {
				digest = child.ManifestDigest
			}
		}
		if digest == "" {
			return "", failure("authenticated index has no native child for deployed node")
		}
	}
	ref, err := name.NewDigest(output.Reference, name.StrictValidation)
	if err != nil {
		return "", failure("invalid authenticated image reference")
	}
	return ref.Context().Name() + "@" + digest, nil
}
func exactRuntimeReference(value string) (string, error) {
	if !safetext.Valid(value, 1100, false) {
		return "", failure("runtime image ID is not bounded safe text")
	}
	if strings.HasPrefix(value, "docker-pullable://") {
		value = strings.TrimPrefix(value, "docker-pullable://")
	} else if strings.Contains(value, "://") {
		return "", failure("runtime image ID scheme cannot establish native manifest identity")
	}
	ref, err := name.NewDigest(value, name.StrictValidation)
	if err != nil || ref.Name() != value || !canonical.IsDigest(ref.DigestStr()) {
		return "", failure("runtime image ID requires an exact repository and native manifest digest")
	}
	return value, nil
}
func evaluateDeployments(targets []DeploymentTarget, outputs map[string]lifecycle.ArtifactCheckOutput, nodes []deploymentNode, pods [][]deploymentPod) ([]deploymentObservation, []testresults.SubjectBinding, error) {
	nodeMap := map[string]deploymentNode{}
	for _, node := range nodes {
		if !safetext.Valid(node.Metadata.Name, 256, false) || !safetext.Valid(node.Metadata.UID, 256, false) {
			return nil, nil, failure("deployment node identity unavailable")
		}
		if _, exists := nodeMap[node.Metadata.Name]; exists {
			return nil, nil, failure("ambiguous deployment node identity")
		}
		nodeMap[node.Metadata.Name] = node
	}
	if len(pods) != len(targets) {
		return nil, nil, failure("deployment selection coverage missing")
	}
	observations := []deploymentObservation{}
	subjects := []testresults.SubjectBinding{}
	seenSubjects := map[string]bool{}
	seenPods := map[string]bool{}
	for i, target := range targets {
		if len(pods[i]) != target.RequiredReplicas {
			return nil, nil, failure("deployment selection does not cover exact required replicas")
		}
		for _, pod := range pods[i] {
			if !safetext.Valid(pod.Metadata.UID, 256, false) || pod.Metadata.Namespace != target.Namespace || pod.Metadata.DeletionTimestamp != nil || pod.Status.Phase != "Running" {
				return nil, nil, failure("selected deployment pod must have live running identity")
			}
			key := pod.Metadata.UID + "/" + target.Container
			if seenPods[key] {
				return nil, nil, failure("duplicate deployment pod identity")
			}
			seenPods[key] = true
			node, exists := nodeMap[pod.Spec.NodeName]
			if !exists {
				return nil, nil, failure("deployment node identity missing")
			}
			platform := node.Status.NodeInfo.OperatingSystem + "/" + node.Status.NodeInfo.Architecture
			reference, err := nativeReference(outputs[target.Output], platform)
			if err != nil {
				return nil, nil, err
			}
			specCount, statusCount := 0, 0
			var observed deploymentObservation
			for _, container := range pod.Spec.Containers {
				if container.Name == target.Container {
					specCount++
					if container.Image != reference {
						return nil, nil, failure("PodSpec image differs from authenticated native manifest")
					}
				}
			}
			for _, container := range pod.Status.ContainerStatuses {
				if container.Name != target.Container {
					continue
				}
				statusCount++
				runtimeRef, err := exactRuntimeReference(container.ImageID)
				if err != nil {
					return nil, nil, err
				}
				if !container.Ready || container.State.Running == nil || !safetext.Valid(container.ContainerID, 512, false) || container.RestartCount == nil || *container.RestartCount < 0 || runtimeRef != reference {
					return nil, nil, failure("ready runtime container identity differs from authenticated native manifest")
				}
				observed = deploymentObservation{Target: i, PodUID: pod.Metadata.UID, ContainerID: container.ContainerID, NodeUID: node.Metadata.UID, Platform: platform, Reference: reference, ImageID: container.ImageID, Restarts: *container.RestartCount}
			}
			if specCount != 1 || statusCount != 1 {
				return nil, nil, failure("deployment must contain one selected spec and runtime container")
			}
			observations = append(observations, observed)
			subjectKey := target.Output + "/" + platform
			if !seenSubjects[subjectKey] {
				seenSubjects[subjectKey] = true
				subjects = append(subjects, testresults.SubjectBinding{Output: target.Output, Platform: platform, Subject: testresults.SubjectDescription{Ref: reference, Digest: strings.Split(reference, "@")[1]}})
			}
		}
	}
	sort.Slice(observations, func(i, j int) bool {
		a, b := observations[i], observations[j]
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		return a.PodUID < b.PodUID
	})
	sort.Slice(subjects, func(i, j int) bool {
		if subjects[i].Output != subjects[j].Output {
			return subjects[i].Output < subjects[j].Output
		}
		return subjects[i].Platform < subjects[j].Platform
	})
	return observations, subjects, nil
}
func (a *admitted) observeDeployments(ctx context.Context, workspace string, outputs map[string]lifecycle.ArtifactCheckOutput) ([]deploymentObservation, []testresults.SubjectBinding, error) {
	cluster, err := a.getObject(ctx, workspace, resourceRef{path: "/api/v1/namespaces/kube-system"})
	if err != nil {
		return nil, nil, err
	}
	if !safetext.Valid(cluster.Metadata.UID, 256, false) {
		return nil, nil, failure("cluster kube-system namespace UID unavailable")
	}
	raw, _, err := a.kube(ctx, workspace, nil, "get", "nodes", "-o", "json")
	if err != nil {
		return nil, nil, err
	}
	var nodes struct {
		Items []deploymentNode `json:"items"`
	}
	if err = decodeAPI(raw, &nodes); err != nil {
		return nil, nil, err
	}
	if len(nodes.Items) > 10000 {
		return nil, nil, failure("deployment node inventory exceeds budget")
	}
	pods := make([][]deploymentPod, len(a.options.Targets))
	for i, target := range a.options.Targets {
		raw, _, err = a.kube(ctx, workspace, nil, "get", "pods", "--namespace="+target.Namespace, "--selector="+target.LabelSelector, "-o", "json")
		if err != nil {
			return nil, nil, err
		}
		var list struct {
			Items []deploymentPod `json:"items"`
		}
		if err = decodeAPI(raw, &list); err != nil {
			return nil, nil, err
		}
		pods[i] = list.Items
	}
	observations, subjects, err := evaluateDeployments(a.options.Targets, outputs, nodes.Items, pods)
	for i := range observations {
		observations[i].KubeSystemNamespaceUID = cluster.Metadata.UID
	}
	return observations, subjects, err
}
func compareDeployments(before, after []deploymentObservation) error {
	if !reflect.DeepEqual(before, after) {
		return failure("deployment identity changed between before and after observations")
	}
	return nil
}
func deploymentFacts(observations []deploymentObservation, phase string) []testresults.EnvironmentFact {
	facts := make([]testresults.EnvironmentFact, 0, len(observations))
	for i, value := range observations {
		facts = append(facts, testresults.EnvironmentFact{Scope: "deployment", Name: fmt.Sprintf("target.%d.replica.%d.apiIdentity", value.Target, i), Phase: phase, Metadata: testresults.Metadata{Status: "observed", Value: fmt.Sprintf("kubeSystemNamespaceUID=%s podUID=%s containerID=%s nodeUID=%s platform=%s imageID=%s restartCount=%d", value.KubeSystemNamespaceUID, value.PodUID, value.ContainerID, value.NodeUID, value.Platform, value.ImageID, value.Restarts)}})
	}
	return facts
}
