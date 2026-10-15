// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/helmarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	"github.com/google/go-containerregistry/pkg/name"
	"go.yaml.in/yaml/v3"
)

const siteOVNVersion = "2.4.1"
const upstreamSiteOVNVersion = "1.4.0"
const siteOVSName = "nvkr-site-ovs"

// Distinct completed output names prevent a baseline Run from being replayed
// as the explicitly selected upstream diagnostic (including its baked disk).
func selectedOVNOutputs(network string) (version, image, chart string, err error) {
	mode, err := normalizeNetwork(network)
	if err != nil {
		return "", "", "", err
	}
	if mode == "upstream-ovn" {
		return upstreamSiteOVNVersion, "upstream-ovn-image-arm64", "upstream-ovn-chart-arm64", nil
	}
	if mode == "ovn" {
		return siteOVNVersion, "ovn-image-arm64", "ovn-chart-arm64", nil
	}
	return "", "", "", configUsage("selected network has no OVN outputs")
}

func selectedDiskOutput(network string) (string, error) {
	mode, err := normalizeNetwork(network)
	if err != nil {
		return "", err
	}
	if mode == "upstream-ovn" {
		return "nvke-node-image-upstream-ovn-arm64", nil
	}
	return DiskOutputName, nil
}

func selectedProfilesOutput(network string) (string, error) {
	mode, err := normalizeNetwork(network)
	if err != nil {
		return "", err
	}
	if mode == "upstream-ovn" {
		return "controller-profiles-upstream-ovn", nil
	}
	return "controller-profiles", nil
}

func admitSiteOVNKernel() error {
	return admitOVNKernel("/sys/module/openvswitch", "/sys/module/geneve", "/dev/net/tun")
}

func admitSiteCIDRs(kind string) error {
	pod, service := netip.MustParsePrefix(managementPodCIDR), netip.MustParsePrefix(managementServiceCIDR)
	for _, value := range []string{kind, GuestCIDR, VIPCIDR} {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Overlaps(pod) || prefix.Overlaps(service) {
			return errors.New("site OVN pod/service ranges overlap admitted Docker or tenant networks")
		}
	}
	return nil
}

// These are the six image slots owned by the selected NVIDIA fork's chart.
// NVKR binds their sha256 values during packaging; this consumer changes only
// their registry location and tag, retaining the selected image digest.
var siteOVNSlots = [][2]string{
	{"ovnkube-control-plane", "ovnkubeControlPlane"},
	{"ovnkube-single-node-zone", "ovnkubeSingleNodeZone"},
	{"ovnkube-single-node-zone", "ovnkubeSingleNodeZoneNbdb"},
	{"ovnkube-single-node-zone", "ovnkubeSingleNodeZoneSbdb"},
	{"ovnkube-single-node-zone", "ovnkubeSingleNodeZoneNorthd"},
	{"ovnkube-single-node-zone", "ovnkubeSingleNodeZoneOvnHost"},
}

func admitOVNKernel(ovs, geneve, tun string) error {
	for _, path := range []string{ovs, geneve} {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return errors.New("site OVN requires already-loaded openvswitch and geneve kernel modules before execution")
		}
	}
	info, err := os.Stat(tun)
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return errors.New("site OVN requires the host TUN character device before execution")
	}
	return nil
}

// Admit the chart/image binding using held, verified OCI payload bytes before
// creating outputs or a cluster. The shared archive owner validates the whole
// archive; the standard readers then read only its unique root values file.
func admitSiteOVNChart(ctx context.Context, chart, image SelectedOutput) error {
	return admitSiteOVNChartForNetwork(ctx, "ovn", chart, image)
}

func admitSiteOVNChartForNetwork(ctx context.Context, network string, chart, image SelectedOutput) error {
	version, imageName, chartName, err := selectedOVNOutputs(network)
	if err != nil {
		return err
	}
	if chart.Observation.Name != chartName || image.Observation.Name != imageName || chart.Observation.Kind != "helm-oci" || image.Observation.Kind != "oci" || chart.Observation.Validate() != nil || image.Observation.Validate() != nil {
		return errors.New("site OVN output identities differ from the selected network")
	}
	_, raw, err := delivery.ReadHelmChart(ctx, chart.Layout, chart.Observation.Digest, "ovn-kubernetes", version)
	if err != nil {
		return err
	}
	return admitSiteOVNArchiveForNetwork(ctx, network, raw, image.Observation.Digest)
}

