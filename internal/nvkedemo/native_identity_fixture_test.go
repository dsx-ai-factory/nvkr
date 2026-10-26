// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/siteidentity"
	"go.yaml.in/yaml/v3"
)

// The fixture supplies only source/CLI observations. The production handoff
// owner must read the exact stdin, validate every transition and issue its own
// completion receipt before the fake bootstrap backend will accept deletion.
func installNativeIdentityFixture(t *testing.T, attempt *nativeSiteAttempt, tools, root string) {
	t.Helper()
	source, _, _, after, result, _, tpm := nativeIdentityProofFixture(t)
	td := "nvke-native-site.nke.local"
	source.Spec = []byte(strings.ReplaceAll(string(source.Spec), "site.example.test", td))
	var spec map[string]any
	if err := json.Unmarshal(source.Spec, &spec); err != nil {
		t.Fatal(err)
	}
	sourceRaw, err := yaml.Marshal(map[string]any{"apiVersion": "v1", "kind": "List", "items": []any{map[string]any{"apiVersion": "spire.spiffe.io/v1alpha1", "kind": "ClusterStaticEntry", "metadata": map[string]any{"name": source.Metadata.Name, "labels": source.Metadata.Labels}, "spec": spec}}})
	if err != nil {
		t.Fatal(err)
	}
	source, selected, err := nativeSourceStaticEntry(sourceRaw, tpm)
	if err != nil {
		t.Fatal(err)
	}
	after.entries[0].Spec = source.Spec
	after.agents.Agents[0].ID.TrustDomain = td
	after.agents.Agents[0].ExpiresAt = json.Number(fmt.Sprint(time.Now().Add(time.Hour).Unix()))
	after.registrations.Entries[0].SPIFFEID.TrustDomain = td
	after.registrations.Entries[0].ParentID.TrustDomain = td
	result.Before.PeerID = selected.SPIFFEID
	result.After.PeerID = selected.SPIFFEID
	result.After.NotBefore = time.Now().Add(-time.Second)
	result.After.NotAfter = time.Now().Add(time.Hour)
	result.CallerID = "spiffe://" + td + "/nvke-service/controller-fixture"
	result.Status = &siteidentity.AgentStatus{ServerAddress: "172.30.0.2", ServerPort: 32123, ConfigApplied: true, HasValidSVID: true, SPIFFEID: selected.SPIFFEID, ExpiresAt: time.Now().Add(time.Hour).Unix()}
	probe := siteidentity.Result{SchemaVersion: siteidentity.SchemaVersion, Operation: "probe", Completion: siteidentity.NotStarted, CallerID: result.CallerID, Before: result.Before}
	writeJSON := func(name string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		writeTenantFixture(t, filepath.Join(root, name), raw, 0600)
	}
	writeTenantFixture(t, filepath.Join(root, "clusterstaticentries.yaml"), sourceRaw, 0600)
	writeJSON("identity-created.json", after.entries[0])
	for name, value := range map[string]any{"identity-entries-empty.json": map[string]any{"items": []any{}}, "identity-entries.json": map[string]any{"items": after.entries}, "identity-agents-empty.json": nativeSpireAgents{}, "identity-agents.json": after.agents, "identity-registrations-empty.json": nativeSpireRegistrations{}, "identity-registrations.json": after.registrations, "identity-map-empty.json": map[string]any{"data": map[string]string{}}, "identity-map.json": map[string]any{"data": after.allowlist}, "identity-probe.json": probe, "identity-result.json": result} {
		if name == "identity-probe.json" || name == "identity-result.json" {
			raw, err := canonical.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			writeTenantFixture(t, filepath.Join(root, name), append(raw, '\n'), 0600)
		} else {
			writeJSON(name, value)
		}
	}
	helper := []byte("synthetic helper artifact bytes\n")
	writeTenantFixture(t, filepath.Join(tools, nativeIdentityHelper), helper, 0700)
	attempt.assets.SiteTools = tools
	attempt.assets.identity = &nativeIdentityAssets{helperSHA: "sha256:" + plainSHA(helper)}
	attempt.spireEnvironment = nativeBootstrapSpireEnvironment(NativeSiteStartup{Name: "nvke-native-site", Gateway: attempt.bridge.gateway.String()})
	writeTenantFixture(t, filepath.Join(root, "identity-helper-receipt"), []byte(plainSHA(helper)+"  /tmp/nvkr-site-identity.fixture/helper\n"), 0600)
	controller := "docker.io/library/controller@sha256:" + strings.Repeat("c", 64)
	server := "docker.io/nvkr-local/spire-server@sha256:" + strings.Repeat("d", 64)
	attempt.assets.Images["controller"] = NativeImageAsset{File: "controller.tar", Reference: controller, ManifestDigest: "sha256:" + strings.Repeat("c", 64)}
	attempt.assets.Images["runtime-spire-server"] = NativeImageAsset{Reference: server, ManifestDigest: "sha256:" + strings.Repeat("d", 64)}
	mounts := func(name, path string) map[string]any {
		return map[string]any{"name": name, "mountPath": path, "readOnly": true}
	}
	containerState := func(name, image string) map[string]any {
		return map[string]any{"name": name, "imageID": image, "ready": true, "containerID": "containerd://" + name, "state": map[string]any{"running": map[string]any{}}}
	}
	controllerPod := map[string]any{"metadata": map[string]any{"name": "controller-fixture", "namespace": "controller-fixture", "uid": "controller-uid"}, "spec": map[string]any{"serviceAccountName": "controller-fixture", "nodeName": "node-fixture", "containers": []any{map[string]any{"name": "controller-fixture", "image": controller, "env": []any{map[string]any{"name": "CONTROLLER_CONFIG_PATH", "value": "/etc/controller/config.yaml"}, map[string]any{"name": "NODE_AGENT_PORT", "value": "15005"}, map[string]any{"name": "SPIRE_SERVER_ADDRESS", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "status.hostIP"}}}}, "volumeMounts": []any{mounts("identity", "/spiffe-workload-api"), mounts("config", "/etc/controller")}}}, "volumes": []any{map[string]any{"name": "identity", "csi": map[string]any{"driver": "csi.spiffe.io", "readOnly": true}}, map[string]any{"name": "config", "configMap": map[string]any{"name": "config-fixture"}}}}, "status": map[string]any{"hostIP": "172.30.0.2", "containerStatuses": []any{containerState("controller-fixture", controller)}}}
	serverPod := map[string]any{"metadata": map[string]any{"name": "server-fixture", "namespace": "server-fixture", "uid": "server-uid", "labels": map[string]string{"app": "server-fixture"}}, "spec": map[string]any{"containers": []any{map[string]any{"name": "server-fixture", "image": server, "volumeMounts": []any{mounts("tpm", "/run/spire/data/tpm-direct/hashes")}}}, "volumes": []any{map[string]any{"name": "tpm", "configMap": map[string]any{"name": "tpm-fixture"}}}}, "status": map[string]any{"containerStatuses": []any{containerState("server-fixture", server)}}}
	writeJSON("identity-pods.json", map[string]any{"items": []any{controllerPod, serverPod}})
	writeJSON("identity-config.json", map[string]any{"metadata": map[string]any{"uid": "config-uid"}, "data": map[string]string{"config.yaml": "spire:\n  className: fixture-class\n  serverNamespace: server-fixture\n  tpmPubhashesConfigMapName: tpm-fixture\n  serverPort: '32123'\n"}})
	writeJSON("identity-nodes.json", map[string]any{"items": []any{map[string]any{"metadata": map[string]string{"name": "node-fixture"}, "status": map[string]any{"nodeInfo": map[string]string{"bootID": result.Before.BootID}, "addresses": []any{map[string]string{"type": "InternalIP", "address": "172.30.0.2"}}}}}})
	writeJSON("identity-services.json", map[string]any{"items": []any{map[string]any{"metadata": map[string]string{"namespace": "server-fixture"}, "spec": map[string]any{"type": "NodePort", "selector": map[string]string{"app": "server-fixture"}, "ports": []any{map[string]any{"protocol": "TCP", "nodePort": 32123}}}}}})
	writeTenantFixture(t, filepath.Join(tools, "kubectl"), []byte(`#!/bin/sh
set -eu
case "$*" in
 *"--dry-run=client"*) printf 'source description must be offline\n' >&2; exit 64;;
 *"create -f - -o json"*)
  test ! -e "$FIXTURE/identity-imported"
  cmp - "$FIXTURE/clusterstaticentries.yaml"
  printf 'identity-import\n' >> "$EVENTS"
  touch "$FIXTURE/identity-imported"
  cat "$FIXTURE/identity-created.json"; exit;;
 *"get pods --all-namespaces"*) cat "$FIXTURE/identity-pods.json"; exit;;
 *"get configmap -n controller-fixture config-fixture"*) cat "$FIXTURE/identity-config.json"; exit;;
 *"get configmap -n server-fixture tpm-fixture"*)
  if test -e "$FIXTURE/identity-imported"; then cat "$FIXTURE/identity-map.json"; else cat "$FIXTURE/identity-map-empty.json"; fi; exit;;
 *"get services -n server-fixture"*) cat "$FIXTURE/identity-services.json"; exit;;
 *"get nodes -o json"*) cat "$FIXTURE/identity-nodes.json"; exit;;
 *"get clusterstaticentries.spire.spiffe.io"*)
  if test -e "$FIXTURE/identity-imported"; then cat "$FIXTURE/identity-entries.json"; else cat "$FIXTURE/identity-entries-empty.json"; fi; exit;;
 *"/opt/spire/bin/spire-server agent list"*)
  if test -e "$FIXTURE/identity-handed-off"; then cat "$FIXTURE/identity-agents.json"; else cat "$FIXTURE/identity-agents-empty.json"; fi; exit;;
 *"/opt/spire/bin/spire-server entry show"*)
  if test -e "$FIXTURE/identity-imported"; then cat "$FIXTURE/identity-registrations.json"; else cat "$FIXTURE/identity-registrations-empty.json"; fi; exit;;
 *"/bin/sh -c umask 077;"*)
  cmp - "$FIXTURE/tools/nvkr-site-identity-helper"
  printf 'identity-stage\n' >> "$EVENTS"; cat "$FIXTURE/identity-helper-receipt"; exit;;
 *"sha256sum /tmp/nvkr-site-identity.fixture/helper"*) cat "$FIXTURE/identity-helper-receipt"; exit;;
 *"-- /tmp/nvkr-site-identity.fixture/helper"*)
  if test ! -e "$FIXTURE/identity-probed"; then
   cat > "$FIXTURE/identity-probe-request.json"
   printf 'identity-probe\n' >> "$EVENTS"; touch "$FIXTURE/identity-probed"; cat "$FIXTURE/identity-probe.json"
  else
   test -e "$FIXTURE/identity-imported"
   cat > "$FIXTURE/identity-handoff-request.json"
   printf 'identity-handoff\n' >> "$EVENTS"; touch "$FIXTURE/identity-handed-off"; cat "$FIXTURE/identity-result.json"
  fi; exit;;
esac
printf 'kubectl\n' >> "$EVENTS"
case "$*" in
 *"get deployments"*) printf '%s\n' '{"items":[{"metadata":{"namespace":"nke-controller"},"spec":{"selector":{"matchLabels":{"app.kubernetes.io/instance":"nke-controller","app.kubernetes.io/name":"cloud-workflow-engine"}}}}]}' ;;
esac
`), 0700)
	writeTenantFixture(t, filepath.Join(tools, "kind"), []byte(`#!/bin/sh
set -eu
case "$*" in
 *delete*)
  test -f "$FIXTURE/identity-handed-off"
  set -- "$FIXTURE"/site-identity-*/completed.json
  test "$#" -eq 1; test -f "$1";;
esac
printf 'kind\n' >> "$EVENTS"
`), 0700)
}

