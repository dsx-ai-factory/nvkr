// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

// Source-distributed empty operator-file fixture. Production uses systemd's
// parser; the fixture stub models only that owner's acceptance result.
const nativeKubeletEmptyOperator = "# Optional kubelet flags; kubeadm uses /var/lib/kubelet/kubeadm-flags.env.\nKUBELET_EXTRA_ARGS=\n"

// Source fixture; admission checks its documented interface, not comments.
const nativeKubeletOperatorUnit = `# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
# Note: This dropin only works with kubeadm and kubelet v1.11+
# Source: Kubernetes kubeadm deb (pkgs.k8s.io); paths use /usr/bin/kubelet.
[Service]
Environment="KUBELET_KUBECONFIG_ARGS=--bootstrap-kubeconfig=/etc/kubernetes/bootstrap-kubelet.conf --kubeconfig=/etc/kubernetes/kubelet.conf"
Environment="KUBELET_CONFIG_ARGS=--config=/var/lib/kubelet/config.yaml"
# This is a file that "kubeadm init" and "kubeadm join" generates at runtime, populating the KUBELET_KUBEADM_ARGS variable dynamically
EnvironmentFile=-/var/lib/kubelet/kubeadm-flags.env
# This is a file that the user can use for overrides of the kubelet args as a last resort. Preferably, the user should use
# the .NodeRegistration.KubeletExtraArgs object in the configuration files instead. KUBELET_EXTRA_ARGS should be sourced from this file.
EnvironmentFile=-/etc/default/kubelet
ExecStart=
ExecStart=/usr/bin/kubelet $KUBELET_KUBECONFIG_ARGS $KUBELET_CONFIG_ARGS $KUBELET_KUBEADM_ARGS $KUBELET_EXTRA_ARGS
`

func TestNativeSmallProfileAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, profile, version string
		code                   e.Code
	}{
		{"default NVIDIA", "", siteOVNVersion, ""},
		{"default upstream", "", upstreamSiteOVNVersion, ""},
		{"small NVIDIA", "small", siteOVNVersion, ""},
		{"unknown", "tiny", siteOVNVersion, e.CodeUsage},
		{"small upstream", "small", upstreamSiteOVNVersion, e.CodeEngineCapability},
		{"small unknown chart", "small", "2.4.3", e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := admitNativeResourceProfile(tc.profile, tc.version)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertNightlyCode(t, err, tc.code)
			cfg := startupDescription()
			cfg.resourceProfile = tc.profile
			cfg.OVNVersion = tc.version
			if raw, err := SiteStartupUserData(startupTestContext(t), cfg); raw != "" || err == nil {
				t.Fatal("unsupported startup returned partial output")
			}
		})
	}
	if _, err := selectedNativeResources(NativeResources{Profile: "tiny"}); err == nil {
		t.Fatal("unknown resource profile accepted")
	} else {
		assertNightlyCode(t, err, e.CodeUsage)
	}
}