func admitSiteOVNArchive(ctx context.Context, raw []byte, imageDigest string) error {
	return admitSiteOVNArchiveForNetwork(ctx, "ovn", raw, imageDigest)
}

// This raw-byte boundary remains independently safe for direct callers. Its
// shared archive inspection is intentionally repeated after ReadHelmChart:
// bytes alone do not carry the delivery owner's prior admission, and tests also
// call this boundary with unverified or malformed archives.
func admitSiteOVNArchiveForNetwork(ctx context.Context, network string, raw []byte, imageDigest string) error {
	version, _, _, err := selectedOVNOutputs(network)
	if err != nil {
		return err
	}
	identity, err := helmarchive.Inspect(ctx, raw)
	if err != nil {
		return err
	}
	if identity.Name != "ovn-kubernetes" || identity.Version != version {
		return errors.New("site OVN chart identity differs from the selected source interface")
	}
	zipped, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer func() { _ = zipped.Close() }()
	reader := tar.NewReader(zipped)
	for {
		if err = requireContext(ctx); err != nil {
			return err
		}
		header, readErr := reader.Next()
		if readErr == io.EOF {
			return errors.New("site OVN chart lacks root values")
		}
		if readErr != nil {
			return readErr
		}
		if header.Name != "ovn-kubernetes/values.yaml" {
			continue
		}
		if header.Typeflag != tar.TypeReg || header.Size > helmarchive.MaxPackageBytes {
			return errors.New("site OVN chart root values exceed the admission bound")
		}
		values, readErr := io.ReadAll(reader)
		if readErr != nil {
			return readErr
		}
		_, err = siteOVNDefaultsForNetwork(network, values, imageDigest)
		return err
	}
}

func siteOVNDefaultsForNetwork(network string, raw []byte, imageDigest string) (map[string]any, error) {
	mode, err := normalizeNetwork(network)
	if err != nil {
		return nil, err
	}
	if mode == "kind-calico" {
		return nil, configUsage("selected network has no OVN chart")
	}
	if !canonical.IsDigest(imageDigest) {
		return nil, errors.New("site OVN requires an exact accepted image digest")
	}
	var defaults map[string]any
	if err := yaml.Unmarshal(raw, &defaults); err != nil {
		return nil, errors.New("selected OVN chart values cannot be decoded")
	}
	tags := siteOVNTags()
	if mode == "upstream-ovn" {
		tags = upstreamSiteOVNTags()
	}
	actualTags, ok := defaults["tags"].(map[string]any)
	if !ok || len(actualTags) != len(tags) {
		return nil, errors.New("selected OVN chart component interface differs")
	}
	for key, expected := range tags {
		if actualTags[key] != expected {
			return nil, errors.New("selected OVN chart component interface differs")
		}
	}
	if mode == "upstream-ovn" {
		global, _ := defaults["global"].(map[string]any)
		image, _ := global["image"].(map[string]any)
		// The source-owned getImage helper joins these fields with ':'. Its
		// exact output is localhost/nvke/ovn-kubernetes@sha256:<bound hex>.
		if image["repository"] != "localhost/nvke/ovn-kubernetes@sha256" || image["tag"] != strings.TrimPrefix(imageDigest, "sha256:") {
			return nil, errors.New("selected upstream OVN chart image binding differs from the accepted image")
		}
		return defaults, nil
	}
	for _, slot := range siteOVNSlots {
		chart, _ := defaults[slot[0]].(map[string]any)
		role, _ := chart[slot[1]].(map[string]any)
		bound, _ := role["image"].(map[string]any)
		if bound["sha256"] != strings.TrimPrefix(imageDigest, "sha256:") {
			return nil, errors.New("selected OVN chart image binding differs from the accepted image")
		}
	}
	return defaults, nil
}

func siteOVNTags() map[string]any {
	return map[string]any{"ovs-node": false, "ovnkube-identity": false, "ovnkube-node-dpu-host": false, "ovnkube-single-node-zone-dpu": false, "ovnkube-crds": true, "ovnkube-control-plane": true, "ovnkube-single-node-zone": true}
}

func upstreamSiteOVNTags() map[string]any {
	return map[string]any{"ovn-ipsec": false, "ovs-node": false, "ovnkube-identity": false, "ovnkube-node-dpu-host": false, "ovnkube-single-node-zone-dpu": false, "ovnkube-control-plane": true, "ovnkube-single-node-zone": true}
}

