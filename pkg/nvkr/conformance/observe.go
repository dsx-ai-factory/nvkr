// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
	digest "github.com/opencontainers/go-digest"
)

func (a *admitted) observe(ctx context.Context, workspace, phase string) ([]testresults.EnvironmentFact, error) {
	env := []testresults.EnvironmentFact{}
	add := func(key, value, evidence string) {
		status := "declared"
		if evidence == "cluster-api" || evidence == "held-bytes" {
			status = "observed"
		}
		env = append(env, testresults.EnvironmentFact{Scope: "cluster", Name: key, Phase: phase, Metadata: testresults.Metadata{Status: status, Value: value}})
	}
	add("role", a.options.Role, "declared")
	for _, fact := range []struct{ name, value string }{{"tools.hydrophone.digest", a.hydrophone.Digest()}, {"tools.kubectl.digest", a.kubectl.Digest()}, {"conformance.imageVersion.declared", a.options.ImageVersion}, {"busybox.imageDigest", a.options.BusyboxImage[strings.LastIndex(a.options.BusyboxImage, "@")+1:]}} {
		evidence := "declared"
		if strings.HasPrefix(fact.name, "tools.") {
			evidence = "held-bytes"
		}
		add(fact.name, fact.value, evidence)
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
		Items []deploymentNode `json:"items"`
	}
	if err = decodeAPI(raw, &nodes); err != nil {
		return env, err
	}
	if len(nodes.Items) == 0 || len(nodes.Items) > 10000 {
		return env, failure("cluster node inventory unavailable or over budget")
	}
	// Refuse an unrepresentable phase before adding per-node rows. The caller
	// reserves the complete before/after graph separately before suite resources.
	if len(env)+10*len(nodes.Items)+7 > testresults.MaxEnvironmentFacts {
		return env, projectionFailure(nil)
	}
	arch, os, kernel, runtime := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, n := range nodes.Items {
		info := n.Status.NodeInfo
		if !safetext.Valid(n.Metadata.Name, 256, false) || !safetext.Valid(n.Metadata.UID, 256, false) {
			return env, failure("node identity unavailable or outside public projection")
		}
		for _, fact := range []struct{ name, value string }{
			{"name", n.Metadata.Name}, {"architecture", info.Architecture},
			{"operatingSystem", info.OperatingSystem}, {"osImage", info.OSImage},
			{"kernelVersion", info.KernelVersion}, {"containerRuntimeVersion", info.ContainerRuntimeVersion},
			{"kubeletVersion", info.KubeletVersion}, {"machineID", info.MachineID},
			{"bootID", info.BootID}, {"systemUUID", info.SystemUUID},
		} {
			metadata := testresults.Metadata{Status: "unavailable"}
			if fact.value != "" {
				if !safetext.Valid(fact.value, 1024, false) {
					return env, failure("node information outside public projection")
				}
				metadata = testresults.Metadata{Status: "observed", Value: fact.value}
			}
			env = append(env, testresults.EnvironmentFact{Scope: "node:" + n.Metadata.UID, Name: fact.name, Phase: phase, Metadata: metadata})
		}
		for _, value := range []string{info.Architecture, info.OperatingSystem, info.KernelVersion, info.ContainerRuntimeVersion} {
			if !publicToken(value) {
				return env, failure("node runtime fact is missing or outside public projection")
			}
		}
		platform := info.OperatingSystem + "/" + info.Architecture
		if platform != a.options.ConformanceImage.Platform || platform != a.options.Busybox.Platform {
			return env, failure("selected suite images do not match every observed node platform")
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
	sort.Slice(env, func(i, j int) bool {
		if env[i].Scope != env[j].Scope {
			return env[i].Scope < env[j].Scope
		}
		return env[i].Name < env[j].Name
	})
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
