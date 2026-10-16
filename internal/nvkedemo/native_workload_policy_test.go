// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	"go.yaml.in/yaml/v3"
)

// Exact identical runcmd bytes from b615's ARM64 ClusterRelease and
// NodePoolRelease fixtures. Shared with the release/network integration tests.
const nativeWorkloadSourceRuncmd = `
runcmd:
  - systemctl stop nvke-agent.service
  - systemctl stop spire-agent.service
{{- if .TeleportEnabled }}
  - echo 'Starting Teleport installation at '$(date) >> /var/log/cloud-init-teleport.log
  - DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 teleport-ent=18.6.4
  - command -v teleport || { echo 'Teleport binary not found' >> /var/log/cloud-init-teleport.log; exit 1; }
  - systemctl enable --now teleport >> /var/log/cloud-init-teleport.log 2>&1
  - systemctl is-active --quiet teleport && echo 'Teleport service started successfully' >> /var/log/cloud-init-teleport.log || { echo 'Teleport service failed to start' >> /var/log/cloud-init-teleport.log; systemctl status teleport --no-pager >> /var/log/cloud-init-teleport.log; exit 1; }
  - echo 'Teleport installation completed at '$(date) >> /var/log/cloud-init-teleport.log
{{- end }}
  - DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 nvke-agent nvke-kubeadm-distro-{{ trimPrefix .KubernetesVersion "v" }} spire-agent
  - [sh, -c, 'u=http://carbide-pxe.forge:18080/files/node-agent/nvke-agent; if curl -fsSL "$u" -o /tmp/nvke-agent; then install -m 0755 /tmp/nvke-agent /usr/local/bin/nvke-agent && echo "downloaded local nvke-agent" && /usr/local/bin/nvke-agent -version; else echo "local nvke-agent not available at $u; using image agent"; fi']
{{- if .CalicoEnabled }}
  - DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 nvke-cni-calico-images-3.30.1 nvke-cni-calico-manifests-3.30.1
{{- end }}
{{- if .OvnEnabled }}
  # The charts package is what the post-init CNI install reads.
  - DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 nvke-cni-ovn-images-1.4.0 nvke-cni-ovn-charts-1.4.0
{{- end }}
  - sysctl --system
  - systemctl daemon-reload
  - systemctl restart spire-agent.service
  - systemctl restart nvke-agent.service
  - 'systemctl restart procps || echo "Warning: procps restart failed but continuing" >> /var/log/cloud-init-teleport.log'
  - touch /run/cloud-init.done
`

func nativeWorkloadTestTemplate() string {
	return "#cloud-config\npackage_update: false\npackage_upgrade: false\n" + nativeWorkloadSourceRuncmd + "\nphone_home:\n  post: all\n"
}

func TestNativeWorkloadPreloadPreservesAgentStartupAndRejectsUnknownSource(t *testing.T) {
	original := nativeWorkloadTestTemplate()
	for _, tc := range []struct {
		name, data string
		valid      bool
	}{
		{"reviewed source", original, true},
		{"selected source 69ef9924", strings.ReplaceAll(original, "1.4.0", "1.6.0"), true},
		{"changed package", strings.Replace(original, "nvke-kubeadm-distro-", "other-package-", 1), false},
		{"changed reload", strings.Replace(original, "systemctl daemon-reload", "systemctl status", 1), false},
		{"changed restart", strings.Replace(original, "restart nvke-agent.service", "start nvke-agent.service", 1), false},
		{"unknown later restart", strings.Replace(original, "  - touch /run/cloud-init.done", "  - systemctl start nvke-agent.service\n  - touch /run/cloud-init.done", 1), false},
		{"duplicate runcmd", original + nativeWorkloadSourceRuncmd, false},
		{"missing phone home", strings.Replace(original, "\nphone_home:\n", "\nunknown:\n", 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			init := map[string]any{"userDataTemplate": tc.data}
			err := nativeDemoWorkloadPolicy(map[string]any{"machineInit": init}, "http://172.30.0.1:18080/files/workload.tar", "sha256:"+strings.Repeat("a", 64))
			if (err == nil) != tc.valid {
				t.Fatalf("projection error=%v; valid=%v", err, tc.valid)
			}
			got := init["userDataTemplate"].(string)
			if !tc.valid {
				if got != tc.data {
					t.Fatal("refused source mutated")
				}
				return
			}
			const originalAPT = "  - DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 nvke-agent nvke-kubeadm-distro-{{ trimPrefix .KubernetesVersion \"v\" }} spire-agent\n"
			const sourceRestarts = "  - sysctl --system\n  - systemctl daemon-reload\n  - systemctl restart spire-agent.service\n  - systemctl restart nvke-agent.service\n"
			if strings.Count(got, "NVKE_AGENT_SKIP_START=1 DEBIAN_FRONTEND=noninteractive apt-get install") != 1 {
				t.Fatal("source postinst startup was not deferred exactly once")
			}
			// Only the package item and final restart block may change.
			before, remainder, ok := strings.Cut(tc.data, originalAPT)
			if !ok {
				t.Fatal("fixture omitted the source apt command")
			}
			between, after, ok := strings.Cut(remainder, sourceRestarts)
			if !ok || !strings.HasPrefix(got, before) || !strings.HasSuffix(got, after) || strings.Count(got, between) != 1 {
				t.Fatal("unrelated source commands changed")
			}
			commands := nativeWorkloadRenderedCommands(t, got, true)
			packageItem, ok := commands[2].([]any)
			if !ok || len(packageItem) != 3 || !strings.Contains(packageItem[2].(string), "nvke-kubeadm-distro-1.34.2 spire-agent") || strings.Contains(packageItem[2].(string), "{{") {
				t.Fatal("package group did not preserve/render NVKE's trimPrefix action")
			}

		})
	}
}

