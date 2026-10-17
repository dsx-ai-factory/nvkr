// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

const nativeNetworkSourceTemplate = `#cloud-config
package_update: true
package_upgrade: false
write_files:
  - path: /etc/nke/trust-bundle.pem
    permissions: '0644'
    content: preserve-source-trust-bundle
runcmd:
  - systemctl stop nvke-agent.service
  - systemctl stop spire-agent.service
{{- if .OvnEnabled }}
  # The charts package is what the post-init CNI install reads.
  - DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 nvke-cni-ovn-images-1.4.0 nvke-cni-ovn-charts-1.4.0
{{- end }}
  - systemctl restart spire-agent.service
  - systemctl restart nvke-agent.service
`

func TestNativeOVNPackageProjectionMatchesTenantSelection(t *testing.T) {
	for _, network := range []string{"", "ovn", "upstream-ovn"} {
		for _, kind := range []string{"clusterrelease", "nodepoolrelease"} {
			t.Run(network+"/"+kind, func(t *testing.T) {
				spec := map[string]any{"machineInit": map[string]any{"userDataTemplate": nativeNetworkSourceTemplate, "preserve": "source"}, "unrelated": "source"}
				if err := nativeDemoOVNPackagePolicy(spec, network); err != nil {
					t.Fatal(err)
				}
				data := spec["machineInit"].(map[string]any)["userDataTemplate"].(string)
				request, err := newTenantRequest("native-demo-tenant", network)
				if err != nil {
					t.Fatal(err)
				}
				if network == "upstream-ovn" && data != nativeNetworkSourceTemplate {
					t.Fatal("explicit matched upstream source changed")
				}
				// The owning source renders the conditional later. Exercise its
				// standard template/cloud-config boundary and inspect the actual
				// package command against the consumer's selected chart stream.
				parsed, err := template.New(kind).Parse(data)
				if err != nil {
					t.Fatal(err)
				}
				var rendered bytes.Buffer
				if err := parsed.Execute(&rendered, struct{ OvnEnabled bool }{true}); err != nil {
					t.Fatal(err)
				}
				var cloud struct {
					RunCmd []string `yaml:"runcmd"`
				}
				if err := yaml.Unmarshal(rendered.Bytes(), &cloud); err != nil {
					t.Fatal(err)
				}
				want := "DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 nvke-cni-ovn-images-" + request.CNIVersion + " nvke-cni-ovn-charts-" + request.CNIVersion
				if len(cloud.RunCmd) != 5 || cloud.RunCmd[2] != want || cloud.RunCmd[0] != "systemctl stop nvke-agent.service" || cloud.RunCmd[1] != "systemctl stop spire-agent.service" || cloud.RunCmd[3] != "systemctl restart spire-agent.service" || cloud.RunCmd[4] != "systemctl restart nvke-agent.service" {
					t.Fatalf("rendered startup lost its matched package or source agent lifecycle: %q", cloud.RunCmd)
				}
				// Reversing only the selected package names must recover every
				// original byte, including trust configuration and unrelated data.
				recovered := strings.NewReplacer("nvke-cni-ovn-images-"+request.CNIVersion, "nvke-cni-ovn-images-1.4.0", "nvke-cni-ovn-charts-"+request.CNIVersion, "nvke-cni-ovn-charts-1.4.0").Replace(data)
				if recovered != nativeNetworkSourceTemplate || spec["unrelated"] != "source" || spec["machineInit"].(map[string]any)["preserve"] != "source" {
					t.Fatal("OVN selection changed unrelated source configuration")
				}
			})
		}
	}
}