func siteOVNValues(raw []byte, reference, api, tag string) (map[string]any, error) {
	return siteOVNValuesForNetwork("ovn", raw, reference, api, tag)
}

func siteOVNValuesForNetwork(network string, raw []byte, reference, api, tag string) (map[string]any, error) {
	mode, err := normalizeNetwork(network)
	if err != nil {
		return nil, err
	}
	image, err := name.NewDigest(reference, name.Insecure, name.StrictValidation)
	if err != nil || !canonical.IsDigest(image.DigestStr()) {
		return nil, errors.New("site OVN requires an exact accepted image reference")
	}
	if _, err = name.NewTag(image.Repository.Name()+":"+tag, name.StrictValidation); err != nil {
		return nil, errors.New("site OVN requires an explicit local image tag")
	}
	endpoint, err := url.Parse(api)
	if err != nil {
		return nil, errors.New("site OVN requires the owned IPv4 API endpoint")
	}
	address, err := netip.ParseAddr(endpoint.Hostname())
	if err != nil || !address.Is4() || !address.IsPrivate() || endpoint.Scheme != "https" || endpoint.Port() != "6443" || endpoint.User != nil || endpoint.Path != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("site OVN requires the owned IPv4 API endpoint")
	}
	defaults, err := siteOVNDefaultsForNetwork(mode, raw, image.DigestStr())
	if err != nil {
		return nil, err
	}
	values := map[string]any{
		"tags": siteOVNTags(), "k8sAPIServer": api,
		"podNetwork": managementPodCIDR + "/26", "serviceNetwork": managementServiceCIDR,
		"mtu": 1400,
		"global": map[string]any{"skipCallToK8s": true,
			"imagePullSecretName": "", "imageRepositoryPrefix": "", "useImageDigest": true},
	}
	if mode == "upstream-ovn" {
		values["tags"] = upstreamSiteOVNTags()
		values["skipCallToK8s"] = true
		values["global"] = map[string]any{"enableOvnKubeIdentity": false, "imagePullSecretName": "",
			"image": map[string]any{"repository": image.Repository.Name() + "@sha256", "tag": strings.TrimPrefix(image.DigestStr(), "sha256:"), "pullPolicy": "IfNotPresent"}}
		return values, nil
	}
	for _, slot := range siteOVNSlots {
		chart, _ := defaults[slot[0]].(map[string]any)
		role, _ := chart[slot[1]].(map[string]any)
		bound, _ := role["image"].(map[string]any)
		projected, found := values[slot[0]].(map[string]any)
		if !found {
			projected = make(map[string]any)
			values[slot[0]] = projected
		}
		projected[slot[1]] = map[string]any{"image": map[string]any{"repository": image.Repository.Name(), "tag": tag, "sha256": bound["sha256"]}}
	}
	return values, nil
}

// siteOVSBootstrap is downstream topology, not a replacement OVN chart. The
// plain pinned Kind image has no OVS service. Reuse the selected OVN image's
// source-owned ovs-server entrypoint with node-local OVS paths. The declared
// process/filesystem lifecycle follows the owned Kind node and its /var volume;
// this is not isolation from the shared kernel used by privileged source code.
func siteOVSBootstrap(reference, owner string) map[string]any {
	var volumes, mounts []any
	for _, path := range []string{"/etc/openvswitch", "/lib/modules", "/var/lib/openvswitch", "/var/log/openvswitch", "/var/run/openvswitch"} {
		key := strings.ReplaceAll(strings.TrimPrefix(path, "/"), "/", "-")
		typeName := "DirectoryOrCreate"
		if path == "/lib/modules" {
			typeName = "Directory"
		}
		volumes = append(volumes, map[string]any{"name": key, "hostPath": map[string]any{"path": path, "type": typeName}})
		mounts = append(mounts, map[string]any{"name": key, "mountPath": path, "readOnly": path == "/lib/modules"})
	}
	probe := map[string]any{"exec": map[string]any{"command": []string{"/usr/share/openvswitch/scripts/ovs-ctl", "status"}}, "timeoutSeconds": 5, "periodSeconds": 5, "failureThreshold": 3}
	return map[string]any{
		"apiVersion": "apps/v1", "kind": "DaemonSet",
		"metadata": map[string]any{"name": siteOVSName, "namespace": "kube-system", "labels": map[string]string{"dev.nvkr.demo.owner": owner}},
		"spec": map[string]any{"selector": map[string]any{"matchLabels": map[string]string{"app": siteOVSName}}, "template": map[string]any{
			"metadata": map[string]any{"labels": map[string]string{"app": siteOVSName, "dev.nvkr.demo.owner": owner}},
			"spec": map[string]any{"hostNetwork": true, "dnsPolicy": "Default", "automountServiceAccountToken": false,
				"priorityClassName": "system-node-critical", "terminationGracePeriodSeconds": 30,
				"nodeSelector": map[string]string{"kubernetes.io/os": "linux", "kubernetes.io/arch": "arm64"},
				"tolerations":  []any{map[string]string{"operator": "Exists"}}, "volumes": volumes,
				"containers": []any{map[string]any{"name": "ovs", "image": reference, "imagePullPolicy": "IfNotPresent",
					"command":         []string{"/root/ovnkube.sh", "ovs-server"},
					"securityContext": map[string]any{"runAsUser": 0, "privileged": true},
					"volumeMounts":    mounts, "readinessProbe": probe,
					"resources": map[string]any{"requests": map[string]string{"cpu": "100m", "memory": "128Mi"}, "limits": map[string]string{"memory": "1Gi"}},
				}},
			},
		}},
	}
}

