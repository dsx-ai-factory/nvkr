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
		assets: NativeAssets{arch: "arm64", Installer: root, installerSelection: selection, Images: map[string]NativeImageAsset{
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
			if tc.name == "retained" {
				attempt := nativeCAPITenantFixture(t, GuestCIDR, "ovn")
				request, err := attempt.namedTenantRequest(t.Context(), owner.name)
				if err != nil {
					t.Fatal(err)
				}
				binding, err := newNativeCAPITenantOwnership(request)
				if err != nil {
					t.Fatal(err)
				}
				binding.deleting = true
				owner.nativeCAPI = binding
				raw, err := json.Marshal([]tenantRow{{Name: owner.name, ID: owner.id, Infra: request.Infra, ControlPlane: &request.ControlPlane}})
				if err != nil {
					t.Fatal(err)
				}
				writeTenantFixture(t, filepath.Join(root, "inventory.json"), raw, 0600)
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

func TestNativeDeletionWaitRequiresFreshAbsenceAfterUnavailableReads(t *testing.T) {
	for _, scenario := range []string{"cluster unavailable", "pool and fallback unavailable", "pool unavailable", "fallback malformed", "fallback null", "fallback replaced", "fallback renamed", "fallback infrastructure", "null inventory", "invalid inventory", "other exit", "fatal pool retained", "fatal pool fallback unavailable", "persistent unavailable", "expiry after retry"} {
		t.Run(scenario, func(t *testing.T) {
			attempt := nativeCAPITenantFixture(t, GuestCIDR, "ovn")
			request, err := attempt.namedTenantRequest(t.Context(), "native-demo-tenant")
			if err != nil {
				t.Fatal(err)
			}
			binding, err := newNativeCAPITenantOwnership(request)
			if err != nil {
				t.Fatal(err)
			}
			binding.workerID = "owned-worker"
			row := tenantRow{Name: request.Name, ID: "owned-id", Infra: request.Infra, ControlPlane: &request.ControlPlane}
			root := privateTestDir(t)
			if err := privateJSON(filepath.Join(root, "clusters.json"), []tenantRow{row}); err != nil {
				t.Fatal(err)
			}
			if err := privateJSON(filepath.Join(root, "pools.json"), []map[string]any{{"id": binding.workerID, "clusterID": row.ID, "name": "worker", "infra": request.NodePools[0].Infra, "desiredNodeCount": 1}}); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "fallback replaced":
				row.ID = "foreign-id"
			case "fallback renamed":
				row.Name = "foreign-name"
			case "fallback infrastructure":
				row.ControlPlane.Infra.PreCommands = []string{"changed"}
			}
			if err := privateJSON(filepath.Join(root, "fallback.json"), []tenantRow{row}); err != nil {
				t.Fatal(err)
			}
			cli := filepath.Join(root, "nvkectl")
			writeTenantFixture(t, cli, []byte(`#!/bin/sh
set -eu
case "$*" in
 'delete cluster native-demo-tenant') printf 'delete\n' >> "$FIXTURE/events"; touch "$FIXTURE/deleted";;
 'get nodepools -o json')
  if test -f "$FIXTURE/deleted"; then
   case "$SCENARIO" in 'fatal pool '*) exit 7;; *) exit 1;; esac
  fi
  cat "$FIXTURE/pools.json";;
 'get clusters -o json')
  if test ! -f "$FIXTURE/deleted"; then cat "$FIXTURE/clusters.json"; exit; fi
  n=0; if test -f "$FIXTURE/reads"; then n=$(cat "$FIXTURE/reads"); fi
  n=$((n+1)); printf '%s\n' "$n" > "$FIXTURE/reads"
  case "$SCENARIO:$n" in
   'cluster unavailable:1'|'pool and fallback unavailable:2'|'persistent unavailable:'*|'expiry after retry:1'|'fatal pool fallback unavailable:2') exit 1;;
   'expiry after retry:2') exec sleep 60;;
   'other exit:1') exit 7;;
   'null inventory:1'|'fallback null:2') printf 'null';;
   'invalid inventory:1') printf '[{}]';;
   'fallback malformed:2') printf '[';;
   'fallback replaced:2'|'fallback renamed:2'|'fallback infrastructure:2') cat "$FIXTURE/fallback.json";;
   'pool and fallback unavailable:1'|'pool unavailable:1'|'pool unavailable:2'|'fallback '*:1|'fatal pool '*:1|'fatal pool retained:2') cat "$FIXTURE/clusters.json";;
   *) printf '[]';;
  esac;;
 *) exit 42;;
esac
`), 0700)
			owner := tenant{name: request.Name, id: "owned-id", intent: true, cliPath: cli, nativeCAPI: binding, deletionPollInterval: 50 * time.Millisecond, session: ManagementSession{Work: root, Runner: Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "FIXTURE=" + root, "SCENARIO=" + scenario}}}}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			if scenario == "persistent unavailable" || scenario == "expiry after retry" {
				// Admission and the single delete finish before starting the short wait budget.
				if _, err := owner.row(ctx); err != nil {
					t.Fatal(err)
				}
				owner.nativeCAPI.deleting = true
				if _, err := owner.capture(ctx, []string{owner.cli(), "delete", "cluster", owner.name}, "delete", tenantDeleteTimeout); err != nil {
					t.Fatal(err)
				}
				err = owner.waitDeleted(ctx, 3*time.Second)
			} else {
				err = owner.restore(ctx)
			}
			recovered := scenario == "cluster unavailable" || scenario == "pool and fallback unavailable" || scenario == "pool unavailable"
			if (err == nil) != recovered {
				t.Fatalf("fresh deletion confirmation=%v, error=%v", recovered, err)
			}
			if scenario == "persistent unavailable" || scenario == "expiry after retry" {
				var command *CommandError
				if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &command) || command.ExitCode != 1 {
					t.Fatal("wait lost its deadline or original unavailable read", err)
				}
			}
			expected := map[string]struct {
				cause string
				reads string
			}{
				"cluster unavailable": {"", "2\n"}, "pool and fallback unavailable": {"", "3\n"}, "pool unavailable": {"", "3\n"},
				"fallback malformed": {"unexpected end of JSON input", "2\n"}, "fallback null": {"requires an array", "2\n"},
				"fallback replaced": {"identity/provider changed", "2\n"}, "fallback renamed": {"changed its name", "2\n"}, "fallback infrastructure": {"infrastructure changed", "2\n"},
				"null inventory": {"requires an array", "1\n"}, "invalid inventory": {"invalid cluster identity", "1\n"}, "other exit": {"exit 7", "1\n"},
				"fatal pool retained": {"exit 7", "2\n"}, "fatal pool fallback unavailable": {"exit 7", "2\n"}, "expiry after retry": {"deadline exceeded", "2\n"},
			}
			if want, ok := expected[scenario]; ok {
				reads, readErr := os.ReadFile(filepath.Join(root, "reads"))
				if readErr != nil || string(reads) != want.reads || want.cause != "" && (err == nil || !strings.Contains(err.Error(), want.cause)) {
					t.Fatalf("wrong failure or observation path: reads=%q err=%v readErr=%v", reads, err, readErr)
				}
			}
			deletes, readErr := os.ReadFile(filepath.Join(root, "events"))
			if readErr != nil || string(deletes) != "delete\n" {
				t.Fatal("observation retried or skipped natural deletion", readErr)
			}
		})
	}
}

