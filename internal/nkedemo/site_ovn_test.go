// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
)

func ovnChartArchive(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	zipped := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(zipped)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		raw := files[name]
		if err := archive.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(raw))}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zipped.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestTemporaryKindNetworkRetainsManagementAndCAPI(t *testing.T) {
	for _, mode := range []string{"kind-calico", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			var phases []string
			session := ManagementSession{Work: privateTestDir(t), Tools: "/tools", Runner: Runner{Env: []string{"SITE_KUBECONFIG=/owned/kubeconfig"}}}
			capture := func(_ context.Context, runner Runner, _ []string, phase string, _ time.Duration) ([]byte, error) {
				phases = append(phases, phase)
				if phase != "management-ready" && environmentValue(runner.Env, "STAGE_NEEDS_RELEASE") != "0" {
					t.Fatal("CAPI stage lost its source-owned contract")
				}
				return nil, nil
			}
			err := installSelectedSiteNetwork(t.Context(), mode, session, "", nil, "", "", capture)
			if mode == "invalid" {
				if err == nil || len(phases) != 0 {
					t.Fatal("unknown mode performed work", err)
				}
				return
			}
			if err != nil || !slices.Equal(phases, []string{"management-ready", "install-capi", "check-capi"}) || environmentValue(session.Runner.Env, "STAGE_NEEDS_RELEASE") != "" {
				t.Fatal("diagnostic stage order or environment differs", phases, err)
			}
		})
	}
}

func TestSiteOVNChartAdmissionReadsBoundAcceptedBytesWithoutOutputs(t *testing.T) {
	for _, scenario := range []string{"exact", "binding", "manifest", "identity", "version", "missing-values", "malformed-values", "unsafe-archive", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			digest := "sha256:" + strings.Repeat("a", 64)
			bound := strings.TrimPrefix(digest, "sha256:")
			if scenario == "binding" {
				bound = strings.Repeat("b", 64)
			}
			values, err := json.Marshal(ovnBoundValues(t, bound))
			if err != nil {
				t.Fatal(err)
			}
			files := map[string][]byte{"ovn-kubernetes/Chart.yaml": []byte("apiVersion: v2\nname: ovn-kubernetes\nversion: 1.5.18\n"), "ovn-kubernetes/values.yaml": values}
			switch scenario {
			case "identity":
				files["foreign/Chart.yaml"] = []byte("apiVersion: v2\nname: foreign\nversion: 1.5.18\n")
				files["foreign/values.yaml"] = values
				delete(files, "ovn-kubernetes/Chart.yaml")
				delete(files, "ovn-kubernetes/values.yaml")
			case "version":
				files["ovn-kubernetes/Chart.yaml"] = []byte("apiVersion: v2\nname: ovn-kubernetes\nversion: 1.5.19\n")
			case "missing-values":
				delete(files, "ovn-kubernetes/values.yaml")
			case "malformed-values":
				files["ovn-kubernetes/values.yaml"] = []byte("{malformed")
			case "unsafe-archive":
				files["ovn-kubernetes/../escape"] = []byte("rejected")
			}
			raw := ovnChartArchive(t, files)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "canceled" {
				cancel()
			}
			err = admitSiteOVNArchive(ctx, raw, digest)
			if (err == nil) != (scenario == "exact" || scenario == "manifest") {
				t.Fatalf("archive admission %s: %v", scenario, err)
			}
			if err != nil {
				return
			}
			root := privateTestDir(t)
			layout := filepath.Join(root, "chart")
			if _, err = delivery.BuildHelmChartArtifact(ctx, layout, "application/vnd.cncf.helm.chart.v1", raw); err != nil {
				t.Fatal(err)
			}
			descriptor, err := delivery.PayloadDescriptor(ctx, layout)
			if err != nil {
				t.Fatal(err)
			}
			chart := SelectedOutput{Layout: layout, Observation: lifecycle.Artifact{Name: "ovn-chart-arm64", Kind: "helm-oci", Digest: descriptor.Digest}}
			image := SelectedOutput{Observation: lifecycle.Artifact{Name: "ovn-image-arm64", Kind: "oci", Digest: digest}}
			if scenario == "manifest" {
				chart.Observation.Digest = "sha256:" + strings.Repeat("b", 64)
			}
			err = admitSiteOVNChart(ctx, chart, image)
			if (err == nil) != (scenario == "exact") {
				t.Fatalf("held OCI binding admission %s: %v", scenario, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 || entries[0].Name() != "chart" {
				t.Fatalf("admission wrote output: %v %v", entries, err)
			}
		})
	}
}

