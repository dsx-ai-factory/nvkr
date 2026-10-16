// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestNativeRetainedStatusObservesBothClustersAndPreservesFailures(t *testing.T) {
	for _, tc := range []struct {
		name                string
		unhealthy, canceled bool
		missing             bool
		wantError           bool
	}{
		{name: "healthy"},
		{name: "health changed", unhealthy: true},
		{name: "canceled", canceled: true, wantError: true},
		{name: "missing config", missing: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			access, runner := operatorStatusFixture(t)
			access.siteQCOW = true
			var output bytes.Buffer
			runner.Progress = &output
			if tc.unhealthy {
				runner.Env = SetEnv(runner.Env, map[string]string{"READY_REPLY": "not ready"})
			}
			if tc.missing {
				access.tenant = filepath.Join(t.TempDir(), "missing")
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			err := presentNativeStatus(ctx, runner, access)
			if (err != nil) != tc.wantError {
				t.Fatalf("status error=%v; wantError=%v", err, tc.wantError)
			}
			if tc.wantError {
				return
			}
			for _, label := range []string{"Site / native QCOW2", "Tenant / QCOW2"} {
				if !strings.Contains(output.String(), label) {
					t.Fatalf("missing native cluster observation %q: %s", label, &output)
				}
			}
			if tc.unhealthy && !strings.Contains(output.String(), "readiness not confirmed") {
				t.Fatal("changed cluster health was hidden")
			}
			if strings.Contains(output.String(), "Kind management") || strings.Contains(output.String(), "PASS") {
				t.Fatal("native observation described a different runtime or granted acceptance")
			}
		})
	}
}

func TestNativeSiteSelectsExactlyOneInstalledSpirePackageUnit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		units []string
		want  string
	}{
		{"current", []string{"nvke-spire-configure.service"}, "restart nvke-spire-configure.service"},
		{"weekend", []string{"nke-spire-configure.service"}, "restart nke-spire-configure.service"},
		{"missing", nil, ""},
		{"ambiguous", []string{"nvke-spire-configure.service", "nke-spire-configure.service"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "called")
			for _, unit := range tc.units {
				if err := os.WriteFile(filepath.Join(root, unit), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			shim := filepath.Join(root, "systemctl")
			if err := os.WriteFile(shim, []byte("#!/bin/sh\nprintf '%s' \"$*\" > "+operatorQuote(marker)+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), "bash", "-euc", strings.ReplaceAll(siteSpireConfigurationScript, "/lib/systemd/system/", root+"/"))
			command.Env = []string{"PATH=" + root + ":/usr/bin:/bin"}
			output, err := command.CombinedOutput()
			if tc.want == "" {
				if err == nil {
					t.Fatal("missing or ambiguous package unit accepted")
				}
				if _, err := os.Stat(marker); !os.IsNotExist(err) {
					t.Fatal("rejected unit selection reached systemctl")
				}
				return
			}
			if err != nil {
				t.Fatalf("unit selection: %v: %s", err, output)
			}
			data, err := os.ReadFile(marker)
			if err != nil || string(data) != tc.want {
				t.Fatalf("systemctl dispatch=%q error=%v", data, err)
			}
		})
	}
}

func TestNativeSiteHandoffObservesRuntimeExitAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		exited, callback, cancel bool
		want                     error
	}{
		{"callback", false, true, false, nil},
		{"startup failure", false, true, false, os.ErrPermission},
		{"runtime already exited", true, false, false, os.ErrPermission},
		{"exit supersedes queued callback", true, true, false, os.ErrPermission},
		{"canceled", false, false, true, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := &OwnedProcess{done: make(chan struct{}), waitErr: os.ErrPermission}
			if tc.exited {
				close(server.done)
			}
			handoff := &nativeHandoff{result: make(chan error, 1)}
			if tc.callback {
				if tc.exited {
					handoff.result <- nil
				} else {
					handoff.result <- tc.want
				}
			}
			if tc.cancel {
				cancel()
			}
			err := waitNativeSiteHandoff(ctx, handoff, server)
			if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("handoff returned %v; want %v", err, tc.want)
			}
		})
	}
}

