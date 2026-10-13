// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"context"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
	digest "github.com/opencontainers/go-digest"
)

// Observe queries a caller-selected existing cluster. Identifiers, endpoint,
// kubeconfig, node names and private labels are excluded from the public result.
// Role remains an operator declaration; all API facts remain unauthenticated.
func Observe(ctx context.Context, o Options, phase string) (testobservation.Environment, error) {
	if phase != "before" && phase != "after" {
		return testobservation.Environment{}, e.New(e.CodeUsage, "observation phase must be before or after")
	}
	a, err := admit(ctx, o)
	if err != nil {
		return testobservation.Environment{}, err
	}
	defer a.close()
	workspace, err := os.MkdirTemp("", "nvkr-conformance-observe-")
	if err != nil {
		return testobservation.Environment{}, e.Wrap(e.CodeFetchFailed, err, "creating private observation workspace")
	}
	defer os.RemoveAll(workspace)
	return a.observe(ctx, workspace, phase)
}
func (a *admitted) observe(ctx context.Context, workspace, phase string) (testobservation.Environment, error) {
	env := testobservation.Environment{Role: a.options.Observation.Environment.Role}
	add := func(key, value, evidence string) {
		env.Facts = append(env.Facts, testobservation.Fact{Name: key, Value: value, Evidence: evidence, Phase: phase})
	}
	add("role", env.Role, "declared")
	for _, fact := range []struct{ name, value string }{{"tools.hydrophone.digest", a.options.Hydrophone.Digest}, {"tools.kubectl.digest", a.options.Kubectl.Digest}, {"harness.revision.declared", a.options.Observation.Suite.HarnessRevision}, {"conformance.imageVersion.declared", a.options.ImageVersion}, {"busybox.imageDigest", a.options.BusyboxImage[strings.LastIndex(a.options.BusyboxImage, "@")+1:]}} {
		add(fact.name, fact.value, "declared")
	}
	raw, _, err := a.kube(ctx, workspace, nil, "get", "--raw=/version")
	if err != nil {
		return env, err
	}
	var version struct {
		GitVersion string `json:"gitVersion"`
	}
	if err = decodeAPI(raw, &version); err != nil {
		return env, err
	}
	live, err := semver.NewVersion(version.GitVersion)
	if err != nil {
		return env, failure("cluster API version unavailable")
	}
	expected, _ := semver.StrictNewVersion(strings.TrimPrefix(a.options.ImageVersion, "v"))
	if live.Major() != expected.Major() || live.Minor() != expected.Minor() {
		return env, failure("conformance image and server major/minor versions differ")
	}
	add("kubernetes.version", live.String(), "cluster-api")
	raw, _, err = a.kube(ctx, workspace, nil, "get", "nodes", "-o", "json")
	if err != nil {
		return env, err
	}
	var nodes struct {
		Items []struct {
			Status struct {
				NodeInfo struct {
					Architecture            string `json:"architecture"`
					OperatingSystem         string `json:"operatingSystem"`
					OSImage                 string `json:"osImage"`
					KernelVersion           string `json:"kernelVersion"`
					ContainerRuntimeVersion string `json:"containerRuntimeVersion"`
				} `json:"nodeInfo"`
			} `json:"status"`
		} `json:"items"`
	}
	if err = decodeAPI(raw, &nodes); err != nil {
		return env, err
	}
	if len(nodes.Items) == 0 || len(nodes.Items) > 10000 {
		return env, failure("cluster node inventory unavailable or over budget")
	}
	arch, os, kernel, runtime := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, n := range nodes.Items {
		info := n.Status.NodeInfo
		for _, value := range []string{info.Architecture, info.OperatingSystem, info.KernelVersion, info.ContainerRuntimeVersion} {
			if !publicToken(value) {
				return env, failure("node runtime fact is missing or outside public projection")
			}
		}
		arch[info.Architecture] = true
		os[info.OperatingSystem] = true
		kernel[info.KernelVersion] = true
		runtime[info.ContainerRuntimeVersion] = true
	}
	add("nodes.count", strconv.Itoa(len(nodes.Items)), "cluster-api")
	for _, fact := range []struct {
		name   string
		values map[string]bool
	}{{"nodes.architectures", arch}, {"nodes.operatingSystems", os}, {"nodes.kernelVersions", kernel}, {"nodes.containerRuntimes", runtime}} {
		value, err := aggregate(fact.values)
		if err != nil {
			return env, err
		}
		add(fact.name, value, "cluster-api")
	}
	// The caller declares which pods implement CNI; that classification is not
	// inferred from arbitrary installed workloads or promoted to API evidence.
	add("cni.selection", "operator-selected kube-system pods", "declared")
	raw, _, err = a.kube(ctx, workspace, nil, "get", "pods", "--namespace=kube-system", "--selector="+a.options.CNISelector, "-o", "json")
	if err != nil {
		return env, err
	}
	var pods struct {
		Items []struct {
			Status struct {
				ContainerStatuses []struct {
					ImageID string `json:"imageID"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err = decodeAPI(raw, &pods); err != nil {
		return env, err
	}
	if len(pods.Items) == 0 || len(pods.Items) > 10000 {
		return env, failure("selected CNI runtime pods unavailable or over budget")
	}
	ids := map[string]bool{}
	for _, p := range pods.Items {
		for _, c := range p.Status.ContainerStatuses {
			id, err := projectImageIdentifier(c.ImageID)
			if err != nil {
				return env, err
			}
			ids[id] = true
		}
	}
	value, err := aggregate(ids)
	if err != nil {
		return env, err
	}
	add("cni.runtimeImageIdentifiers", value, "cluster-api")
	sort.Slice(env.Facts, func(i, j int) bool { return env.Facts[i].Name < env.Facts[j].Name })
	return env, nil
}
func publicToken(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(".-+_:()/", r)) {
			return false
		}
	}
	return true
}
func aggregate(values map[string]bool) (string, error) {
	if len(values) == 0 {
		return "", failure("runtime facts unavailable")
	}
	out := make([]string, 0, len(values))
	for v := range values {
		out = append(out, v)
	}
	sort.Strings(out)
	value := strings.Join(out, ",")
	if len(value) > 4096 {
		return "", failure("public fact aggregate exceeds budget")
	}
	return value, nil
}

func projectImageIdentifier(id string) (string, error) {
	if i := strings.LastIndex(id, "@"); i >= 0 {
		id = id[i+1:]
	} else if i := strings.Index(id, "://"); i >= 0 {
		id = id[i+3:]
	}
	parsed, err := digest.Parse(id)
	if err != nil || parsed.Algorithm() != digest.SHA256 {
		return "", failure("CNI runtime image identifier cannot be projected as SHA256")
	}
	return parsed.String(), nil
}
