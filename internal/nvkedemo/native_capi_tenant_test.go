// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func nativeCAPITenantFixture(t *testing.T, subnet, network string) nativeSiteAttempt {
	t.Helper()
	root := privateTestDir(t)
	selection := nativeInstallerFixture(t, root, "dfc817aa34205e8099dad91dfd05170499d00b60", "v1.13.6")
	prefix, _ := netip.ParsePrefix(subnet)
	gateway := prefix.Addr().Next()
	payload := filepath.Join(root, "transport.tar")
	writeTenantFixture(t, payload, []byte("selected image transport"), 0600)
	digest := "sha256:" + strings.Repeat("b", 64)
	boot, err := projectNativeSiteBoot(t.Context(), nativeMachineImageBootTemplate, "http://172.30.0.1:18082/local.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	return nativeSiteAttempt{network: network, boot: boot, api: "http://" + gateway.String() + ":18085",
		bridge: nativeBridgeConfig{prefix: prefix, gateway: gateway},
		assets: NativeAssets{Installer: root, installerSelection: selection, Images: map[string]NativeImageAsset{
			"ovn":      {File: payload, ManifestDigest: digest, Reference: "localhost/nvke/ovn-kubernetes@" + digest},
			"workload": {File: payload, Reference: "example.invalid/selected-workload@" + digest, ManifestDigest: digest},
		}},
	}
}

func TestNativeCAPITenantPreservesSourceCredentialFilesAndLocalImages(t *testing.T) {
	for _, subnet := range []string{"192.168.64.0/24", GuestCIDR} {
		t.Run(subnet, func(t *testing.T) {
			attempt := nativeCAPITenantFixture(t, subnet, "ovn")
			request, err := attempt.namedTenantRequest(t.Context(), "native-demo-tenant")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), `"nico":`) || strings.Contains(string(raw), `"osProfile":`) || strings.Contains(string(raw), `"release":`) {
				t.Fatal("generic CAPI request retained direct NICo provider fields")
			}
			if request.Infra == nil || request.ControlPlane.Infra == nil || len(request.NodePools) != 1 || request.NodePools[0].Infra == nil {
				t.Fatal("missing complete CAPI provider topology")
			}
			for _, infra := range []*TenantMachineInfra{request.ControlPlane.Infra, request.NodePools[0].Infra} {
				if len(infra.BootstrapFiles) != 0 || len(infra.PreCommands) != 1 {
					t.Fatal("NVKE must own bootstrap credential files")
				}
				var template struct {
					Spec struct {
						Template struct {
							Spec struct {
								IPXE string `json:"ipxeScript"`
							} `json:"spec"`
						} `json:"template"`
					} `json:"spec"`
				}
				if err := json.Unmarshal(infra.MachineTemplate, &template); err != nil || template.Spec.Template.Spec.IPXE != attempt.boot.script {
					t.Fatal("CAPI template lost the source-selected actual QCOW boot script", err)
				}
				commands := infra.PreCommands[0]
				for _, required := range []string{"NVKE_AGENT_SKIP_START=1", "nvke-kubeadm-distro-" + request.Kubernetes, "nvke-cni-ovn-charts-" + request.CNIVersion, attempt.api + "/files/ovn.tar", attempt.api + "/files/workload.tar", "sha256sum -c -", "inspecti --quiet", attempt.assets.Images["workload"].Reference, "test ! -e /etc/nke/spire.env", "test -s /etc/spire-agent/agent.conf", "systemctl restart spire-agent.service"} {
					if !strings.Contains(commands, required) {
						t.Fatalf("missing real bootstrap step %s", required)
					}
				}
				guard := strings.Index(commands, "ConditionPathExists=/run/nvkr-native-tenant-preparation.ready")
				install := strings.Index(commands, "NVKE_AGENT_SKIP_START=1")
				ready := strings.Index(commands, "ready > /run/nvkr-native-tenant-preparation.ready")
				if guard < 0 || guard > install || ready < strings.Index(commands, "systemctl restart spire-agent.service") {
					t.Fatal("source-owned agent restart can bypass incomplete consumer preparation")
				}
				for _, forbidden := range []string{"touch /run/cloud-init.done", "bootstrap-success.complete", "SPIRE_JOIN_TOKEN=", " > /etc/spire-agent/agent.conf", "AGENT_ARGS=", "restart nvke-agent.service"} {
					if strings.Contains(commands, forbidden) {
						t.Fatalf("consumer replaced source bootstrap or acceptance: %s", forbidden)
					}
				}
			}
		})
	}
}