func TestNativeDeletionReadFailureRetainsFatalErrors(t *testing.T) {
	joinedExit := &CommandError{ExitCode: 1}
	for _, tc := range []struct {
		name    string
		err     error
		pending bool
	}{
		{"joined read exit", joinedExit, true},
		{"not deleting", joinedExit, false},
		{"unadopted", joinedExit, false},
		{"not native", joinedExit, false},
		{"other exit", &CommandError{ExitCode: 7}, false},
		{"invalid joined evidence", errors.Join(joinedExit, errors.New("invalid JSON")), false},
		{"unknown failure", errors.New("unknown read failure"), false},
		{"unjoined", &CommandError{ExitCode: 1, Cause: ErrUnjoined}, false},
		{"canceled", &CommandError{ExitCode: 1, Cause: context.Canceled}, false},
		{"deadline", &CommandError{ExitCode: 1, Cause: context.DeadlineExceeded}, false},
		{"command deadline", &CommandError{ExitCode: 143, Cause: errors.Join(context.DeadlineExceeded, errCommandDeadline)}, false},
		{"output failure", &CommandError{ExitCode: -1, Cause: errors.New("output unavailable")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owner := tenant{id: "owned", nativeCAPI: &nativeCAPITenantOwnership{deleting: true}}
			switch tc.name {
			case "not deleting":
				owner.nativeCAPI.deleting = false
			case "unadopted":
				owner.id = ""
			case "not native":
				owner.nativeCAPI = nil
			}
			err := owner.nativeDeletionReadFailure(t.Context(), tc.err)
			_, pending := err.(*nativeDeletionReadPending)
			if pending != tc.pending || !errors.Is(err, tc.err) {
				t.Fatal("deletion read changed its retry classification or original cause", err)
			}
		})
	}
}

func TestNativeCAPITenantOwnershipBindsStoredClusterAndWorker(t *testing.T) {
	for _, conformance := range []bool{false, true} {
		t.Run(fmt.Sprint(conformance), func(t *testing.T) { nativeCAPITenantOwnershipCases(t, conformance) })
	}
}