func validateSiteOVNPods(rows podList, reference, owner string) (observedRuntime, error) {
	return validateSiteOVNPodsForNetwork("ovn", rows, reference, owner)
}

func validateSiteOVNPodsForNetwork(network string, rows podList, reference, owner string) (observedRuntime, error) {
	var observed observedRuntime
	version, _, _, err := selectedOVNOutputs(network)
	if err != nil {
		return observed, err
	}
	image, err := name.NewDigest(reference, name.Insecure, name.StrictValidation)
	if err != nil || !canonical.IsDigest(image.DigestStr()) {
		return observed, errors.New("site OVN observation requires the accepted image")
	}
	chartReference := image.Repository.Name() + ":" + owner + "@" + image.DigestStr()
	if version == upstreamSiteOVNVersion {
		chartReference = reference
	}
	seen := make(map[string]bool)
	for _, pod := range rows.Items {
		role, specification := "", chartReference
		var containers []string
		switch {
		case pod.Metadata.Namespace == "kube-system" && pod.Metadata.Labels["app"] == siteOVSName:
			if pod.Metadata.Labels["dev.nvkr.demo.owner"] != owner {
				return observedRuntime{}, errors.New("site OVS helper ownership differs")
			}
			role, specification, containers = "ovs", reference, []string{"ovs"}
		case pod.Metadata.Namespace == "ovn-kubernetes" && pod.Metadata.Labels["name"] == "ovnkube-control-plane":
			role, containers = "control-plane", []string{"ovnkube-cluster-manager"}
		case pod.Metadata.Namespace == "ovn-kubernetes" && pod.Metadata.Labels["app"] == "ovnkube-node":
			role, containers = "node", []string{"nb-ovsdb", "sb-ovsdb", "ovn-northd", "ovnkube-controller", "ovn-controller"}
			if version == upstreamSiteOVNVersion {
				containers = append(containers, "ovs-metrics-exporter")
			}
		case pod.Metadata.Namespace == "ovn-kubernetes":
			return observedRuntime{}, errors.New("unexpected OVN component or disabled image role")
		default:
			continue
		}
		if seen[role] || pod.Metadata.UID == "" || !pod.Spec.HostNetwork || pod.Status.Phase != "Running" || len(pod.Spec.InitContainers) != 0 || len(pod.Spec.Containers) != len(containers) || len(pod.Status.ContainerStatuses) != len(containers) {
			return observedRuntime{}, errors.New("site OVN requires exactly its owned running network pods")
		}
		seen[role] = true
		for _, container := range containers {
			count, states := 0, 0
			for _, described := range pod.Spec.Containers {
				if described.Name == container && described.Image == specification {
					count++
				}
			}
			for _, status := range pod.Status.ContainerStatuses {
				if status.Name == container && status.ImageID == reference && status.Ready && status.State.Running != nil && status.State.Terminated == nil {
					states++
				}
			}
			if count != 1 || states != 1 {
				return observedRuntime{}, errors.New("site OVN running image differs from the accepted manifest or readiness")
			}
			observed.Containers = append(observed.Containers, observedPodContainer{PodUID: pod.Metadata.UID, Namespace: pod.Metadata.Namespace, Container: container, Kind: "containers", ImageID: reference})
		}
	}
	if len(seen) != 3 {
		return observedRuntime{}, errors.New("site OVN observation omits its OVS, controller or node pod")
	}
	slices.SortFunc(observed.Containers, func(a, b observedPodContainer) int {
		return strings.Compare(a.Namespace+"\x00"+a.PodUID+"\x00"+a.Container, b.Namespace+"\x00"+b.PodUID+"\x00"+b.Container)
	})
	observed.Status = "observed"
	return observed, nil
}