func assertNativeIdentityFixtureCompletion(t *testing.T, root string) {
	t.Helper()
	for _, operation := range []string{"probe", "handoff"} {
		raw, err := os.ReadFile(filepath.Join(root, "identity-"+operation+"-request.json"))
		if err != nil {
			t.Fatal(err)
		}
		request, err := siteidentity.DecodeRequest(t.Context(), raw)
		if err != nil || request.Operation != operation {
			t.Fatalf("fixed request was not admitted: %v", err)
		}
		if request.ServerAddress != "172.30.0.2" || request.ServerPort != 32123 {
			t.Fatal("source-owned reconfiguration did not retain the selected permanent endpoint")
		}
	}
	receipts, err := filepath.Glob(filepath.Join(root, "site-identity-*", "completed.json"))
	if err != nil || len(receipts) != 1 {
		t.Fatal("owning package did not issue one completed receipt", receipts, err)
	}
	raw, err := os.ReadFile(receipts[0])
	if err != nil {
		t.Fatal(err)
	}
	var receipt struct {
		SourceSHA, HelperSHA, CSEUID, RegistrationID, AgentSerial, BootID string
		ObservedAt                                                        time.Time
	}
	if err := nativeIdentityJSON(raw, &receipt); err != nil || receipt.CSEUID == "" || receipt.RegistrationID == "" || receipt.AgentSerial == "" || receipt.BootID == "" || receipt.ObservedAt.IsZero() {
		t.Fatal("incomplete package-issued identity receipt", err)
	}
}
