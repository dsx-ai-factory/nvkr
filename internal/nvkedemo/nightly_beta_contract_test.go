// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestNightlyDiskAliasesShareHeldBytesAndRemainScoped(t *testing.T) {
	for _, network := range []string{"ovn", "upstream-ovn"} {
		t.Run(network, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, DiskOutputName+".qcow2")
			raw := []byte("exact authenticated candidate disk")
			if err := privateWrite(path, raw); err != nil {
				t.Fatal(err)
			}
			output, err := selectedDiskOutput(network)
			if err != nil {
				t.Fatal(err)
			}
			service, err := startNightlyDiskService(t.Context(), path, "127.0.0.1:0", NightlyProfile{Network: network, DiskOutput: output})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 10*time.Second)
				defer cancel()
				if err := service.Stop(cleanup); err != nil {
					t.Error(err)
				}
			})
			// Replacing the pathname cannot replace either route's held file.
			if err := os.Rename(path, filepath.Join(root, "original")); err != nil {
				t.Fatal(err)
			}
			if err := privateWrite(path, []byte("substituted disk")); err != nil {
				t.Fatal(err)
			}
			base := strings.TrimSuffix(service.url, "/"+DiskOutputName+".qcow2")
			image := lifecycle.Artifact{Digest: "sha256:" + plainSHA(raw), Disk: &delivery.DiskOutput{Size: int64(len(raw))}}
			if err := verifyHTTPDisk(t.Context(), service.url, image); err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Timeout: 5 * time.Second}
			aliasStatus := http.StatusNotFound
			if network == "upstream-ovn" {
				aliasStatus = http.StatusOK
			}
			for _, request := range []struct {
				method, path string
				status       int
			}{
				{http.MethodGet, "/nvke-node-image-upstream-ovn-arm64.qcow2", aliasStatus},
				{http.MethodHead, "/" + output + ".qcow2", http.StatusOK},
				{http.MethodGet, "/foreign.qcow2", http.StatusNotFound},
				{http.MethodGet, "/" + output + ".qcow2?extra=1", http.StatusNotFound},
				{http.MethodPost, "/" + output + ".qcow2", http.StatusNotFound},
			} {
				req, err := http.NewRequestWithContext(t.Context(), request.method, base+request.path, nil)
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				body, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr != nil || response.StatusCode != request.status || request.status == http.StatusOK && request.method == http.MethodGet && string(body) != string(raw) {
					t.Fatalf("unexpected selected-route response for %s %s: %d %q %v", request.method, request.path, response.StatusCode, body, readErr)
				}
			}
		})
	}
}

func TestNightlyDiskAliasesRejectUnselectedInputsBeforeWork(t *testing.T) {
	for _, test := range []struct{ network, output, filename string }{
		{"unknown", DiskOutputName, DiskOutputName + ".qcow2"},
		{"kind-calico", DiskOutputName, DiskOutputName + ".qcow2"},
		{"upstream-ovn", DiskOutputName, DiskOutputName + ".qcow2"},
		{"ovn", "nvke-node-image-upstream-ovn-arm64", DiskOutputName + ".qcow2"},
		{"upstream-ovn", "nvke-node-image-upstream-ovn-arm64", "foreign.qcow2"},
	} {
		t.Run(test.network+"/"+test.output+"/"+test.filename, func(t *testing.T) {
			service, err := startNightlyDiskService(t.Context(), filepath.Join(t.TempDir(), test.filename), "invalid-listener", NightlyProfile{Network: test.network, DiskOutput: test.output})
			assertNightlyCode(t, err, e.CodeEngineCapability)
			if service != nil {
				t.Fatal("unselected disk route started a service")
			}
		})
	}
	for _, alias := range []string{"foreign.qcow2", "../nvke-node-image-upstream-ovn-arm64.qcow2", "/nvke-node-image-upstream-ovn-arm64.qcow2"} {
		service, err := startDiskServiceWithNightlyAlias(t.Context(), filepath.Join(t.TempDir(), DiskOutputName+".qcow2"), "invalid-listener", alias)
		assertNightlyCode(t, err, e.CodeEngineCapability)
		if service != nil {
			t.Fatal("arbitrary alias started a service")
		}
	}
}