type siteNetworkCapture func(context.Context, Runner, []string, string, time.Duration) ([]byte, error)

func captureSiteNetwork(ctx context.Context, runner Runner, args []string, phase string, timeout time.Duration) ([]byte, error) {
	return runner.Capture(ctx, args, phase, timeout)
}

func installSelectedSiteNetwork(ctx context.Context, selected string, session ManagementSession, tool, node string, outputs map[string]SelectedOutput, reference, api string, capture siteNetworkCapture) error {
	mode, err := normalizeNetwork(selected)
	if err != nil {
		return err
	}
	if mode != "kind-calico" {
		return installSiteNetworkForNetwork(ctx, mode, session, tool, node, outputs, reference, api, capture)
	}
	if err = requireContext(ctx); err != nil {
		return err
	}
	kube := environmentValue(session.Runner.Env, "SITE_KUBECONFIG")
	if kube == "" || capture == nil {
		return errors.New("kind diagnostic requires an explicit owned kubeconfig and command runner")
	}
	if _, err = capture(ctx, session.Runner, []string{filepath.Join(session.Tools, "kubectl"), "--kubeconfig", kube, "wait", "nodes", "--all", "--for=condition=Ready", "--timeout=180s"}, "management-ready", 210*time.Second); err != nil {
		return err
	}
	return installManagementAPI(ctx, session, capture)
}

func installSiteNetworkForNetwork(ctx context.Context, network string, session ManagementSession, tool, node string, outputs map[string]SelectedOutput, reference, api string, capture siteNetworkCapture) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	version, imageName, chartName, err := selectedOVNOutputs(network)
	if err != nil {
		return err
	}
	kube := environmentValue(session.Runner.Env, "SITE_KUBECONFIG")
	if kube == "" || node == "" || capture == nil {
		return errors.New("site OVN requires an explicit owned node, kubeconfig and command runner")
	}
	chart, ok := outputs["ovn-chart"]
	image, hasImage := outputs["ovn"]
	if !ok || !hasImage || chart.Observation.Name != chartName || chart.Observation.Kind != "helm-oci" || image.Observation.Name != imageName || image.Observation.Kind != "oci" || chart.Observation.Validate() != nil || image.Observation.Validate() != nil {
		return errors.New("site OVN requires both exact completed OVN outputs")
	}
	selected, err := name.NewDigest(reference, name.Insecure, name.StrictValidation)
	if err != nil || selected.DigestStr() != image.Observation.Digest {
		return errors.New("site OVN registry reference differs from the accepted image")
	}
	archive := filepath.Join(session.Work, "site-ovn.tgz")
	raw, err := capture(ctx, session.Runner, []string{tool, "publish", "chart", "--layout", chart.Layout, "--manifest", chart.Observation.Digest, "--name", "ovn-kubernetes", "--version", version, "--out", archive}, "site-ovn-chart", 120*time.Second)
	if err != nil {
		return err
	}
	var exported delivery.HelmChartExport
	if err = json.Unmarshal(raw, &exported); err != nil || exported.ManifestDigest != chart.Observation.Digest || exported.Name != "ovn-kubernetes" || exported.Version != version {
		return errors.New("site OVN chart extraction observation differs")
	}
	digest, err := DigestFile(ctx, archive)
	if err != nil {
		return err
	}
	if exported.ArchiveDigest != digest {
		return errors.New("site OVN chart archive differs from verified extraction")
	}
	helm, kubectl := filepath.Join(session.Tools, "helm"), filepath.Join(session.Tools, "kubectl")
	defaults, err := capture(ctx, session.Runner, []string{helm, "show", "values", archive}, "site-ovn-defaults", 30*time.Second)
	if err != nil {
		return err
	}
	values, err := siteOVNValuesForNetwork(network, defaults, reference, api, session.Name)
	if err != nil {
		return err
	}
	valuesPath, ovsPath := filepath.Join(session.Work, "site-ovn-values.json"), filepath.Join(session.Work, "site-ovs.json")
	if err = privateJSON(valuesPath, values); err != nil {
		return err
	}
	if err = privateJSON(ovsPath, siteOVSBootstrap(reference, session.Name)); err != nil {
		return err
	}
	// Never adopt an unexpected daemon/socket from the node image. Creation
	// rather than apply also rejects any pre-existing helper resource.
	for _, step := range []struct {
		args    []string
		phase   string
		timeout time.Duration
	}{
		{[]string{"docker", "exec", node, "test", "!", "-S", "/var/run/openvswitch/db.sock"}, "site-ovs-fresh", 30 * time.Second},
		{[]string{kubectl, "--kubeconfig", kube, "create", "-f", ovsPath}, "site-ovs-create", 30 * time.Second},
		{[]string{kubectl, "--kubeconfig", kube, "-n", "kube-system", "rollout", "status", "daemonset/" + siteOVSName, "--timeout=300s"}, "site-ovs-ready", 330 * time.Second},
		{[]string{helm, "--kubeconfig", kube, "install", "ovn-kubernetes", archive, "--namespace", "ovn-kubernetes", "--create-namespace", "-f", valuesPath, "--timeout", "5m"}, "site-ovn-install", 330 * time.Second},
		{[]string{kubectl, "--kubeconfig", kube, "-n", "ovn-kubernetes", "rollout", "status", "deployment/ovnkube-control-plane", "--timeout=300s"}, "site-ovn-control-plane-ready", 330 * time.Second},
		{[]string{kubectl, "--kubeconfig", kube, "-n", "ovn-kubernetes", "rollout", "status", "daemonset/ovnkube-node", "--timeout=300s"}, "site-ovn-node-ready", 330 * time.Second},
		{[]string{kubectl, "--kubeconfig", kube, "wait", "nodes", "--all", "--for=condition=Ready", "--timeout=180s"}, "management-ready", 210 * time.Second},
	} {
		if _, err = capture(ctx, session.Runner, step.args, step.phase, step.timeout); err != nil {
			return err
		}
	}
	if err = observeSelectedSiteNetwork(ctx, network, session, reference, exported, capture); err != nil {
		return err
	}
	return installManagementAPI(ctx, session, capture)
}