func TestNativeSiteSeedProjectsSharedTenantBudgetAndPreservesSource(t *testing.T) {
	const fixture = `tenants: [{id: original, name: unchanged}]
instance_types:
  - {id: a1b2c3d4-0005-4000-8000-000000000004, name: cpu6-mem32gb-disk60gb, site_id: a1b2c3d4-0001-4000-8000-000000000001, gpu_count: 0, cpu_count: 6, memory_gib: 32, disk_gib: 60, extra: preserve-site}
  - {id: a1b2c3d4-0005-4000-8000-000000000003, name: cpu4-mem6gb-disk60gb, site_id: a1b2c3d4-0001-4000-8000-000000000001, gpu_count: 0, cpu_count: 4, memory_gib: 6, disk_gib: 60, extra: preserve-tenant}
  - {id: unrelated, name: untouched, cpu_count: 2, memory_gib: 4, disk_gib: 50}
`
	type testCase struct {
		name   string
		change func(map[string]any)
	}
	cases := []testCase{{name: "known source"}}
	for index, role := range []string{"site", "tenant"} {
		cases = append(cases,
			testCase{role + " missing", func(doc map[string]any) {
				items := doc["instance_types"].([]any)
				doc["instance_types"] = append(items[:index], items[index+1:]...)
			}},
			testCase{role + " duplicate", func(doc map[string]any) {
				items := doc["instance_types"].([]any)
				doc["instance_types"] = append(items, items[index])
			}},
		)
		for field, value := range map[string]any{"id": "unknown", "name": "other", "site_id": "other", "gpu_count": 1, "cpu_count": 3, "memory_gib": 16, "disk_gib": 61} {
			cases = append(cases, testCase{role + " changed " + field, func(doc map[string]any) {
				doc["instance_types"].([]any)[index].(map[string]any)[field] = value
			}})
		}
	}
	cases = append(cases, testCase{"malformed instance type", func(doc map[string]any) {
		doc["instance_types"] = append(doc["instance_types"].([]any), "invalid")
	}})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var expected map[string]any
			if err := yaml.Unmarshal([]byte(fixture), &expected); err != nil {
				t.Fatal(err)
			}
			if tc.change != nil {
				tc.change(expected)
			}
			raw, err := yaml.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			source := filepath.Join(dir, "seed.yaml")
			if err := os.WriteFile(source, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			path, err := nativeSiteSeed(t.Context(), source, dir)
			original, readErr := os.ReadFile(source)
			if readErr != nil || !bytes.Equal(original, raw) {
				t.Fatal("source seed changed")
			}
			if tc.change != nil {
				if err == nil || path != "" {
					t.Fatalf("unknown or ambiguous source accepted: path=%q error=%v", path, err)
				}
				if _, err := os.Stat(filepath.Join(dir, "native-seed.yaml")); !os.IsNotExist(err) {
					t.Fatal("rejected source produced output")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var actual map[string]any
			if err := yaml.Unmarshal(data, &actual); err != nil {
				t.Fatal(err)
			}
			items := expected["instance_types"].([]any)
			site := items[0].(map[string]any)
			site["name"], site["cpu_count"], site["memory_gib"] = "native-site-cpu4-mem12gb-disk60gb", 4, 12
			tenant := items[1].(map[string]any)
			tenant["name"], tenant["memory_gib"] = "cpu4-mem8gb-disk60gb", 8
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("runtime seed changed fields outside selected sizing: got=%v want=%v", actual, expected)
			}
			for _, network := range []string{"ovn", "upstream-ovn"} {
				request, err := newTenantRequest("native-demo-tenant", network)
				if err != nil || !reflect.DeepEqual(request.ControlPlane.NICo.Types, []string{tenant["id"].(string)}) || len(request.NodePools) != 1 || request.NodePools[0].NICo.Type != tenant["id"] {
					t.Fatalf("control plane and worker must select the projected 8 GiB type: %+v; %v", request, err)
				}
			}
			legacy := []byte(fmt.Sprintf("  - id: %q\n    name: \"cpu4-mem6gb-disk60gb\"\n    site_id: %q\n    gpu_count: 0\n    cpu_count: 4\n    memory_gib: 6\n    disk_gib: 60\n", TenantInstanceType, SiteID))
			projected, err := projectSeedFixture(legacy, plainSHA(legacy))
			if err != nil || !bytes.Contains(projected, []byte("memory_gib: 8")) {
				t.Fatalf("legacy and native tenant policy diverged: %s; %v", projected, err)
			}
		})
	}
}

func TestNativeSiteStartupNeverCopiesOrRunsHostCLI(t *testing.T) {
	cfg := startupDescription()
	cfg.Deployment = &NativeSiteDeployment{NicoURL: "http://172.30.0.1:18080", ImageURL: "http://172.30.0.1:18081/local.qcow2", ProfilesImage: "docker.io/library/cwe-profiles:local"}
	data, err := SiteStartupUserData(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded startupCloudConfig
	if err := yaml.Unmarshal([]byte(data), &decoded); err != nil {
		t.Fatal(err)
	}
	script := decoded.WriteFiles[0].Content
	for _, forbidden := range []string{"nvkectl", "kind", "docker", "--disable-mtls"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("host control entered guest startup: %s", forbidden)
		}
	}
	if !strings.Contains(script, "nerdctl --namespace k8s.io load") {
		t.Fatal("guest must import local runtime images")
	}
}

