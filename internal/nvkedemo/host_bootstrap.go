// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

// Guest startup prepares the operating system and reports its TPM identity.
// NVKE commands, bootstrap and deployment run on the execution host.
const hostSitePreparationScript = `#!/bin/bash
set -euo pipefail
umask 077
work=/var/lib/nvke-native
source "$work/return-status.sh"
install -d -m 0700 "$work/bin" "$work/downloads"
cd "$work"
{{range .Assets}}
curl --fail --silent --show-error --connect-timeout 10 --proto '=http' \
  {{quote .URL}} -o {{quote (printf "downloads/%s" .Name)}}
printf '%s  %s\n' {{quote (sha .Digest)}} {{quote (printf "downloads/%s" .Name)}} | sha256sum -c -
{{end}}
install -m 0755 downloads/nerdctl bin/nerdctl
export PATH="$work/bin:$PATH"
DEBIAN_FRONTEND=noninteractive NVKE_AGENT_SKIP_START=1 apt-get install -y -o DPkg::Lock::Timeout=600 \
  nvke-agent spire-agent nvke-kubeadm-distro-{{.KubernetesVersion}} nvke-cni-ovn-images-{{.OVNVersion}} nvke-cni-ovn-charts-{{.OVNVersion}}
# Cloud-init wrote logging-only drop-ins before package installation. Reload
# even when APT finds the packages already installed and runs no postinst.
systemctl daemon-reload
systemctl stop nvke-agent.service spire-agent.service
install -d -m 0700 /etc/nke
install -d -m 1777 /var/lib/nvke-native/postgres /var/lib/nvke-native/spire
install -m 0644 downloads/spire-ca.crt /etc/nke/trust-bundle.pem
printf '%s' {{quote (spireEnvironment .)}} > /etc/nke/spire.env
{{configureSpire}}
{{range $role, $reference := .ImageImports}}
{{importImage (printf "downloads/%s.tar" $role) $reference}}
{{end}}
{{prepareOVN}}
systemctl start --no-block spire-agent.service nvke-agent.service
python3 - "$(get_tpm_pubhash)" <<'IDENTITY' > site-prepared.json
import json,sys
json.dump({'tpmEkThumbprint':sys.argv[1]},sys.stdout)
IDENTITY
curl --fail --silent --show-error --connect-timeout 10 --max-time 60 \
  --proto '=https' --cacert handoff-ca.pem --header @handoff-header \
  --header 'X-NVKE-Startup-Status: prepared' --data-binary @site-prepared.json {{quote .HandoffURL}}
`

func admitNativeSiteNetworks(bridge nativeBridgeConfig) error {
	if !bridge.prefix.IsValid() {
		return nvkrerr.New(nvkrerr.CodeEngineCapability, "site bootstrap requires an observed guest subnet")
	}
	for _, network := range []string{managementPodCIDR, managementServiceCIDR} {
		if bridge.prefix.Overlaps(netip.MustParsePrefix(network)) {
			return nvkrerr.New(nvkrerr.CodeEngineCapability, "site Kubernetes range %s overlaps guest subnet %s", network, bridge.prefix)
		}
	}
	return nil
}