func TestNativeCAPITenantDeletionConfirmationRequiresFreshAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, inventory                                           string
		deleting, adopted, canceled, unjoined, wantGone, wantRead bool
	}{
		{name: "gone", inventory: `[]`, deleting: true, adopted: true, wantGone: true, wantRead: true},
		{name: "unrelated", inventory: `[{"id":"unrelated-id","name":"unrelated"}]`, deleting: true, adopted: true, wantGone: true, wantRead: true},
		{name: "retained", inventory: `[{"id":"owned-id","name":"native-demo-tenant"}]`, deleting: true, adopted: true, wantRead: true},
		{name: "replaced", inventory: `[{"id":"replacement-id","name":"native-demo-tenant"}]`, deleting: true, adopted: true, wantRead: true},
		{name: "renamed", inventory: `[{"id":"owned-id","name":"other-name"}]`, deleting: true, adopted: true, wantRead: true},
		{name: "malformed JSON", inventory: `[`, deleting: true, adopted: true, wantRead: true},
		{name: "null inventory", inventory: `null`, deleting: true, adopted: true, wantRead: true},
		{name: "missing identity", inventory: `[{}]`, deleting: true, adopted: true, wantRead: true},
		{name: "not deleting", inventory: `[]`, adopted: true},
		{name: "not adopted", inventory: `[]`, deleting: true},
		{name: "canceled", inventory: `[]`, deleting: true, adopted: true, canceled: true},
		{name: "unjoined", inventory: `[]`, deleting: true, adopted: true, unjoined: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := privateTestDir(t)
			cli, observed := filepath.Join(root, "nvkectl"), filepath.Join(root, "observed")
			writeTenantFixture(t, filepath.Join(root, "inventory.json"), []byte(tc.inventory), 0600)
			writeTenantFixture(t, cli, []byte("#!/bin/sh\nset -eu\ntest \"$*\" = 'get clusters -o json'\nprintf '%s\\n' observed >> \"$OBSERVED\"\ncat \"$INVENTORY\"\n"), 0700)
			owner := tenant{name: "native-demo-tenant", cliPath: cli, nativeCAPI: &nativeCAPITenantOwnership{deleting: tc.deleting}, session: ManagementSession{Runner: Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "OBSERVED=" + observed, "INVENTORY=" + filepath.Join(root, "inventory.json")}}}}
			if tc.adopted {
				owner.id = "owned-id"
			}
			queryErr := errors.New("original nodepool lookup failed")
			cause := queryErr
			if tc.unjoined {
				cause = errors.Join(cause, ErrUnjoined)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			row, err := owner.nativeCAPIDeletionObserved(ctx, cause)
			if row != nil || (err == nil) != tc.wantGone || err != nil && !errors.Is(err, queryErr) {
				t.Fatalf("deletion result=%+v err=%v", row, err)
			}
			reads, readErr := os.ReadFile(observed)
			if tc.wantRead && (readErr != nil || string(reads) != "observed\n") || !tc.wantRead && !errors.Is(readErr, os.ErrNotExist) {
				t.Fatalf("expected exactly one bounded confirmation=%v, observations=%q err=%v", tc.wantRead, reads, readErr)
			}
		})
	}
}

func TestNativeCAPITenantOwnershipBindsStoredClusterAndWorker(t *testing.T) {
	for _, conformance := range []bool{false, true} {
		t.Run(fmt.Sprint(conformance), func(t *testing.T) { nativeCAPITenantOwnershipCases(t, conformance) })
	}
}

func nativeCAPITenantOwnershipCases(t *testing.T, conformance bool) {
	for _, failure := range []string{"", "cluster ID", "cluster name", "site", "control plane VPC", "worker VPC", "worker ID", "worker count", "extra worker", "missing worker", "deleted worker", "mixed provider", "bootstrap command"} {
		t.Run(failure, func(t *testing.T) {
			attempt := nativeCAPITenantFixture(t, GuestCIDR, "ovn")
			if conformance {
				attempt.conformance = &nativeConformanceConfig{}
			}
			request, err := attempt.namedTenantRequest(t.Context(), "native-demo-tenant")
			if err != nil {
				t.Fatal(err)
			}
			binding, err := newNativeCAPITenantOwnership(request)
			if err != nil {
				t.Fatal(err)
			}
			row := tenantRow{Name: request.Name, ID: "owned-id", Infra: request.Infra, ControlPlane: &request.ControlPlane}
			pool := map[string]any{"id": "owned-worker", "clusterID": row.ID, "name": "worker", "infra": request.NodePools[0].Infra, "controlPlane": false, "desiredNodeCount": request.NodePools[0].Count}
			binding.workerID = "owned-worker"
			pools := []map[string]any{pool}
			switch failure {
			case "cluster ID":
				row.ID = "foreign-id"
			case "cluster name":
				row.Name = "foreign-name"
			case "site":
				row.Infra.Cluster = json.RawMessage(strings.Replace(string(row.Infra.Cluster), SiteID, "foreign-site", 1))
			case "control plane VPC":
				row.ControlPlane.Infra.MachineTemplate = json.RawMessage(strings.Replace(string(row.ControlPlane.Infra.MachineTemplate), VPCID, "foreign-vpc", 1))
			case "worker VPC":
				request.NodePools[0].Infra.MachineTemplate = json.RawMessage(strings.Replace(string(request.NodePools[0].Infra.MachineTemplate), VPCID, "foreign-vpc", 1))
			case "worker count":
				pool["desiredNodeCount"] = request.NodePools[0].Count + 1
			case "worker ID":
				pool["id"] = "foreign-worker"
			case "extra worker":
				pools = append(pools, pool)
			case "missing worker":
				pools = nil
			case "deleted worker":
				pools, binding.deleting = nil, true
			case "mixed provider":
				row.NICo = TenantProvider{OrgID, SiteID, VPCID}
			case "bootstrap command":
				row.ControlPlane.Infra.PreCommands = []string{"echo changed"}
			}
			root := privateTestDir(t)
			data, err := json.Marshal(pools)
			if err != nil {
				t.Fatal(err)
			}
			writeTenantFixture(t, filepath.Join(root, "pools.json"), data, 0600)
			cli := filepath.Join(root, "nvkectl")
			writeTenantFixture(t, cli, []byte("#!/bin/sh\nset -eu\ntest \"$1:$2:$3:$4\" = \"get:nodepools:--cluster:native-demo-tenant\"\ncat \"$POOLS\"\n"), 0700)
			owner := tenant{name: request.Name, id: "owned-id", cliPath: cli, nativeCAPI: binding, session: ManagementSession{Runner: Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "POOLS=" + filepath.Join(root, "pools.json")}}}}
			got, err := owner.nativeCAPIRow(t.Context(), []tenantRow{row})
			if (err == nil && got != nil) != (failure == "" || failure == "deleted worker") {
				t.Fatalf("ownership result=%+v err=%v", got, err)
			}
		})
	}
}