func TestNightlyBetaProfileResourcesBindAuthenticatedInputs(t *testing.T) {
	for _, scenario := range []string{"ovn", "upstream-ovn", "duplicate disk", "wrong disk", "unknown profile", "wrong URL", "missing template", "existing output", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			profile := NightlyProfile{Network: "ovn", DiskOutput: DiskOutputName, OSProfile: TenantProfile}
			metadata, ipxe, userData := tenantReleaseFixtureBytes()
			if scenario == "upstream-ovn" {
				profile.Network, profile.DiskOutput = "upstream-ovn", "nvke-node-image-upstream-ovn-arm64"
				ipxe = []byte(strings.ReplaceAll(string(ipxe), DiskOutputName, profile.DiskOutput))
			}
			digest := "sha256:" + strings.Repeat("1", 64)
			image := lifecycle.Artifact{Name: profile.DiskOutput, Kind: "disk-image", OS: "linux", Arch: "arm64", Digest: digest, Disk: &delivery.DiskOutput{PayloadDigest: digest, Size: 123, Format: "qcow2", Encryption: "none"}}
			release := lifecycle.Release{Artifacts: []lifecycle.Artifact{image}}
			out := filepath.Join(root, "resources")
			ctx := t.Context()
			wantCode := e.CodeVerifyFailed
			switch scenario {
			case "duplicate disk":
				release.Artifacts = append(release.Artifacts, image)
			case "wrong disk":
				release.Artifacts[0].Name = "foreign"
			case "unknown profile":
				profile.OSProfile, wantCode = "foreign", e.CodeEngineCapability
			case "wrong URL":
				ipxe = []byte(strings.ReplaceAll(string(ipxe), GuestGateway, "example.invalid"))
			case "existing output":
				if err := os.Mkdir(out, 0o700); err != nil {
					t.Fatal(err)
				}
				wantCode = e.CodeImmutableViolation
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				wantCode = e.CodeTimeout
			}
			base := filepath.Join(root, "profiles/osProfiles")
			if err := os.MkdirAll(filepath.Join(base, TenantProfile), 0o700); err != nil {
				t.Fatal(err)
			}
			for path, data := range map[string][]byte{"metadata.yaml": metadata, TenantProfile + "/ipxeScript": ipxe, TenantProfile + "/userData.tmpl": userData} {
				if scenario == "missing template" && strings.HasSuffix(path, "userData.tmpl") {
					continue
				}
				if err := privateWrite(filepath.Join(base, path), data); err != nil {
					t.Fatal(err)
				}
			}
			err := nightlyProfileResources(ctx, profile, release, root, out)
			if scenario != "ovn" && scenario != "upstream-ovn" {
				assertNightlyCode(t, err, wantCode)
				entries, _ := os.ReadDir(out)
				if len(entries) != 0 {
					t.Fatal("refused inputs wrote resource documents")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"MachineImage", "ClusterRelease", "NodePoolRelease"} {
				raw, err := os.ReadFile(filepath.Join(out, TenantProfile+"-"+strings.ToLower(kind)+".yaml"))
				if err != nil {
					t.Fatal(err)
				}
				var document nightlyReleaseResource
				if err := json.Unmarshal(raw, &document); err != nil {
					t.Fatal(err)
				}
				if document.APIVersion != "internal.nvke.nvidia.com/v1" || document.Kind != kind || document.Metadata.Name != TenantProfile || document.Metadata.Annotations["nvkr.dev/diagnostic-disk-digest"] != digest {
					t.Fatalf("resource lost current API or exact disk binding: %s", raw)
				}
				spec := document.Spec.(map[string]any)
				if kind == "MachineImage" {
					if spec["nico"].(map[string]any)["ipxeScript"] != string(ipxe) {
						t.Fatal("resource changed authenticated boot bytes")
					}
				} else if spec["machineImageRef"].(map[string]any)["name"] != TenantProfile || spec["machineInit"].(map[string]any)["userDataTemplate"] != string(userData) || document.Metadata.Annotations["internal.nvke.nvidia.com/machine-init-kubernetes-version-injected"] != "true" {
					t.Fatal("release changed current version metadata, image binding or authenticated template")
				}
			}
		})
	}
}