// A successful kubectl exit is not admission of its body or live image IDs.
// Keep this owner boundary around decoding, exact validation and persistence.
func observeSelectedSiteNetwork(ctx context.Context, network string, session ManagementSession, reference string, exported delivery.HelmChartExport, capture siteNetworkCapture) (result error) {
	boundary := beginProgress(session.Runner.Progress, "site-ovn-admission", PhaseStarted, PhaseFinished, PhaseFailed)
	defer func() { boundary.finish(result) }()
	kubectl, kube := filepath.Join(session.Tools, "kubectl"), environmentValue(session.Runner.Env, "SITE_KUBECONFIG")
	raw, err := capture(ctx, session.Runner, []string{kubectl, "--kubeconfig", kube, "get", "pods", "-A", "-o", "json"}, "site-ovn-observation", 30*time.Second)
	if err != nil {
		return err
	}
	var pods podList
	if err = json.Unmarshal(raw, &pods); err != nil {
		return errors.New("site OVN pod observation cannot be decoded")
	}
	observed, err := validateSiteOVNPodsForNetwork(network, pods, reference, session.Name)
	if err != nil {
		return err
	}
	if err = privateJSON(filepath.Join(session.Work, "site-ovn-inputs.json"), struct {
		Scope    string                   `json:"scope"`
		Chart    delivery.HelmChartExport `json:"chart"`
		Deployed observedRuntime          `json:"deployed"`
	}{"unqualified-local-site-network-diagnostic", exported, observed}); err != nil {
		return err
	}
	return nil
}

func installManagementAPI(ctx context.Context, session ManagementSession, capture siteNetworkCapture) error {
	stage := filepath.Join(session.Work, "source/deploy/stages/04-install-cluster-api.sh")
	// This source stage predates site release materialization. Keep its original
	// release-free contract in a child-only environment; subsequent site stages
	// still require the selected release and cannot inherit this override.
	capiRunner := session.Runner
	capiRunner.Env = SetEnv(session.Runner.Env, map[string]string{"STAGE_NEEDS_RELEASE": "0"})
	for _, step := range []struct {
		action, phase string
		timeout       time.Duration
	}{{"run", "install-capi", 1200 * time.Second}, {"check", "check-capi", 180 * time.Second}} {
		if _, err := capture(ctx, capiRunner, []string{"bash", stage, step.action}, step.phase, step.timeout); err != nil {
			return err
		}
	}
	return nil
}