func TestNativeDemoPackagePolicyPreservesCurrentSourceTemplate(t *testing.T) {
	const tail = "\nruncmd:\n  - apt-get install -y nvke-agent nvke-kubeadm-distro-{{ trimPrefix .KubernetesVersion \"v\" }}\n"
	for _, tc := range []struct {
		name, data string
		valid      bool
	}{
		{"source refresh enabled", "#cloud-config\npackage_update: true\npackage_upgrade: false\n" + tail, true},
		{"already disabled", "#cloud-config\npackage_update: false\npackage_upgrade: false\n" + tail, true},
		{"missing", "#cloud-config\npackage_upgrade: false\n", false},
		{"duplicate", "#cloud-config\npackage_update: true\npackage_update: false\npackage_upgrade: false\n", false},
		{"upgrades enabled", "#cloud-config\npackage_update: true\npackage_upgrade: true\n", false},
		{"other format", "#!/bin/sh\npackage_update: true\npackage_upgrade: false\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			init := map[string]any{"userDataTemplate": tc.data}
			err := nativeDemoPackagePolicy(map[string]any{"machineInit": init})
			if (err == nil) != tc.valid {
				t.Fatalf("policy error=%v; valid=%v", err, tc.valid)
			}
			want := tc.data
			if tc.valid {
				want = strings.Replace(want, "\npackage_update: true\n", "\npackage_update: false\n", 1)
			}
			if init["userDataTemplate"] != want {
				t.Fatal("startup policy changed unrelated source commands or a rejected template")
			}
		})
	}
}

func TestNativeReleaseAppliesDemoPolicyToControlPlaneAndWorker(t *testing.T) {
	for _, network := range []string{"", "ovn", "upstream-ovn"} {
		for _, preload := range []bool{false, true} {
			for _, sourceVersion := range []string{"1.4.0", "1.6.0"} {
				t.Run(fmt.Sprintf("%s/preload=%t/source=%s", network, preload, sourceVersion), func(t *testing.T) {
					testNativeReleasePolicies(t, network, preload, sourceVersion)
				})
			}
		}
	}
}

func testNativeReleasePolicies(t *testing.T, network string, preload bool, sourceVersion string) {
	t.Helper()
	installer, out := t.TempDir(), t.TempDir()
	root := filepath.Join(installer, "controller/test/fixtures/releases")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	template := "#cloud-config\npackage_update: true\npackage_upgrade: false\n" + strings.ReplaceAll(nativeWorkloadSourceRuncmd, "1.4.0", sourceVersion) + "\nphone_home:\n  post: all\n"
	sources := make(map[string][]byte)
	for _, kind := range []string{"machineimage", "clusterrelease", "nodepoolrelease"} {
		spec := map[string]any{"machineImageRef": map[string]any{"name": "original"}, "machineInit": map[string]any{"userDataTemplate": template}}
		if kind == "machineimage" {
			spec = map[string]any{"operatingSystem": map[string]any{"version": "24.04"}, "nico": map[string]any{"ipxeScript": "#!ipxe\nset image-url http://127.0.0.1/nvke-node.qcow2\n"}}
		}
		raw, err := yaml.Marshal(map[string]any{"metadata": map[string]any{"name": "original"}, "spec": spec})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "nvke-nico-vms-24.04-arm64-fde-"+kind+".yaml"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		sources[kind] = raw
	}
	attempt := nativeSiteAttempt{assets: NativeAssets{Installer: installer}, network: network}
	if preload {
		workload := filepath.Join(t.TempDir(), "workload.tar")
		if err := os.WriteFile(workload, []byte("local transport fixture\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		attempt.assets.Images = map[string]NativeImageAsset{"workload": {File: workload}}
	}
	if err := attempt.releases(t.Context(), "http://172.30.0.1:8080/local.qcow2", "http://172.30.0.1:18080", out); err != nil {
		t.Fatal(err)
	}
	request, err := newTenantRequest("native-demo-tenant", network)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"clusterrelease", "nodepoolrelease"} {
		raw, err := os.ReadFile(filepath.Join(out, kind+".yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		spec := doc["spec"].(map[string]any)
		got := spec["machineInit"].(map[string]any)["userDataTemplate"].(string)
		if spec["machineImageRef"].(map[string]any)["name"] != TenantProfile || !strings.Contains(got, "\npackage_update: false\n") {
			t.Fatalf("%s lost the selected image or package header", kind)
		}
		wantCommand := "DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 nvke-cni-ovn-images-" + request.CNIVersion + " nvke-cni-ovn-charts-" + request.CNIVersion
		var matches int
		for _, command := range nativeWorkloadRenderedCommands(t, got, true) {
			if text, ok := command.(string); ok && text == wantCommand {
				matches++
			}
		}
		if matches != 1 {
			t.Fatalf("%s did not render exactly one matched OVN package command", kind)
		}
		if preload {
			if !strings.Contains(got, "NVKE_AGENT_SKIP_START=1") || !strings.Contains(got, "inspecti --quiet") {
				t.Fatalf("%s did not apply the workload handoff before network projection", kind)
			}
		} else {
			want := strings.Replace(template, "package_update: true", "package_update: false", 1)
			want = strings.NewReplacer("nvke-cni-ovn-images-"+sourceVersion, "nvke-cni-ovn-images-"+request.CNIVersion, "nvke-cni-ovn-charts-"+sourceVersion, "nvke-cni-ovn-charts-"+request.CNIVersion).Replace(want)
			if got != want {
				t.Fatalf("%s changed unrelated source commands", kind)
			}
		}
	}
	for kind, before := range sources {
		after, err := os.ReadFile(filepath.Join(root, "nvke-nico-vms-24.04-arm64-fde-"+kind+".yaml"))
		if err != nil || !bytes.Equal(before, after) {
			t.Fatalf("%s source fixture was changed: %v", kind, err)
		}
	}
}