func TestNightlyBetaSiteSpecRejectsUnboundInputs(t *testing.T) {
	for _, scenario := range []string{"exact", "bad cluster", "bad gateway", "mutable controller", "mutable profiles", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			work, source := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(source, "nktp/hack"), 0o700); err != nil {
				t.Fatal(err)
			}
			telemetry := []byte("metrics-server:\n  args: [\"--kubelet-insecure-tls\"]\n")
			if err := privateWrite(filepath.Join(source, "nktp/hack/e2e-values.yaml"), telemetry); err != nil {
				t.Fatal(err)
			}
			cluster, gateway := "nvkr-nightly-fixture", "172.18.0.1"
			controller, profiles := "example.invalid/controller@sha256:"+strings.Repeat("a", 64), "example.invalid/profiles@sha256:"+strings.Repeat("b", 64)
			ctx := t.Context()
			wantCode := e.CodeVerifyFailed
			switch scenario {
			case "bad cluster":
				cluster = "wrong/name"
			case "bad gateway":
				gateway = "localhost"
			case "mutable controller":
				controller = "example.invalid/controller:latest"
			case "mutable profiles":
				profiles = "example.invalid/profiles:latest"
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				wantCode = e.CodeTimeout
			}
			path, err := nightlySiteSpec(ctx, Runner{Work: work, Env: []string{"KIND_CLUSTER_NAME=" + cluster}}, source, gateway, controller, profiles)
			if scenario != "exact" {
				assertNightlyCode(t, err, wantCode)
				entries, _ := os.ReadDir(work)
				if path != "" || len(entries) != 0 {
					t.Fatal("refused site inputs wrote private output")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var spec map[string]any
			if err := json.Unmarshal(raw, &spec); err != nil {
				t.Fatal(err)
			}
			stackRaw, err := os.ReadFile(filepath.Join(work, "stack-config.json"))
			if err != nil {
				t.Fatal(err)
			}
			var stack map[string]any
			if err := json.Unmarshal(stackRaw, &stack); err != nil {
				t.Fatal(err)
			}
			for _, obsolete := range []string{"controllerStack", "registry", "caCertFile", "caKeyFile"} {
				if strings.Contains(string(raw), obsolete) {
					t.Fatalf("obsolete site field %s", obsolete)
				}
			}
			bound, err := nightlyStackEnvironment(Runner{Work: work}, path)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"NVKECTL_NICO_TOKEN", "NVKECTL_NODE_USER", "NVKECTL_NODE_PASSWORD", "NVKECTL_DATABASE_PASSWORD", "NVKECTL_SPIRE_CA_CERT_FILE", "NVKECTL_SPIRE_CA_KEY_FILE", "NKE_E2E_CANDIDATE_STACK_CONFIG", "NKE_E2E_CANDIDATE_NKTP_VALUES"} {
				if environmentValue(bound.Env, key) == "" {
					t.Fatalf("missing handoff %s", key)
				}
			}
			images := stack["images"].(map[string]any)
			if spec["clusterName"] != cluster || images["controller"] != controller || images["profiles"] != profiles || spec["spire"].(map[string]any)["trustDomain"] != cluster+".nke.local" || stack["nico"].(map[string]any)["endpoint"] != "http://"+gateway+":18089" {
				t.Fatal("site lost suite identity or selected image references")
			}
			if strings.Contains(string(raw), "nico-vmsctl-token") {
				t.Fatal("site embeds credentials instead of private credential files")
			}
			copied, err := os.ReadFile(filepath.Join(work, "nightly-nktp-values.yaml"))
			if err != nil || string(copied) != string(telemetry) {
				t.Fatal("site changed source-owned telemetry bytes", err)
			}
		})
	}
}