func ovnBoundValues(t *testing.T, digest string) map[string]any {
	t.Helper()
	values := map[string]any{"tags": map[string]any{"ovs-node": false, "ovnkube-identity": false, "ovnkube-node-dpu-host": false, "ovnkube-single-node-zone-dpu": false, "ovnkube-crds": true, "ovnkube-control-plane": true, "ovnkube-single-node-zone": true}}
	for _, slot := range siteOVNSlots {
		roles, ok := values[slot[0]].(map[string]any)
		if !ok {
			roles = make(map[string]any)
			values[slot[0]] = roles
		}
		roles[slot[1]] = map[string]any{"image": map[string]any{"repository": "nvcr.io/nv-ngn/sdn/ovnkube", "tag": "source", "sha256": digest}}
	}
	return values
}

func TestSiteOVNValuesPreserveEveryBoundImageAndDisableForeignComponents(t *testing.T) {
	digest := strings.Repeat("a", 64)
	for _, scenario := range []string{"exact", "missing-role", "wrong-binding", "enabled-ovs", "enabled-dpu", "extra-tag", "malformed", "tag-only", "secret-reference", "wrong-api", "secret-api"} {
		t.Run(scenario, func(t *testing.T) {
			defaults := ovnBoundValues(t, digest)
			reference, api := "172.18.0.2:5000/nkd-local/ovn@sha256:"+digest, "https://172.18.0.3:6443"
			role := defaults["ovnkube-single-node-zone"].(map[string]any)
			switch scenario {
			case "missing-role":
				delete(role, "ovnkubeSingleNodeZoneSbdb")
			case "wrong-binding":
				role["ovnkubeSingleNodeZoneSbdb"].(map[string]any)["image"].(map[string]any)["sha256"] = strings.Repeat("b", 64)
			case "enabled-ovs":
				defaults["tags"].(map[string]any)["ovs-node"] = true
			case "enabled-dpu":
				defaults["tags"].(map[string]any)["ovnkube-node-dpu-host"] = true
			case "extra-tag":
				defaults["tags"].(map[string]any)["foreign"] = true
			case "tag-only":
				reference = "172.18.0.2:5000/nkd-local/ovn:mutable"
			case "secret-reference":
				reference = "private-secret@invalid"
			case "wrong-api":
				api = "http://172.18.0.3:6443"
			case "secret-api":
				api = "https://user:private-secret@172.18.0.3:6443"
			}
			raw, err := json.Marshal(defaults)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "malformed" {
				raw = []byte("{private-secret")
			}
			values, err := siteOVNValues(raw, reference, api, "owned")
			if scenario != "exact" {
				if err == nil || values != nil || strings.Contains(err.Error(), "private-secret") {
					t.Fatalf("unsafe values accepted or diagnostics leaked: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, slot := range siteOVNSlots {
				image := values[slot[0]].(map[string]any)[slot[1]].(map[string]any)["image"].(map[string]any)
				if image["repository"] != "172.18.0.2:5000/nkd-local/ovn" || image["sha256"] != digest || image["tag"] != "owned" {
					t.Fatalf("lost bound source role: %v", slot)
				}
			}
			for _, disabled := range []string{"ovs-node", "ovnkube-identity", "ovnkube-node-dpu-host", "ovnkube-single-node-zone-dpu"} {
				if values["tags"].(map[string]any)[disabled] != false {
					t.Fatal("enabled a foreign image", disabled)
				}
			}
			global := values["global"].(map[string]any)
			if global["useImageDigest"] != true || global["enableNgnExtension"] != false || global["imagePullSecretName"] != "" || values["podNetwork"] != managementPodCIDR+"/26" || values["serviceNetwork"] != managementServiceCIDR {
				t.Fatal("OVN values differ from admitted local topology")
			}
		})
	}
}

func TestSiteOVNKernelAndCIDRsRejectUnavailableCapabilities(t *testing.T) {
	for _, scenario := range []string{"exact", "missing-ovs", "missing-geneve", "regular-tun", "missing-tun"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			ovs, geneve, tun := root, root, os.DevNull
			switch scenario {
			case "missing-ovs":
				ovs = filepath.Join(root, "absent")
			case "missing-geneve":
				geneve = filepath.Join(root, "absent")
			case "regular-tun":
				tun = filepath.Join(root, "regular")
				if err := os.WriteFile(tun, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-tun":
				tun = filepath.Join(root, "absent")
			}
			if err := admitOVNKernel(ovs, geneve, tun); (err == nil) != (scenario == "exact") {
				t.Fatalf("kernel admission: %v", err)
			}
		})
	}
	for _, cidr := range []string{"172.18.0.0/16", "10.244.0.0/16", "10.240.0.0/12", "10.96.0.0/16", "10.0.0.0/8", "unknown"} {
		t.Run(cidr, func(t *testing.T) {
			if err := admitSiteCIDRs(cidr); (err == nil) != (cidr == "172.18.0.0/16") {
				t.Fatalf("network overlap admission: %v", err)
			}
		})
	}
}

func ovnPodsFixture(reference, owner string) podList {
	var rows podList
	for _, row := range []struct {
		namespace, label, value string
		containers              []string
	}{
		{"kube-system", "app", siteOVSName, []string{"ovs"}},
		{"ovn-kubernetes", "name", "ovnkube-control-plane", []string{"ovnkube-cluster-manager"}},
		{"ovn-kubernetes", "app", "ovnkube-node", []string{"nb-ovsdb", "sb-ovsdb", "ovn-northd", "ovnkube-controller", "ovn-controller"}},
	} {
		var pod sitePod
		pod.Metadata = podMetadata{UID: row.value + "-uid", Namespace: row.namespace, Labels: map[string]string{row.label: row.value, "dev.nkd.demo.owner": owner}}
		pod.Spec.HostNetwork, pod.Status.Phase = true, "Running"
		spec := reference
		if row.namespace == "ovn-kubernetes" {
			repository, digest, _ := strings.Cut(reference, "@")
			spec = repository + ":" + owner + "@" + digest
		}
		for _, container := range row.containers {
			pod.Spec.Containers = append(pod.Spec.Containers, podContainer{Name: container, Image: spec})
			status := podContainerState{Name: container, ImageID: reference, Ready: true}
			running := json.RawMessage("{}")
			status.State.Running = &running
			pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, status)
		}
		rows.Items = append(rows.Items, pod)
	}
	return rows
}