func TestNativeCAPITenantDeletionPoolInventoryAvoidsNameResolution(t *testing.T) {
	attempt := nativeCAPITenantFixture(t, GuestCIDR, "ovn")
	request, err := attempt.namedTenantRequest(t.Context(), "native-demo-tenant")
	if err != nil {
		t.Fatal(err)
	}
	binding, err := newNativeCAPITenantOwnership(request)
	if err != nil {
		t.Fatal(err)
	}
	binding.workerID, binding.deleting = "owned-worker", true
	row := tenantRow{Name: request.Name, ID: "owned-id", Infra: request.Infra, ControlPlane: &request.ControlPlane}
	root := privateTestDir(t)
	if err := privateJSON(filepath.Join(root, "pools.json"), []map[string]any{{"id": binding.workerID, "clusterID": row.ID, "name": "worker", "infra": request.NodePools[0].Infra}}); err != nil {
		t.Fatal(err)
	}
	if err := privateJSON(filepath.Join(root, "clusters.json"), []tenantRow{row}); err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(root, "nvkectl")
	writeTenantFixture(t, cli, []byte(`#!/bin/sh
set -eu
case "$*" in
 'get nodepools --cluster native-demo-tenant -o json') printf 'synthetic private name lookup error\n' >&2; exit 1;;
 'get nodepools -o json') cat "$FIXTURE/pools.json";;
 'get clusters -o json') cat "$FIXTURE/clusters.json";;
 *) exit 42;;
esac
`), 0700)
	owner := tenant{name: request.Name, id: row.ID, cliPath: cli, nativeCAPI: binding, session: ManagementSession{Runner: Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "FIXTURE=" + root}}}}
	got, err := owner.nativeCAPIRow(t.Context(), []tenantRow{row})
	if err != nil || got == nil || got.ID != row.ID {
		t.Fatal("stable-ID pool inventory depended on a separate cluster-name lookup", err)
	}
}

func nativeCAPITenantOwnershipCases(t *testing.T, conformance bool) {
	for _, failure := range []string{"", "unrelated worker", "unrelated after deletion", "moved worker", "duplicate moved worker", "duplicate before adoption", "null inventory", "object inventory", "malformed inventory", "invalid worker identity", "invalid cluster identity", "invalid unrelated identity", "cluster ID", "cluster name", "site", "control plane VPC", "worker VPC", "worker ID", "worker name", "worker count", "extra worker", "missing worker", "deleted worker", "mixed provider", "bootstrap command"} {
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
			case "unrelated worker", "unrelated after deletion":
				foreign := map[string]any{"id": "unrelated-worker", "clusterID": "unrelated-cluster", "name": "legacy pool/with spaces"}
				if failure == "unrelated after deletion" {
					pools, binding.deleting = []map[string]any{foreign}, true
				} else {
					pools = append(pools, foreign)
				}
			case "moved worker":
				pool["clusterID"] = "foreign-cluster"
				binding.deleting = true
			case "duplicate moved worker", "duplicate before adoption":
				pools = append(pools, map[string]any{"id": binding.workerID, "clusterID": "foreign-cluster", "name": "worker"})
				if failure == "duplicate before adoption" {
					binding.workerID = ""
				}
			case "null inventory", "malformed inventory", "object inventory":
				pools, binding.deleting = nil, true
			case "invalid unrelated identity":
				pools = append(pools, map[string]any{"clusterID": "foreign-cluster", "name": "worker"})
			case "invalid worker identity":
				pool["id"] = ""
			case "invalid cluster identity":
				pool["clusterID"] = ""
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
			case "worker name":
				pool["name"] = "foreign-worker"
			case "extra worker":
				pools = append(pools, pool)
			case "missing worker":
				pools = nil
			case "deleted worker":
				pools, binding.deleting = []map[string]any{}, true
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
			switch failure {
			case "malformed inventory":
				data = []byte(`[{`)
			case "object inventory":
				data = []byte(`{}`)
			}
			writeTenantFixture(t, filepath.Join(root, "pools.json"), data, 0600)
			cli := filepath.Join(root, "nvkectl")
			writeTenantFixture(t, cli, []byte("#!/bin/sh\nset -eu\ntest \"$*\" = \"get nodepools -o json\"\ncat \"$POOLS\"\n"), 0700)
			owner := tenant{name: request.Name, id: "owned-id", cliPath: cli, nativeCAPI: binding, session: ManagementSession{Runner: Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "POOLS=" + filepath.Join(root, "pools.json")}}}}
			got, err := owner.nativeCAPIRow(t.Context(), []tenantRow{row})
			if (err == nil && got != nil) != (failure == "" || failure == "deleted worker" || failure == "unrelated worker" || failure == "unrelated after deletion") {
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

func TestNativeSecurityValuesSupplyAdmittedStaging(t *testing.T) {
	for _, missing := range []string{"", "volumes", "volumeMounts"} {
		t.Run(missing, func(t *testing.T) {
			controller := nativeTenantSecurityValues()["controller"].(map[string]any)
			if missing != "" {
				delete(controller, missing)
			}
			raw, err := json.Marshal(controller)
			if err != nil {
				t.Fatal(err)
			}
			var authored struct {
				Volumes      []nativeIdentityVolume
				VolumeMounts []nativeIdentityMount
			}
			if err := json.Unmarshal(raw, &authored); err != nil {
				t.Fatal(err)
			}
			var pod nativeIdentityPod
			pod.pod.Spec.Volumes = authored.Volumes
			pod.container.VolumeMounts = authored.VolumeMounts
			if err := nativeIdentityStaging(pod); (err == nil) != (missing == "") {
				t.Fatal("authored staging disagrees with runtime admission", err)
			}
		})
	}
}