func (s *nativeSiteAttempt) bootstrapOnHost(ctx context.Context, runner Runner, ip, tools, out string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	if err := admitNativeSiteNetworks(s.bridge); err != nil {
		return err
	}
	kubernetes, err := nativeKubernetesVersion(ctx, s.assets)
	if err != nil {
		return err
	}
	home := filepath.Join(out, "home")
	if err := os.Mkdir(home, 0700); err != nil {
		return err
	}
	runner.Env = SetEnv(runner.Env, map[string]string{"HOME": home, "XDG_CONFIG_HOME": filepath.Join(out, "config"), "XDG_CACHE_HOME": filepath.Join(home, ".cache"), "XDG_STATE_HOME": filepath.Join(home, ".local", "state")})
	raw, err := os.ReadFile(filepath.Join(out, "site-prepared.json"))
	if err != nil {
		return err
	}
	var prepared struct {
		TPM string `json:"tpmEkThumbprint"`
	}
	if err := json.Unmarshal(raw, &prepared); err != nil {
		return err
	}
	thumbprint, err := hex.DecodeString(prepared.TPM)
	if err != nil || len(thumbprint) != 32 {
		return errors.New("site preparation did not return a valid TPM identity")
	}
	if err := admitNativeSiteContract(ctx, s.assets); err != nil {
		return err
	}
	cli := s.assets.CLI
	shared := filepath.Join(out, "bootstrap-shared")
	caRoot := filepath.Join(shared, "ca")
	for _, directory := range []string{shared, caRoot, filepath.Join(shared, "tmp")} {
		if err := os.Mkdir(directory, 0700); err != nil {
			return err
		}
	}
	for _, name := range []string{"ca.crt", "ca.key"} {
		if err := copyPrivate(ctx, filepath.Join(filepath.Dir(s.files), name), filepath.Join(caRoot, name), 0600); err != nil {
			return err
		}
	}
	if s.bootstrap == nil {
		images := []string{s.assets.Images["controller"].File, s.assets.Images["provisioned-provider"].File}
		backend, err := prepareHostBootstrap(ctx, runner, runtime.GOOS, shared, s.bridge.gateway.String(), images)
		s.bootstrap = backend
		if err != nil {
			return err
		}
	}
	runner = s.bootstrap.bind(runner)
	if err := os.WriteFile(filepath.Join(out, "hosts.csv"), []byte("name,ip,tpm_ek_thumbprint\nnvke-native-site,"+ip+","+prepared.TPM+"\n"), 0600); err != nil {
		return err
	}
	version, _, _, err := selectedOVNOutputs(s.network)
	if err != nil {
		return err
	}
	args := []string{cli, "generate", "sitespec", "nvke-native-site", "--nodes", filepath.Join(out, "hosts.csv"),
		"--set", "cluster.k8sVersion=" + kubernetes, "--set", "cluster.cniName=ovn-kubernetes", "--set", "cluster.cniVersion=" + version,
		"--set", "cluster.podCidr=" + managementPodCIDR, "--set", "cluster.serviceCidr=" + managementServiceCIDR,
		"--set", "cluster.kubeletConfig.systemReservedMemory=1Gi", "--set", "spire.trustDomain=nvke-native-site.nke.local",
		"--set", "spire.upstreamCA=true"}
	runner = bindNativeSpireCA(runner, caRoot)
	spec, err := s.capture(ctx, runner, "generate-sitespec", args...)
	if err != nil {
		return err
	}
	specPath := filepath.Join(out, "sitespec.yaml")
	if err := os.WriteFile(specPath, spec, 0600); err != nil {
		return err
	}
	for _, role := range []string{"controller", "provisioned-provider"} {
		if err := runner.Run(ctx, []string{"nerdctl", "load", "--input", s.assets.Images[role].File}, "host-import-"+role, 0); err != nil {
			return err
		}
	}
	identity, err := randomScalar()
	if err != nil {
		return err
	}
	cluster := "nvke-bootstrap-" + identity[:12]
	names, err := s.capture(ctx, runner, "existing-bootstrap-clusters", filepath.Join(tools, "kind"), "get", "clusters")
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(string(names)) {
		if name == cluster {
			return errors.New("fresh bootstrap name already exists")
		}
	}
	s.bootstrapName = cluster
	timeout, err := remainingWork(ctx)
	if err != nil {
		return err
	}
	runner.Env = SetEnv(runner.Env, map[string]string{"NVKECTL_BOOTSTRAP_CLUSTER_NAME": cluster, "NVKECTL_BOOTSTRAP_WAIT_READY": timeout.String()})
	s.bootstrapRunner = &runner
	if err := runner.Run(ctx, []string{cli, "bootstrap", "site", "--sitespec", specPath, "--nvke-controller-image", s.assets.Images["controller"].Reference, "--provisioned-provider-image", s.assets.Images["provisioned-provider"].Reference,
		"--kubeconfig-out", filepath.Join(out, "site.kubeconfig"), "--encryption-key-out", filepath.Join(out, "site-etcd-encryption-key.json"), "--diagnostics-dir", filepath.Join(out, "bootstrap-diagnostics"), "--keep-bootstrap-cluster", "--timeout", timeout.String()}, "host-nvke-bootstrap", 0); err != nil {
		return err
	}
	if err := s.deployFromHost(ctx, runner, cli, tools, out, specPath); err != nil {
		return err
	}
	receipt, err := s.completeNativeSiteIdentity(ctx, runner, ip, out)
	if err != nil {
		return &nativeSiteIdentityFailure{cause: err}
	}
	if receipt == nil {
		return &nativeSiteIdentityFailure{cause: errors.New("site identity owner returned no completion receipt")}
	}
	return s.stopBootstrap(ctx)
}