func TestSiteOVNObservationRequiresAllSelectedRunningRoles(t *testing.T) {
	reference := "172.18.0.2:5000/nkd-local/ovn@sha256:" + strings.Repeat("a", 64)
	for _, scenario := range []string{"exact", "missing-helper", "missing-controller", "missing-node", "duplicate-node", "foreign-owner", "foreign-image", "foreign-image-id", "not-ready", "missing-status", "extra-container", "missing-uid", "not-host-network", "disabled-component"} {
		t.Run(scenario, func(t *testing.T) {
			rows := ovnPodsFixture(reference, "owned")
			switch scenario {
			case "missing-helper":
				rows.Items = rows.Items[1:]
			case "missing-controller":
				rows.Items = append(rows.Items[:1], rows.Items[2])
			case "missing-node":
				rows.Items = rows.Items[:2]
			case "duplicate-node":
				rows.Items = append(rows.Items, rows.Items[2])
			case "foreign-owner":
				rows.Items[0].Metadata.Labels["dev.nkd.demo.owner"] = "foreign"
			case "foreign-image":
				rows.Items[2].Spec.Containers[0].Image = "nvcr.io/foreign:mutable"
			case "foreign-image-id":
				rows.Items[2].Status.ContainerStatuses[0].ImageID = "nvcr.io/foreign@sha256:" + strings.Repeat("b", 64)
			case "not-ready":
				rows.Items[2].Status.ContainerStatuses[0].Ready = false
			case "missing-status":
				rows.Items[2].Status.ContainerStatuses = rows.Items[2].Status.ContainerStatuses[1:]
			case "extra-container":
				rows.Items[2].Spec.Containers = append(rows.Items[2].Spec.Containers, podContainer{Name: "foreign"})
			case "missing-uid":
				rows.Items[2].Metadata.UID = ""
			case "not-host-network":
				rows.Items[2].Spec.HostNetwork = false
			case "disabled-component":
				rows.Items = append(rows.Items, sitePod{Metadata: podMetadata{Namespace: "ovn-kubernetes", Labels: map[string]string{"app": "ovs-node"}}})
			}
			observed, err := validateSiteOVNPods(rows, reference, "owned")
			if scenario == "exact" {
				if err != nil || observed.Status != "observed" || len(observed.Containers) != 7 {
					t.Fatalf("OVN observation: %v %v", observed, err)
				}
			} else if err == nil || len(observed.Containers) != 0 {
				t.Fatalf("unsafe OVN observation: %v %v", observed, err)
			}
		})
	}
}