func TestNativeSmallProfileNetworkAdmissionPrecedesHostAndArtifacts(t *testing.T) {
	for _, tc := range []struct {
		name, profile, network, message string
		partial                         bool
		code                            e.Code
	}{
		{"unknown", "tiny", "ovn", "unknown native resource profile", false, e.CodeUsage},
		{"upstream", "small", "upstream-ovn", "small native resource profile requires NVIDIA OVN", false, e.CodeEngineCapability},
		{"calico", "small", "kind-calico", "native site requires the selected OVN", false, e.CodeEngineCapability},
		{"unknown network", "small", "foreign", "network must be", false, e.CodeUsage},
		{"partial sizing", "small", "ovn", "positive bounded", true, e.CodeUsage},
	} {
		for _, boundary := range []string{"prebuild admission", "artifact selection", "direct native"} {
			t.Run(tc.name+"/"+boundary, func(t *testing.T) {
				root := privateTestDir(t)
				resources := NativeResources{SiteMemoryGiB: 4, TenantMemoryGiB: 4, HeadroomMiB: 1024, VCPUs: 4, Profile: tc.profile}
				if tc.partial {
					resources.SiteMemoryGiB = 0
				}
				cfg := Config{Resources: resources, Network: tc.network, Cleanup: true, Out: filepath.Join(root, "absent-parent", "output"), Runs: RunInputs{OS: filepath.Join(root, "missing-artifact-run")}}
				var err error
				switch boundary {
				case "prebuild admission":
					err = AdmitDemoDeployment(t.Context(), cfg)
				case "artifact selection":
					var result NativeConfig
					result, err = nativeDeploymentConfig(t.Context(), cfg, "unsupported/host")
					if !reflect.DeepEqual(result, NativeConfig{}) {
						t.Fatal("refused profile returned deployment inputs")
					}
				case "direct native":
					err = runNativeSite(t.Context(), NativeConfig{Resources: resources, Network: tc.network, Out: cfg.Out, Accelerator: "unsupported"}, func(context.Context, *NativeSite) error {
						t.Fatal("refused profile reached runtime callback")
						return nil
					})
				}
				if boundary == "direct native" {
					assertNightlyCode(t, err, e.CodePreflightFailed)
					var wrapped *e.Error
					if !errors.As(err, &wrapped) {
						t.Fatal(err)
					}
					err = wrapped.Unwrap()
				}
				assertNightlyCode(t, err, tc.code)
				if !strings.Contains(err.Error(), tc.message) {
					t.Fatalf("later host/path/artifact gate replaced profile refusal: %v", err)
				}
				entries, err := os.ReadDir(root)
				if err != nil || len(entries) != 0 {
					t.Fatal("refused profile performed output/artifact work", entries, err)
				}
			})
		}
	}
	for _, tc := range []struct {
		name, network string
		resources     NativeResources
	}{
		{"empty keeps legacy ordering", "foreign", NativeResources{SiteMemoryGiB: -1}},
		{"small defaults", "", NativeResources{Profile: "small"}},
		{"small explicit NVIDIA", "ovn", NativeResources{SiteMemoryGiB: 4, TenantMemoryGiB: 4, HeadroomMiB: 1024, VCPUs: 4, Profile: "small"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := admitNativeResourcesForNetwork(tc.resources, tc.network); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNativeSmallProfileKubeletPreparationRejectsUnknownBeforeOutput(t *testing.T) {
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	for _, profile := range []string{"tiny", "SMALL", " small"} {
		t.Run(profile, func(t *testing.T) {
			script, err := nativeKubeletProfilePreparation(ended, profile)
			assertNightlyCode(t, err, e.CodeUsage)
			if script != "" {
				t.Fatal("unknown profile returned executable preparation")
			}
		})
	}
	if script, err := nativeKubeletProfilePreparation(ended, ""); err != nil || script != "" {
		t.Fatal("empty profile changed default context/admission behavior", err)
	}
	if script, err := nativeKubeletProfilePreparation(startupTestContext(t), "small"); err != nil || script == "" {
		t.Fatal("admitted small profile omitted preparation", err)
	}
}

func TestNativeSmallProfileOperatorFileUsesOwnerAndAtomicCommit(t *testing.T) {
	for _, tc := range []struct {
		name, contents                          string
		badUnit, symlink, pass, rejectCandidate bool
	}{
		{"empty", "", false, false, true, false},
		{"source default", nativeKubeletEmptyOperator, false, false, true, false},
		{"changed source comments", nativeKubeletEmptyOperator, false, false, true, false},
		{"quoted empty with comments", "# Operator comment\nKUBELET_EXTRA_ARGS=\"\"\n", false, false, true, false},
		{"nonempty args", "KUBELET_EXTRA_ARGS=--fail-swap-on=false\n", false, false, false, false},
		{"oversized operator file", strings.Repeat("#", 65537), false, false, false, false},
		{"other operator values preserved", "OTHER=value\nKUBELET_EXTRA_ARGS=''\n", false, false, true, false},
		{"unterminated other quote rejected by owner", "KUBELET_EXTRA_ARGS=\nOTHER=\"", false, false, false, true},
		{"changed unit", nativeKubeletEmptyOperator, true, false, false, false},
		{"symlink", nativeKubeletEmptyOperator, false, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			operator, unit := filepath.Join(root, "kubelet"), filepath.Join(root, "10-kubeadm.conf")
			writeTenantFixture(t, operator, []byte(tc.contents), 0600)
			unitBytes := nativeKubeletOperatorUnit
			if tc.name == "changed source comments" {
				unitBytes = "# harmless source comment\n" + unitBytes
			}
			if tc.badUnit {
				unitBytes = strings.ReplaceAll(unitBytes, "$KUBELET_EXTRA_ARGS", "")
			}
			writeTenantFixture(t, unit, []byte(unitBytes), 0600)
			// Only the owner command/result is stubbed: this test does not
			// reimplement systemd's EnvironmentFile grammar or run a service.
			writeTenantFixture(t, filepath.Join(root, "systemd-run"), []byte("#!/bin/sh\nset -eu\nprintf '%s\\n' \"$@\" >> \"$OWNER_ARGS\"\nfor arg in \"$@\"; do case \"$arg\" in --property=EnvironmentFile=*nvkr-small*) test \"$OWNER_CANDIDATE\" = true || exit 1;; esac; done\ntest \"$OWNER_EMPTY\" = true\n"), 0700)
			if tc.symlink {
				if err := os.Rename(operator, operator+".held"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(operator+".held", operator); err != nil {
					t.Fatal(err)
				}
			}
			script := nativeSmallKubeletScript(operator, unit)
			if raw, err := exec.CommandContext(t.Context(), "bash", "-n", "-c", script).CombinedOutput(); err != nil {
				t.Fatalf("shell syntax: %s %v", raw, err)
			}
			if shellcheck, err := exec.LookPath("shellcheck"); err == nil {
				// Dollar signs in literal unit interfaces and child-shell tests
				// deliberately expand only in their owning interpreter.
				lint := exec.CommandContext(t.Context(), shellcheck, "-S", "warning", "-s", "bash", "-e", "SC2016", "-")
				lint.Stdin = strings.NewReader("#!/bin/bash\nset -euo pipefail\n" + script)
				if raw, err := lint.CombinedOutput(); err != nil {
					t.Fatalf("operator shell lint: %s %v", raw, err)
				}
			}
			command := exec.CommandContext(t.Context(), "bash", "-euc", script)
			ownerEmpty := "false"
			if tc.pass || tc.rejectCandidate {
				ownerEmpty = "true"
			}
			candidateAccepted := "true"
			if tc.rejectCandidate {
				candidateAccepted = "false"
			}
			command.Env = append(os.Environ(), "PATH="+root+":"+os.Getenv("PATH"), "OWNER_EMPTY="+ownerEmpty, "OWNER_CANDIDATE="+candidateAccepted, "OWNER_ARGS="+filepath.Join(root, "owner-args"))
			output, err := command.CombinedOutput()
			if (err == nil) != tc.pass {
				t.Fatalf("operator admission: %s %v", output, err)
			}
			if candidates, err := filepath.Glob(operator + ".nvkr-small.*"); err != nil || len(candidates) != 0 {
				t.Fatal("candidate operator file was not cleaned", candidates, err)
			}
			actual, err := os.ReadFile(operator)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.pass {
				if string(actual) != tc.contents {
					t.Fatal("refused operator file changed")
				}
				return
			}
			if !strings.HasPrefix(string(actual), tc.contents) {
				t.Fatal("existing operator assignments were changed")
			}
			// systemd owns EnvironmentFile parsing. Bash below inspects only
			// the fixed shell-compatible assignment emitted by this test.
			ownerArgs, err := os.ReadFile(filepath.Join(root, "owner-args"))
			if err != nil {
				t.Fatal(err)
			}
			for _, required := range []string{"--wait", "--pipe", "--collect", "--expand-environment=no", "--property=Type=oneshot", "--property=TimeoutStartSec=10s", "--property=TimeoutStopSec=5s", "--property=EnvironmentFile=" + operator, `test -z "${KUBELET_EXTRA_ARGS-}"`, `test "$KUBELET_EXTRA_ARGS" = '--kube-reserved=cpu=1000m,memory=256Mi,ephemeral-storage=1Gi'`} {
				if !strings.Contains(string(ownerArgs), required) {
					t.Fatalf("owner admission missing %s", required)
				}
			}
			command = exec.CommandContext(t.Context(), "bash", "-euc", `source "$1"; printf '%s' "$KUBELET_EXTRA_ARGS"`, "contract", operator)
			raw, err := command.CombinedOutput()
			if err != nil || string(raw) != "--kube-reserved=cpu=1000m,memory=256Mi,ephemeral-storage=1Gi" {
				t.Fatalf("effective full-map flags: %s %v", raw, err)
			}
		})
	}
	if script, err := nativeKubeletProfilePreparation(t.Context(), ""); script != "" || err != nil {
		t.Fatal("default profile writes operator configuration")
	}
}

func TestNativeSmallProfileTelemetryPreservesDefaultAndAllRoles(t *testing.T) {
	baseline, err := demoTelemetryValues()
	if err != nil {
		t.Fatal(err)
	}
	empty, err := demoTelemetryValuesForProfile("")
	if err != nil || !bytes.Equal(empty, baseline) {
		t.Fatal("empty profile changed telemetry bytes")
	}
	small, err := demoTelemetryValuesForProfile("small")
	if err != nil {
		t.Fatal(err)
	}
	var old, current map[string]any
	if err := yaml.Unmarshal(baseline, &old); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(small, &current); err != nil {
		t.Fatal(err)
	}
	if len(current) != 8 {
		t.Fatal("telemetry role membership changed")
	}
	for _, role := range []string{"watchdog", "gateway_logs", "gateway_metrics", "gateway_traces", "large_target_metrics", "cluster_events", "daemonset"} {
		settings := current[role].(map[string]any)["kubernetes"].(map[string]any)
		requests := settings["resources"].(map[string]any)["requests"].(map[string]any)
		if requests["cpu"] != "25m" || requests["memory"] != "64Mi" {
			t.Fatalf("collector %s requests changed", role)
		}
		delete(requests, "memory")
		if allocator, ok := settings["targetAllocator"].(map[string]any); ok {
			r := allocator["resources"].(map[string]any)["requests"].(map[string]any)
			if r["cpu"] != "25m" || r["memory"] != "64Mi" {
				t.Fatal("allocator budget changed")
			}
			delete(r, "memory")
		}
	}
	if !reflect.DeepEqual(old, current) {
		t.Fatal("small profile changed non-memory telemetry settings")
	}
}

func TestNativeSmallProfileOVNOnlyChangesTwoMemoryRequests(t *testing.T) {
	ref := "localhost/nvke/ovn-kubernetes@sha256:" + strings.Repeat("a", 64)
	baseline, err := nativeOVNValues(siteOVNVersion, ref)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := nativeOVNValuesForProfile(siteOVNVersion, ref, "")
	if err != nil || empty != baseline {
		t.Fatal("default OVN bytes changed")
	}
	small, err := nativeOVNValuesForProfile(siteOVNVersion, ref, "small")
	if err != nil {
		t.Fatal(err)
	}
	var old, current map[string]any
	if err := json.Unmarshal([]byte(baseline), &old); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(small), &current); err != nil {
		t.Fatal(err)
	}
	for _, binding := range []struct{ component, workload string }{{"ovnkube-control-plane", "ovnkubeControlPlane"}, {"ovnkube-single-node-zone", "ovnkubeSingleNodeZone"}} {
		settings := current[binding.component].(map[string]any)[binding.workload].(map[string]any)
		requests := settings["resources"].(map[string]any)["requests"].(map[string]any)
		if len(requests) != 1 || requests["memory"] != "128Mi" {
			t.Fatal("unexpected OVN resource mutation")
		}
		delete(settings, "resources")
	}
	if !reflect.DeepEqual(old, current) {
		t.Fatal("small profile changed OVN images, features or other workloads")
	}
}

func TestNativeSmallProfileConfigurationReachesBothGuestOwners(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "demo.yaml")
	writeTenantFixture(t, path, []byte("version: 1\nrun:\n  out: runtime\n  resources:\n    siteMemoryGiB: 4\n    tenantMemoryGiB: 4\n    headroomMiB: 1024\n    vcpus: 4\n    profile: small\n"), 0600)
	file, err := LoadDemoFile(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	configured, err := file.runDescription(nil)
	if err != nil {
		t.Fatal(err)
	}
	attempt := nativeCAPITenantFixture(t, GuestCIDR, "ovn")
	attempt.resources = configured.Resources
	request, err := attempt.namedTenantRequest(startupTestContext(t), "small-tenant")
	if err != nil {
		t.Fatal(err)
	}
	if request.ControlPlane.Reserved != "256Mi" || request.NodePools[0].SystemReserved != "256Mi" || request.NodePools[0].KubeReserved != "256Mi" {
		t.Fatal("supported tenant API memory fields not projected")
	}
	for _, infra := range []*TenantMachineInfra{request.ControlPlane.Infra, request.NodePools[0].Infra} {
		if !strings.Contains(infra.PreCommands[0], "--kube-reserved=cpu=1000m,memory=256Mi,ephemeral-storage=1Gi") {
			t.Fatal("guest omitted full kubelet reservation override")
		}
	}
	cfg := startupDescription()
	cfg.resourceProfile = configured.Resources.Profile
	raw, err := SiteStartupUserData(startupTestContext(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var startup startupCloudConfig
	if err := yaml.Unmarshal([]byte(strings.TrimPrefix(raw, "#cloud-config\n")), &startup); err != nil {
		t.Fatal(err)
	}
	script := startup.WriteFiles[0].Content
	apt, operator, agent := strings.Index(script, "apt-get install"), strings.Index(script, "--kube-reserved=cpu=1000m,memory=256Mi,ephemeral-storage=1Gi"), strings.Index(script, "systemctl start --no-block")
	if apt < 0 || operator < apt || agent < operator {
		t.Fatal("site override is not after package install and before agent/kubelet bootstrap")
	}
	if output, err := exec.CommandContext(t.Context(), "bash", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("site shell: %s %v", output, err)
	}
}

// This opt-in contract renders the actual selected source charts. It exercises
// their merge/templates, not a mirrored model, and boots no guest or service.
func TestNativeSmallProfileSourceHelmContract(t *testing.T) {
	nktp, base, ovn := os.Getenv("NVKR_NATIVE_PROFILE_NKTP_CHART"), os.Getenv("NVKR_NATIVE_PROFILE_NKTP_VALUES"), os.Getenv("NVKR_NATIVE_PROFILE_OVN_CHART")
	if nktp == "" && base == "" && ovn == "" {
		t.Skip("set the exact source NKTP chart/base-values and OVN chart for the render contract")
	}
	if nktp == "" || base == "" || ovn == "" {
		t.Fatal("source render requires all three explicit chart inputs")
	}
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Fatal(err)
	}
	decode := func(t *testing.T, raw []byte) map[string]map[string]any {
		objects := map[string]map[string]any{}
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		for {
			var object map[string]any
			err := decoder.Decode(&object)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(object) == 0 {
				continue
			}
			meta := object["metadata"].(map[string]any)
			namespace, _ := meta["namespace"].(string)
			key := object["kind"].(string) + "/" + namespace + "/" + meta["name"].(string)
			if objects[key] != nil {
				t.Fatalf("duplicate rendered identity %s", key)
			}
			objects[key] = object
		}
		return objects
	}
	render := func(t *testing.T, chart, namespace, profile string, values []byte, extra ...string) map[string]map[string]any {
		dir := privateTestDir(t)
		path := filepath.Join(dir, "values.yaml")
		writeTenantFixture(t, path, values, 0600)
		args := []string{helm, "template", "nktp", chart, "--namespace", namespace, "--kube-version", "1.36.3"}
		args = append(args, extra...)
		args = append(args, "-f", path)
		if namespace == "ovn-kubernetes" {
			args = append(args, "--set", "global.skipCallToK8s=true", "--set", "k8sAPIServer=https://192.0.2.1:6443", "--set", "podNetwork=10.244.0.0/16", "--set", "serviceNetwork=10.96.0.0/12")
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
		defer cancel()
		raw, err := (Runner{Work: dir, Env: NativeEnvironment()}).Capture(ctx, args, "source-"+profile, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return decode(t, raw)
	}
	request := func(t *testing.T, container map[string]any, want string) {
		resources := container["resources"].(map[string]any)
		requests := resources["requests"].(map[string]any)
		if requests["memory"] == nil || want != "" && requests["memory"] != want {
			t.Fatalf("source request %v differs from %s", requests["memory"], want)
		}
		delete(requests, "memory")
	}
	for _, kind := range []string{"nktp", "ovn"} {
		t.Run(kind, func(t *testing.T) {
			chart, namespace := nktp, "nktp"
			var previous, current []byte
			var extra []string
			if kind == "nktp" {
				previous, err = demoTelemetryValues()
				if err != nil {
					t.Fatal(err)
				}
				current, err = demoTelemetryValuesForProfile("small")
				extra = []string{"-f", base}
			} else {
				chart, namespace = ovn, "ovn-kubernetes"
				ref := "localhost/nvke/ovn-kubernetes@sha256:" + strings.Repeat("a", 64)
				old, oldErr := nativeOVNValues(siteOVNVersion, ref)
				if oldErr != nil {
					t.Fatal(oldErr)
				}
				small, smallErr := nativeOVNValuesForProfile(siteOVNVersion, ref, "small")
				if smallErr != nil {
					t.Fatal(smallErr)
				}
				previous, current = []byte(old), []byte(small)
			}
			if err != nil {
				t.Fatal(err)
			}
			oldObjects, newObjects := render(t, chart, namespace, "default", previous, extra...), render(t, chart, namespace, "small", current, extra...)
			mains, allocators, heavy, ksm := 0, 0, 0, 0
			for key, obj := range newObjects {
				old := oldObjects[key]
				if old == nil {
					t.Fatalf("small profile added %s", key)
				}
				meta := obj["metadata"].(map[string]any)
				name := meta["name"].(string)
				if strings.HasPrefix(name, "nktp-ksm-") && (obj["kind"] == "Deployment" || obj["kind"] == "DaemonSet") {
					ksm++
				}
				if kind == "nktp" && obj["kind"] == "OpenTelemetryCollector" {
					spec := obj["spec"].(map[string]any)
					oldSpec := old["spec"].(map[string]any)
					request(t, spec, "64Mi")
					request(t, oldSpec, "")
					mains++
					if allocator, ok := spec["targetAllocator"].(map[string]any); ok {
						request(t, allocator, "64Mi")
						request(t, oldSpec["targetAllocator"].(map[string]any), "")
						allocators++
					}
				}
				if kind == "ovn" && (obj["kind"] == "Deployment" || obj["kind"] == "DaemonSet") {
					containers := obj["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
					oldContainers := old["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
					for i, value := range containers {
						container := value.(map[string]any)
						if container["name"] == "ovnkube-controller" || container["name"] == "ovnkube-cluster-manager" {
							request(t, container, "128Mi")
							request(t, oldContainers[i].(map[string]any), "")
							heavy++
						}
					}
				}
			}
			if !reflect.DeepEqual(oldObjects, newObjects) {
				t.Fatal("rendered objects, limits, CPU, replicas, images, env or features changed outside admitted memory requests")
			}
			if kind == "nktp" && (mains != 7 || allocators != 3 || ksm != 3) {
				t.Fatalf("source role coverage collectors=%d allocators=%d KSM=%d", mains, allocators, ksm)
			}
			if kind == "ovn" && heavy != 2 {
				t.Fatalf("source OVN role coverage=%d", heavy)
			}
			t.Logf("%s: %d object identities unchanged; collectors=%d allocators=%d KSM=%d OVN-heavy=%d", kind, len(oldObjects), mains, allocators, ksm, heavy)
		})
	}
}