func TestNativeCAPITenantSecurityKeepsTPMConfiguration(t *testing.T) {
	values := nativeTenantSecurityValues()
	controller := values["controller"].(map[string]any)
	if controller["supportedInfraProviders"] != "nico,infra" || controller["nodeAgent"].(map[string]string)["spireAttestationMethod"] != "joinToken" {
		t.Fatal("controller cannot issue tenant join tokens")
	}
	env := map[string]string{}
	for _, entry := range controller["env"].([]map[string]string) {
		if _, duplicate := env[entry["name"]]; duplicate {
			t.Fatal("duplicate controller environment entry")
		}
		env[entry["name"]] = entry["value"]
	}
	if env["SPIRE_SERVER_API_ADDRESS"] != "spire-server.spire-server.svc.cluster.local:443" || env["NODE_AGENT_PORT"] != "15005" {
		t.Fatal("site controller must use the SPIRE Service port and retain the node-agent port")
	}
	attestors := values["spire"].(map[string]any)["spire-server"].(map[string]any)["nodeAttestor"].(map[string]any)
	if len(attestors) != 1 || !attestors["joinToken"].(map[string]bool)["enabled"] {
		t.Fatal("join-token enablement must not replace existing TPM settings")
	}
}

// The explicitly supplied source-built CLI exercises NVKE's real request
// decoder against an owned local HTTP fixture. It starts no cluster or guest.
func TestNativeCAPITenantActualNVKERequestDecoder(t *testing.T) {
	cli := os.Getenv("NVKR_NATIVE_CAPI_TEST_CLI")
	if cli == "" {
		t.Skip("requires the selected-source native NVKE CLI")
	}
	attempt := nativeCAPITenantFixture(t, GuestCIDR, "ovn")
	request, err := attempt.namedTenantRequest(t.Context(), "native-demo-tenant")
	if err != nil {
		t.Fatal(err)
	}
	observed := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/clusters" {
			http.NotFound(w, r)
			return
		}
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		observed <- body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","name":"native-demo-tenant"}`))
	}))
	defer server.Close()
	root := privateTestDir(t)
	config := filepath.Join(root, "config", "nvkectl")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	writeTenantFixture(t, filepath.Join(config, "config.yaml"), []byte("current-site: fixture\nsites:\n  fixture:\n    url: "+server.URL+"\n    kubeconfig: ''\n"), 0600)
	path := filepath.Join(root, "request.json")
	if err := privateJSON(path, request); err != nil {
		t.Fatal(err)
	}
	runner := Runner{Work: root, Env: SetEnv(NativeEnvironment(), map[string]string{"XDG_CONFIG_HOME": filepath.Join(root, "config")})}
	if err := runner.Run(t.Context(), []string{cli, "create", "cluster", "-f", path}, "selected-nvke-capi-decoder", time.Minute); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-observed:
		var actual TenantRequest
		if err := json.Unmarshal(raw, &actual); err != nil {
			t.Fatal(err)
		}
		if actual.Infra == nil || !sameInfraManifest(actual.Infra.Cluster, request.Infra.Cluster) || !sameMachineInfra(actual.ControlPlane.Infra, request.ControlPlane.Infra) || len(actual.NodePools) != 1 || !sameMachineInfra(actual.NodePools[0].Infra, request.NodePools[0].Infra) {
			t.Fatal("actual NVKE decoder changed CAPI input")
		}
	default:
		t.Fatal("actual NVKE CLI never submitted decoded request")
	}
}