func (s *nativeSiteAttempt) deployFromHost(ctx context.Context, runner Runner, cli, tools, out, specPath string) error {
	runner.Env = SetEnv(runner.Env, map[string]string{"KUBECONFIG": filepath.Join(out, "site.kubeconfig"), "NVKECTL": cli})
	version, err := nativeCAPIProviderVersion(ctx, s.assets)
	if err != nil {
		return err
	}
	timeout, err := remainingWork(ctx)
	if err != nil {
		return err
	}
	// The selected clusterctl's init --help declares integer seconds.
	initArgs := append(nativeCAPIInitArgs(tools, filepath.Join(out, "site.kubeconfig"), version), fmt.Sprintf("--wait-provider-timeout=%d", (timeout-1)/time.Second+1))
	if err := runner.Run(ctx, initArgs, "host-site-capi", 0); err != nil {
		return err
	}
	storage := `apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: nvke-native-local
  annotations: {storageclass.kubernetes.io/is-default-class: "true"}
provisioner: kubernetes.io/no-provisioner
volumeBindingMode: Immediate
`
	for _, item := range []struct{ name, capacity string }{{"postgres", "40Gi"}, {"spire", "10Gi"}} {
		storage += fmt.Sprintf("---\napiVersion: v1\nkind: PersistentVolume\nmetadata:\n  name: nvke-native-%s\nspec:\n  capacity: {storage: %s}\n  accessModes: [ReadWriteOnce]\n  storageClassName: nvke-native-local\n  persistentVolumeReclaimPolicy: Retain\n  hostPath: {path: /var/lib/nvke-native/%s, type: DirectoryOrCreate}\n", item.name, item.capacity, item.name)
	}
	storagePath := filepath.Join(out, "local-storage.yaml")
	if err := os.WriteFile(storagePath, []byte(storage), 0600); err != nil {
		return err
	}
	if err := runner.Run(ctx, []string{filepath.Join(tools, "kubectl"), "apply", "-f", storagePath}, "host-site-storage", time.Minute); err != nil {
		return err
	}
	runner, stackConfig, err := s.prepareNativeStack(ctx, runner, out)
	if err != nil {
		return err
	}
	if _, err := s.capture(ctx, runner, "validate-sitespec", cli, "validate", "sitespec", "--sitespec", specPath); err != nil {
		return err
	}
	telemetry, err := demoTelemetryValues()
	if err != nil {
		return err
	}
	telemetryPath := filepath.Join(out, "demo-telemetry-values.yaml")
	if err := os.WriteFile(telemetryPath, telemetry, 0600); err != nil {
		return err
	}
	args := []string{"bash", filepath.Join(s.assets.Installer, "deploy", "nvke-deploy.sh"), "install", "--deploy-path", filepath.Join(s.assets.Installer, "deploy", "nico"), "--sitespec", specPath, "--kubeconfig", filepath.Join(out, "site.kubeconfig"), "--values-for", "nktp=" + telemetryPath}
	if stackConfig != "" {
		args = append(args, "--stack-config", stackConfig)
	}
	if len(s.assets.RuntimeOutputs) != 0 {
		values := runtimeValues("docker.io", s.assets.RuntimeOutputs)
		for _, item := range []string{"postgresql", "spire", "temporal"} {
			path := filepath.Join(out, item+"-runtime-images.json")
			if err := privateJSON(path, values[item]); err != nil {
				return err
			}
			args = append(args, "--values-for", item+"="+path)
		}
	}
	securityValues := nativeTenantSecurityValues()
	for _, item := range []string{"controller", "spire"} {
		path := filepath.Join(out, item+"-native-tenant-security.json")
		if err := privateJSON(path, securityValues[item]); err != nil {
			return err
		}
		args = append(args, "--values-for", item+"="+path)
	}
	deployment, helmArgs, err := prepareSiteHelm(ctx, runner, tools, out)
	if err != nil {
		return err
	}
	args = append(args, helmArgs...)
	if err := deployment.Run(ctx, args, "host-nvke-deploy", 0); err != nil {
		return err
	}
	// Both source commands remain under the original work deadline.
	return runner.Run(ctx, []string{"bash", filepath.Join(s.assets.Installer, "controller", "hack", "apply-capi-spiffe-exec-auth.sh")}, "host-site-spiffe-exec", 0)
}