func nativeWorkloadRenderedCommands(t *testing.T, raw string, ovn bool) []any {
	t.Helper()
	// NVKE bootstrap_userdata.go uses text/template with utils.TrimPrefix(s,prefix).
	// Exercise that standard engine on the pinned startup fixture, not a partial
	// renderer or an approximation of its template syntax.
	source, err := template.New("startup-fixture").Funcs(template.FuncMap{"trimPrefix": strings.TrimPrefix}).Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	var rendered bytes.Buffer
	if err := source.Execute(&rendered, map[string]any{"TeleportEnabled": false, "CalicoEnabled": false, "OvnEnabled": ovn, "KubernetesVersion": "v1.34.2"}); err != nil {
		t.Fatal(err)
	}
	var config struct {
		RunCmd []any `yaml:"runcmd"`
	}
	if err := yaml.Unmarshal(rendered.Bytes(), &config); err != nil {
		t.Fatal(err)
	}
	return config.RunCmd
}

func TestNativeWorkloadImportGatesRestartsDespiteCloudInitContinuation(t *testing.T) {
	for _, fail := range []string{"", "package", "package-partial", "package-partial-stale-ready", "sysctl", "reload", "download", "digest", "containerd", "import", "cri", "spire"} {
		name := fail
		if name == "" {
			name = "success"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			payload := []byte("observed local image transport fixture\n")
			digest := "sha256:" + plainSHA(payload)
			if fail == "digest" {
				payload = []byte("changed local transport\n")
			}
			payloadFile := filepath.Join(root, "payload")
			if err := os.WriteFile(payloadFile, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			shims := map[string]string{
				"apt-get": `
case "$*" in
 *nvke-kubeadm-distro-*)
  printf 'apt-base skip=%s frontend=%s\n' "${NVKE_AGENT_SKIP_START:-}" "${DEBIAN_FRONTEND:-}" >> "$CALLS"
  test "${NVKE_AGENT_SKIP_START:-}" = 1
  test "${DEBIAN_FRONTEND:-}" = noninteractive
  test "$FAIL_STAGE" != package
  : > "$RUNTIME_READY"
  case "$FAIL_STAGE" in package-partial|package-partial-stale-ready) exit 42 ;; esac
  ;;
 *) printf 'apt-cni\n' >> "$CALLS" ;;
esac
`,
				"systemctl": `
printf 'systemctl %s\n' "$*" >> "$CALLS"
case "$*" in
 daemon-reload) test "$FAIL_STAGE" != reload ;;
 'start containerd.service') test -f "$RUNTIME_READY"; test "$FAIL_STAGE" != containerd ;;
 'restart spire-agent.service') test "$FAIL_STAGE" != spire ;;
esac
`,
				"sysctl": `
printf 'sysctl\n' >> "$CALLS"
test "$FAIL_STAGE" != sysctl
`,
				"curl": `
# The unchanged source's optional agent URL is absent in this isolated fixture.
if [ "$1" = -fsSL ]; then exit 1; fi
printf 'curl-workload\n' >> "$CALLS"
test "$FAIL_STAGE" != download
while [ "$#" -gt 0 ]; do
 if [ "$1" = -o ]; then cp "$PAYLOAD" "$2"; exit 0; fi
 shift
done
exit 2
`,
				"ctr": `
printf 'ctr %s\n' "$*" >> "$CALLS"
test -f "$RUNTIME_READY"
test "$FAIL_STAGE" != import
: > "$IMPORTED"
`,
				"crictl": `
printf 'crictl %s\n' "$*" >> "$CALLS"
test "$*" = "--runtime-endpoint unix:///run/containerd/containerd.sock --image-endpoint unix:///run/containerd/containerd.sock --timeout 10s inspecti --quiet $WORKLOAD_IMAGE"
test -f "$IMPORTED"
test "$FAIL_STAGE" != cri
`,
				"touch": `printf 'touch %s\n' "$*" >> "$CALLS"`,
			}
			for name, script := range shims {
				if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/bash\nset -eu\n"+script+"\n"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			init := map[string]any{"userDataTemplate": nativeWorkloadTestTemplate()}
			if err := nativeDemoWorkloadPolicy(map[string]any{"machineInit": init}, "http://172.30.0.1:18080/files/workload.tar", digest); err != nil {
				t.Fatal(err)
			}
			commands := nativeWorkloadRenderedCommands(t, init["userDataTemplate"].(string), true)
			state := filepath.Join(root, "packages.state")
			if fail == "package-partial-stale-ready" {
				if err := os.WriteFile(state, []byte("ready\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			calls := filepath.Join(root, "calls")
			env := SetEnv(os.Environ(), map[string]string{"PATH": bin + string(os.PathListSeparator) + os.Getenv("PATH"), "CALLS": calls, "PAYLOAD": payloadFile, "RUNTIME_READY": filepath.Join(root, "runtime-ready"), "IMPORTED": filepath.Join(root, "imported"), "WORKLOAD_IMAGE": WorkloadImage, "FAIL_STAGE": fail})
			// Like cloud-init, continue to later items after a failed runcmd command.
			// Guest-only absolute paths are redirected into the isolated fixture.
			var failures int
			for _, row := range commands {
				var argv []string
				switch v := row.(type) {
				case string:
					argv = []string{"/bin/sh", "-c", v}
				case []any:
					for _, arg := range v {
						value, ok := arg.(string)
						if !ok {
							t.Fatal("non-string command argument")
						}
						argv = append(argv, strings.ReplaceAll(strings.ReplaceAll(value, "/var/lib/nvke-demo-workload.tar", filepath.Join(root, "workload.tar")), "/run/nvke-demo-workload-packages.state", state))
					}
				default:
					t.Fatalf("unexpected source runcmd %T", row)
				}
				ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
				command := exec.CommandContext(ctx, argv[0], argv[1:]...)
				command.Env = env
				output, err := command.CombinedOutput()
				cancel()
				if err != nil {
					failures++
					if fail == "" {
						t.Fatalf("success command %q failed: %v\n%s", argv, err, output)
					}
				}
			}
			raw, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			log := string(raw)
			if !strings.Contains(log, "systemctl restart procps\n") || !strings.Contains(log, "touch /run/cloud-init.done\n") {
				t.Fatal("did not simulate continuation after the guarded item", log)
			}
			if fail != "" {
				if strings.HasPrefix(fail, "package") {
					stateBytes, stateErr := os.ReadFile(state)
					if stateErr != nil || string(stateBytes) != "pending\n" {
						t.Fatalf("failed install retained readiness: %q %v", stateBytes, stateErr)
					}
					if strings.Contains(log, "sysctl\n") || strings.Contains(log, "ctr ") || strings.Contains(log, "crictl ") {
						t.Fatal("failed install reached the final guarded work", log)
					}
				}
				if failures == 0 || strings.Contains(log, "systemctl restart nvke-agent.service\n") {
					t.Fatalf("failed %s handoff allowed agent restart:\n%s", fail, log)
				}
				return
			}
			sequence := []string{"apt-base skip=1 frontend=noninteractive\n", "apt-cni\n", "sysctl\n", "systemctl daemon-reload\n", "curl-workload\n", "systemctl start containerd.service\n", "ctr --address /run/containerd/containerd.sock --namespace k8s.io --timeout 120s images import --platform linux/arm64 --label io.cri-containerd.image=managed ", "crictl --runtime-endpoint ", "systemctl restart spire-agent.service\n", "systemctl restart nvke-agent.service\n"}
			previous := -1
			for _, expected := range sequence {
				at := strings.Index(log, expected)
				if at <= previous {
					t.Fatalf("lost package/runtime/import/restart order at %q:\n%s", expected, log)
				}
				previous = at
			}
			if strings.Count(log, "systemctl restart nvke-agent.service\n") != 1 || strings.Count(log, "crictl ") != 1 {
				t.Fatal("duplicated restart or CRI verification", log)
			}
		})
	}
}