// This exercises beta's actual sitespec validator and stack renderer. It does
// not boot Kind or VMs and establishes no published artifact/runtime evidence.
func TestNightlyBetaSiteSourceContract(t *testing.T) {
	source := os.Getenv("NVKR_NVKE_SOURCE_CONTRACT")
	if source == "" {
		t.Skip("set NVKR_NVKE_SOURCE_CONTRACT and NVKR_NVKE_SOURCE_CLI for the beta source contract")
	}
	cli, err := resolveExisting(os.Getenv("NVKR_NVKE_SOURCE_CLI"))
	if err != nil {
		t.Fatal(err)
	}
	work := privateTestDir(t)
	runner := Runner{Work: work, Env: []string{"PATH=" + os.Getenv("PATH"), "HOME=" + work, "KIND_CLUSTER_NAME=nvkr-nightly-contract", "NVKECTL_RENDER_ALLOW_TEMP_OUT=1"}}
	if suite := os.Getenv("NVKR_NVKE_NIGHTLY_SOURCE_CONTRACT"); suite != "" {
		profile, err := loadNightlyProfile(t.Context(), "../../inputs/testing/nightly-profiles.json", "nvke-nightly-arm64")
		if err != nil {
			t.Fatal(err)
		}
		source = suite
		if err := nightlySource(t.Context(), runner, source, profile.HarnessCommit); err != nil {
			t.Fatal(err)
		}
	} else {
		selected, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join("..", "..", "inputs/catalogs/tools/native")))
		if err != nil {
			t.Fatal(err)
		}
		if err := nightlySource(t.Context(), runner, source, selected.Components["nvke-installer-sources"].Commit); err != nil {
			t.Fatal(err)
		}
	}
	if err := bootstrapCA(t.Context(), ManagementSession{Work: work, Runner: runner}); err != nil {
		t.Fatal(err)
	}
	controller, profiles := "example.invalid/controller@sha256:"+strings.Repeat("a", 64), "example.invalid/profiles@sha256:"+strings.Repeat("b", 64)
	spec, err := nightlySiteSpec(t.Context(), runner, source, "172.18.0.1", controller, profiles)
	if err != nil {
		t.Fatal(err)
	}
	runner, err = nightlyStackEnvironment(runner, spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(t.Context(), []string{cli, "validate", "sitespec", "-f", spec}, "nightly-sitespec-contract", time.Minute); err != nil {
		t.Fatal(err)
	}
	rendered := filepath.Join(work, "rendered")
	command := []string{cli, "render", "deploy-path", "--deploy-path", filepath.Join(source, "deploy/nico"), "--sitespec", spec, "--stack-config", filepath.Join(work, "stack-config.json"), "--arch", "linux/arm64", "--out", rendered}
	if os.Getenv("NVKR_NVKE_NIGHTLY_SOURCE_CONTRACT") != "" {
		runner.Env = SetEnv(runner.Env, map[string]string{"NVKECTL_BIN": cli, "WORKSPACE_ROOT": source, "STATE_DIR": work, "NKE_E2E_CANDIDATE_RELEASE": "", "NKE_E2E_CANDIDATE_LAYOUT": ""})
		command = []string{"bash", "-eu", "-c", `source "$1"; candidate_render_stack`, "contract", filepath.Join(source, "controller/hack/ci/nico-vms-candidate.sh")}
	}
	if err := runner.Run(t.Context(), command, "nightly-render-contract", time.Minute); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(rendered, "controller/site-values.yaml"))
	if err != nil || !strings.Contains(string(raw), "repository: example.invalid/controller") || !strings.Contains(string(raw), "digest: sha256:"+strings.Repeat("a", 64)) || !strings.Contains(string(raw), "repository: example.invalid/profiles") || !strings.Contains(string(raw), "digest: sha256:"+strings.Repeat("b", 64)) {
		t.Fatal("native renderer lost candidate image references", err)
	}
}