func TestSiteOVNBootstrapUsesNodeLocalPaths(t *testing.T) {
	reference := "172.18.0.2:5000/nkd-local/ovn@sha256:" + strings.Repeat("a", 64)
	manifest := siteOVSBootstrap(reference, "owned")
	pod := manifest["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	if pod["hostNetwork"] != true || pod["automountServiceAccountToken"] != false {
		t.Fatal("OVS helper scope differs")
	}
	container := pod["containers"].([]any)[0].(map[string]any)
	if container["image"] != reference || !slices.Equal(container["command"].([]string), []string{"/root/ovnkube.sh", "ovs-server"}) {
		t.Fatal("OVS helper replaced source implementation/image")
	}
	var paths []string
	for _, volume := range pod["volumes"].([]any) {
		host := volume.(map[string]any)["hostPath"].(map[string]any)
		paths = append(paths, host["path"].(string))
	}
	if !slices.Equal(paths, []string{"/etc/openvswitch", "/lib/modules", "/var/lib/openvswitch", "/var/log/openvswitch", "/var/run/openvswitch"}) {
		t.Fatal("OVS helper mounts unrelated host paths", paths)
	}
	for _, mount := range container["volumeMounts"].([]any) {
		row := mount.(map[string]any)
		if row["mountPath"] == "/lib/modules" && row["readOnly"] != true {
			t.Fatal("OVS helper permits writes to kernel module files")
		}
	}
}