// The source controller starts HTTP after migrations. Gate its unchanged
// liveness/readiness probes on startup, using Helm's supported post-renderer
// and the bundled kubectl's Kustomize rather than modifying source charts.
// The original Runner context still bounds the installation process.
func prepareSiteHelm(ctx context.Context, runner Runner, tools, out string) (Runner, []string, error) {
	budget, err := remainingWork(ctx)
	if err != nil {
		return runner, nil, err
	}
	seconds := int64((budget-1)/time.Second + 1)
	if seconds > math.MaxInt32 {
		return runner, nil, nvkrerr.New(nvkrerr.CodeUsage, "site work budget exceeds the Kubernetes progress deadline range")
	}
	home := filepath.Join(out, "site-helm")
	if err := os.Mkdir(home, 0700); err != nil {
		return runner, nil, err
	}
	plugins := filepath.Join(home, "plugins")
	plugin := filepath.Join(plugins, "nvkr-site-startup")
	if err := os.MkdirAll(plugin, 0700); err != nil {
		return runner, nil, err
	}
	files := []struct{ name, content string }{
		{"plugin.yaml", `apiVersion: v1
type: postrenderer/v1
name: nvkr-site-startup
version: 0.1.0
runtime: subprocess
runtimeConfig:
  platformCommand:
    - command: /bin/sh
      args: ["${HELM_PLUGIN_DIR}/render.sh"]
`},
		{"render.sh", `#!/bin/sh
# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0
set -eu
umask 077
scratch=$(mktemp -d "$HELM_PLUGIN_DIR/render.XXXXXX")
trap 'rm -rf "$scratch"' EXIT
trap 'exit 1' HUP INT TERM
cat > "$scratch/rendered.yaml"
cp "$HELM_PLUGIN_DIR/kustomization.yaml" "$scratch/kustomization.yaml"
"$NVKR_SITE_KUBECTL" kustomize "$scratch"
`},
		{"kustomization.yaml", fmt.Sprintf(`apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources: [rendered.yaml]
patches:
  - target:
      group: apps
      version: v1
      kind: Deployment
      name: nke-controller
      namespace: nke-controller
    patch: |-
      apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: nke-controller
        namespace: nke-controller
      spec:
        progressDeadlineSeconds: %d
        template:
          spec:
            containers:
              - name: nke-controller
                startupProbe:
                  httpGet: {path: /healthz, port: http}
                  periodSeconds: 10
                  failureThreshold: %d
`, seconds, (seconds+9)/10)},
	}
	for _, file := range files {
		if err := privateWrite(filepath.Join(plugin, file.name), []byte(file.content)); err != nil {
			return runner, nil, err
		}
	}
	runner.Env = SetEnv(runner.Env, map[string]string{
		"HELM_PLUGINS": plugins, "HELM_CACHE_HOME": filepath.Join(home, "cache"),
		"HELM_CONFIG_HOME": filepath.Join(home, "config"), "HELM_DATA_HOME": filepath.Join(home, "data"),
		"NVKR_SITE_KUBECTL": filepath.Join(tools, "kubectl"),
	})
	return runner, []string{"--timeout", budget.String(), "--", "--post-renderer", "nvkr-site-startup"}, nil
}