func TestNativeOVNPackageProjectionRejectsChangedSourceWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name, data, network string
	}{
		{"missing command", strings.ReplaceAll(nativeNetworkSourceTemplate, "nvke-cni-ovn-", "other-cni-"), "ovn"},
		{"duplicate command", nativeNetworkSourceTemplate + nativeNetworkSourceTemplate, "ovn"},
		{"changed image version", strings.ReplaceAll(nativeNetworkSourceTemplate, "images-1.4.0", "images-2.4.0"), "ovn"},
		{"changed chart version", strings.ReplaceAll(nativeNetworkSourceTemplate, "charts-1.4.0", "charts-2.4.0"), "ovn"},
		{"chart version suffix", strings.ReplaceAll(nativeNetworkSourceTemplate, "charts-1.4.0", "charts-1.4.01"), "ovn"},
		{"changed source flags", strings.ReplaceAll(nativeNetworkSourceTemplate, "Timeout=600", "Timeout=601"), "ovn"},
		{"extra image command", nativeNetworkSourceTemplate + "  - apt-get install nvke-cni-ovn-images-2.4.0\n", "ovn"},
		{"extra chart command", nativeNetworkSourceTemplate + "  - apt-get install nvke-cni-ovn-charts-2.4.0\n", "ovn"},
		{"unknown format", strings.ReplaceAll(nativeNetworkSourceTemplate, "#cloud-config", "#!/bin/sh"), "ovn"},
		{"unknown network", nativeNetworkSourceTemplate, "other"},
		{"non-OVN network", nativeNetworkSourceTemplate, "kind-calico"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := map[string]any{"machineInit": map[string]any{"userDataTemplate": tc.data}, "unrelated": "preserve"}
			before, err := yaml.Marshal(spec)
			if err != nil {
				t.Fatal(err)
			}
			if err := nativeDemoOVNPackagePolicy(spec, tc.network); err == nil {
				t.Fatal("unknown or ambiguous source accepted")
			}
			after, err := yaml.Marshal(spec)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("refused source was mutated")
			}
		})
	}
	for _, spec := range []map[string]any{nil, {}, {"machineInit": "invalid"}, {"machineInit": map[string]any{}}, {"machineInit": map[string]any{"userDataTemplate": 42}}} {
		before, err := yaml.Marshal(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := nativeDemoOVNPackagePolicy(spec, "ovn"); err == nil {
			t.Fatal("missing or malformed source template accepted")
		}
		after, err := yaml.Marshal(spec)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("refused malformed source was mutated")
		}
	}
}