func TestSiteOVNInstallationOrdersReadinessBeforeCAPIAndStopsOnFailure(t *testing.T) {
	for _, failed := range []string{"", "binding", "site-ovs-ready", "site-ovn-node-ready", "site-ovn-observation"} {
		t.Run(failed, func(t *testing.T) {
			work := privateTestDir(t)
			digest := "sha256:" + strings.Repeat("a", 64)
			reference := "172.18.0.2:5000/nkd-local/ovn@" + digest
			outputs := map[string]SelectedOutput{
				"ovn":       {Observation: lifecycle.Artifact{Name: "ovn-image-arm64", Kind: "oci", Digest: digest}},
				"ovn-chart": {Layout: "selected-chart", Observation: lifecycle.Artifact{Name: "ovn-chart-arm64", Kind: "helm-oci", Digest: "sha256:" + strings.Repeat("b", 64)}},
			}
			session := ManagementSession{Name: "owned", Work: work, Tools: "selected-tools", Runner: Runner{Env: []string{"SITE_KUBECONFIG=owned-kubeconfig", "STAGE_NEEDS_RELEASE=1"}}}
			var phases []string
			capture := func(ctx context.Context, runner Runner, args []string, phase string, timeout time.Duration) ([]byte, error) {
				if ctx != t.Context() || timeout <= 0 || timeout > 20*time.Minute {
					t.Fatal("unbounded or substituted command context")
				}
				wantRelease := "1"
				if phase == "install-capi" || phase == "check-capi" {
					wantRelease = "0"
				}
				if environmentValue(runner.Env, "STAGE_NEEDS_RELEASE") != wantRelease || environmentValue(runner.Env, "RELEASE_FILE") != "" || environmentValue(runner.Env, "SITE_KUBECONFIG") != "owned-kubeconfig" {
					t.Fatal("network/CAPI child environment violates the source stage contract")
				}
				phases = append(phases, phase)
				if phase == failed {
					return nil, &CommandError{Phase: phase, ExitCode: 1, Cause: errors.New("failed")}
				}
				switch phase {
				case "site-ovn-chart":
					path := filepath.Join(work, "site-ovn.tgz")
					if !slices.Contains(args, outputs["ovn-chart"].Observation.Digest) || !slices.Contains(args, siteOVNVersion) || !slices.Contains(args, "ovn-kubernetes") {
						t.Fatal("extraction did not select exact chart identity")
					}
					if err := os.WriteFile(path, []byte("controlled chart archive; not an external Helm runtime"), 0o600); err != nil {
						t.Fatal(err)
					}
					sum, err := DigestFile(ctx, path)
					if err != nil {
						t.Fatal(err)
					}
					return json.Marshal(delivery.HelmChartExport{ManifestDigest: outputs["ovn-chart"].Observation.Digest, ArchiveDigest: sum, Name: "ovn-kubernetes", Version: siteOVNVersion})
				case "site-ovn-defaults":
					binding := strings.TrimPrefix(digest, "sha256:")
					if failed == "binding" {
						binding = strings.Repeat("c", 64)
					}
					return json.Marshal(ovnBoundValues(t, binding))
				case "site-ovn-observation":
					return json.Marshal(ovnPodsFixture(reference, "owned"))
				case "site-ovn-install":
					if !slices.Contains(args, "--create-namespace") {
						t.Fatal("fresh Helm release namespace must exist before Secret storage")
					}
					return nil, nil
				default:
					return nil, nil
				}
			}
			err := installSiteNetwork(t.Context(), session, "selected-nkdctl", outputs, reference, "https://172.18.0.3:6443", capture)
			if environmentValue(session.Runner.Env, "STAGE_NEEDS_RELEASE") != "1" {
				t.Fatal("CAPI override escaped its scoped child runner")
			}
			if failed != "" {
				if err == nil || slices.Contains(phases, "install-capi") || failed == "binding" && slices.Contains(phases, "site-ovs-create") {
					t.Fatalf("failed network reached CAPI/deployment: %v %v", phases, err)
				}
				return
			}
			want := []string{"site-ovn-chart", "site-ovn-defaults", "site-ovs-fresh", "site-ovs-create", "site-ovs-ready", "site-ovn-install", "site-ovn-control-plane-ready", "site-ovn-node-ready", "management-ready", "site-ovn-observation", "install-capi", "check-capi"}
			if err != nil || !slices.Equal(phases, want) {
				t.Fatalf("network/CAPI ordering: %v %v", phases, err)
			}
			if _, err := os.Stat(filepath.Join(work, "site-ovn-inputs.json")); err != nil {
				t.Fatal("missing exact runtime image observation", err)
			}
		})
	}
}