// nativeCAPIProviderVersion rechecks the extracted metadata against the selected
// completed tool Run. Source revisions and provider versions remain catalog data.
func nativeCAPIProviderVersion(ctx context.Context, assets NativeAssets) (string, error) {
	if err := requireContext(ctx); err != nil {
		return "", err
	}
	selection := assets.installerSelection
	if selection == nil {
		return "", errors.New("native installer has no checked tool Run selection")
	}
	commit, err := readRunFile(ctx, assets.Installer, "SOURCE_COMMIT", 41)
	if err != nil {
		return "", fmt.Errorf("read native source selection: %w", err)
	}
	version, err := readRunFile(ctx, assets.Installer, "CAPI_VERSION", 128)
	if err != nil {
		return "", fmt.Errorf("read native CAPI selection: %w", err)
	}
	versions, err := readRunFile(ctx, assets.Installer, "make/tool-versions.mk", 64<<10)
	if err != nil {
		return "", fmt.Errorf("read native source versions: %w", err)
	}
	if string(commit) != selection.commit+"\n" || string(version) != selection.capiVersion+"\n" || plainSHA(versions) != selection.versionsDigest {
		return "", errors.New("native installer metadata differs from the checked tool Run")
	}
	return selection.capiVersion, nil
}

func nativeCAPIInitArgs(tools, kubeconfig, version string) []string {
	return []string{filepath.Join(tools, "clusterctl"), "init", "--kubeconfig", kubeconfig,
		"--core", "cluster-api:" + version, "--bootstrap", "kubeadm:" + version,
		"--control-plane", "kubeadm:" + version, "--wait-providers"}
}

// The source chart's redundant telemetry defaults exceed a four-CPU demo
// node. Use its supported values to keep one collector per role with small
// CPU requests; authentication, probes and memory limits remain source-owned.
func demoTelemetryValues() ([]byte, error) {
	values := map[string]any{"otel-lgtm": map[string]any{"enabled": false}}
	for _, role := range []string{"watchdog", "gateway_logs", "gateway_metrics", "gateway_traces", "large_target_metrics", "cluster_events", "daemonset"} {
		resources := func() map[string]any { return map[string]any{"requests": map[string]any{"cpu": "25m"}} }
		settings := map[string]any{"resources": resources()}
		if role != "daemonset" {
			settings["replicas"] = 1
			settings["autoscaler"] = map[string]any{"minReplicas": 1, "maxReplicas": 1}
		}
		if role == "watchdog" || role == "large_target_metrics" || role == "daemonset" {
			settings["targetAllocator"] = map[string]any{"resources": resources()}
		}
		values[role] = map[string]any{"kubernetes": settings}
	}
	return yaml.Marshal(values)
}

// The same selected-source metadata is required at every native executable
// boundary. Extracted text alone cannot replace the checked tool Run selection.
func admitNativeSiteContract(ctx context.Context, assets NativeAssets) error {
	_, err := nativeKubernetesVersion(ctx, assets)
	return err
}

func bindNativeSpireCA(runner Runner, caRoot string) Runner {
	runner.Env = SetEnv(runner.Env, map[string]string{
		"NVKECTL_SPIRE_CA_CERT_FILE": filepath.Join(caRoot, "ca.crt"),
		"NVKECTL_SPIRE_CA_KEY_FILE":  filepath.Join(caRoot, "ca.key"),
	})
	return runner
}

func (s *nativeSiteAttempt) prepareNativeStack(ctx context.Context, runner Runner, out string) (Runner, string, error) {
	if err := requireContext(ctx); err != nil {
		return runner, "", err
	}
	nodePassword, err := randomScalar()
	if err != nil {
		return runner, "", err
	}
	databasePassword, err := randomScalar()
	if err != nil {
		return runner, "", err
	}
	runner = bindNativeSpireCA(runner, filepath.Join(out, "bootstrap-shared", "ca"))
	runner.Env = SetEnv(runner.Env, map[string]string{
		"NVKECTL_NICO_TOKEN": "nico-simctl-token", "NVKECTL_NODE_USER": "nvke-demo",
		"NVKECTL_NODE_PASSWORD": nodePassword, "NVKECTL_DATABASE_PASSWORD": databasePassword,
	})
	path := filepath.Join(out, "stack-config.json")
	config := map[string]any{
		"nico":   map[string]string{"endpoint": "http://" + net.JoinHostPort(s.bridge.gateway.String(), "18080"), "orgID": "novxwn552ssc", "apiName": "forge"},
		"images": map[string]string{"controller": s.assets.Images["controller"].Reference, "profiles": s.assets.Images["profiles"].Reference},
	}
	if err := privateJSON(path, config); err != nil {
		return runner, "", err
	}
	return runner, path, nil
}