func TestNativeAPIClientAdmissionIsNarrowAndFailsClosed(t *testing.T) {
	selector := map[string]any{"matchLabels": map[string]any{"app.kubernetes.io/instance": "nke-controller", "app.kubernetes.io/name": "observed-chart-name"},
		"matchExpressions": []any{map[string]any{"key": "api", "operator": "Exists"}}}
	deployment := map[string]any{"metadata": map[string]any{"namespace": "nke-controller"}, "spec": map[string]any{"selector": selector}}
	response, _ := json.Marshal(map[string]any{"items": []any{deployment}})
	mac := nativeBridgeConfig{prefix: netip.MustParsePrefix("192.168.64.0/24"), gateway: netip.MustParseAddr("192.168.64.1")}
	for _, tc := range []struct {
		name                                              string
		bridge                                            nativeBridgeConfig
		response                                          string
		queryExit, applyExit                              string
		canceled, invalidGateway, wantPolicy, wantSuccess bool
	}{
		{name: "Linux", bridge: linuxNativeBridge(), response: string(response), wantPolicy: true, wantSuccess: true},
		{name: "Mac", bridge: mac, response: string(response), wantPolicy: true, wantSuccess: true},
		{name: "empty inventory", bridge: mac, response: `{"items":[]}`},
		{name: "ambiguous inventory", bridge: mac, response: `{"items":[{},{}]}`},
		{name: "invalid JSON", bridge: mac, response: `{`},
		{name: "foreign namespace", bridge: mac, response: strings.Replace(string(response), `"namespace":"nke-controller"`, `"namespace":"other"`, 1)},
		{name: "empty selector", bridge: mac, response: `{"items":[{"metadata":{"namespace":"nke-controller"},"spec":{"selector":{}}}]}`},
		{name: "foreign release selector", bridge: mac, response: strings.Replace(string(response), `"app.kubernetes.io/instance":"nke-controller"`, `"app.kubernetes.io/instance":"other"`, 1)},
		{name: "public gateway", bridge: nativeBridgeConfig{prefix: netip.MustParsePrefix("8.8.8.0/24"), gateway: netip.MustParseAddr("8.8.8.8")}, response: string(response), invalidGateway: true},
		{name: "gateway outside bridge", bridge: nativeBridgeConfig{prefix: mac.prefix, gateway: netip.MustParseAddr("172.30.0.1")}, response: string(response), invalidGateway: true},
		{name: "IPv6 gateway", bridge: nativeBridgeConfig{prefix: netip.MustParsePrefix("fd00::/64"), gateway: netip.MustParseAddr("fd00::1")}, response: string(response), invalidGateway: true},
		{name: "canceled", bridge: mac, response: string(response), canceled: true},
		{name: "query failed", bridge: mac, response: string(response), queryExit: "7"},
		{name: "apply failed", bridge: mac, response: string(response), applyExit: "7", wantPolicy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := privateTestDir(t)
			tools := filepath.Join(out, "tools")
			if err := os.Mkdir(tools, 0700); err != nil {
				t.Fatal(err)
			}
			input, called, applied := filepath.Join(out, "response.json"), filepath.Join(out, "called"), filepath.Join(out, "applied")
			writeTenantFixture(t, input, []byte(tc.response), 0600)
			writeTenantFixture(t, filepath.Join(tools, "kubectl"), []byte(`#!/bin/sh
set -eu
printf 'query-or-apply\n' >> "$CALLED"
case "$*" in
 *"get deployments"*) test -z "$QUERY_EXIT" || exit "$QUERY_EXIT"; cat "$RESPONSE";;
 *apply*) printf 'apply\n' > "$APPLIED"; test -z "$APPLY_EXIT" || exit "$APPLY_EXIT";;
 *) exit 42;;
esac
`), 0700)
			runner := Runner{Work: out, Env: []string{"PATH=/usr/bin:/bin", "RESPONSE=" + input, "CALLED=" + called, "APPLIED=" + applied, "QUERY_EXIT=" + tc.queryExit, "APPLY_EXIT=" + tc.applyExit}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			a := nativeSiteAttempt{bridge: tc.bridge}
			err := a.admitAPIClient(ctx, runner, tools, out, filepath.Join(out, "site.kubeconfig"))
			if (err == nil) != tc.wantSuccess {
				t.Fatalf("unexpected policy admission: %v", err)
			}
			if tc.invalidGateway {
				var coded *nvkrerr.Error
				if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeEngineCapability {
					t.Fatalf("gateway code: %v", err)
				}
			}
			if tc.canceled && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation: %v", err)
			}
			if tc.invalidGateway || tc.canceled {
				if _, err := os.Stat(called); !os.IsNotExist(err) {
					t.Fatal("inadmissible policy reached kubectl")
				}
			}
			raw, readErr := os.ReadFile(filepath.Join(out, "nvke-api-client.json"))
			if !tc.wantPolicy {
				if !os.IsNotExist(readErr) {
					t.Fatalf("rejected controller produced policy: %v", readErr)
				}
				if _, err := os.Stat(applied); !os.IsNotExist(err) {
					t.Fatal("rejected controller reached policy apply")
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": "nvke-demo-api-client", "namespace": "nke-controller"},
				"spec": map[string]any{"podSelector": selector, "policyTypes": []any{"Ingress"}, "ingress": []any{map[string]any{
					"from":  []any{map[string]any{"ipBlock": map[string]any{"cidr": netip.PrefixFrom(tc.bridge.gateway, 32).String()}}},
					"ports": []any{map[string]any{"protocol": "TCP", "port": float64(8080)}}}}}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("policy broadened scope or changed observed selector: %s", raw)
			}
			if tc.applyExit != "" && CommandExitCode(err) != 7 {
				t.Fatalf("apply failure hidden: %v", err)
			}
		})
	}
}
