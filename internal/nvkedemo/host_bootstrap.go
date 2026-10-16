// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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
curl --fail --silent --show-error --connect-timeout 10 --max-time 900 --proto '=http' \
  {{quote .URL}} -o {{quote (printf "downloads/%s" .Name)}}
printf '%s  %s\n' {{quote (sha .Digest)}} {{quote (printf "downloads/%s" .Name)}} | sha256sum -c -
{{end}}
install -m 0755 downloads/nerdctl bin/nerdctl
export PATH="$work/bin:$PATH"
DEBIAN_FRONTEND=noninteractive NVKE_AGENT_SKIP_START=1 apt-get install -y -o DPkg::Lock::Timeout=600 \
  nvke-agent spire-agent nvke-kubeadm-distro-1.34.2 nvke-cni-ovn-images-{{.OVNVersion}} nvke-cni-ovn-charts-{{.OVNVersion}}
systemctl stop nvke-agent.service spire-agent.service
install -d -m 0700 /etc/nke
install -d -m 1777 /var/lib/nvke-native/postgres /var/lib/nvke-native/spire
install -m 0644 downloads/spire-ca.crt /etc/nke/trust-bundle.pem
printf 'SPIRE_SERVER_ADDRESS=%s\nSPIRE_SERVER_PORT=30081\nSPIRE_TRUST_DOMAIN=%s\n' \
  {{quote .Gateway}} {{quote (printf "%s.nke.local" .Name)}} > /etc/nke/spire.env
{{configureSpire}}
systemctl start --no-block spire-agent.service nvke-agent.service
for image in controller profiles capnico{{if .RuntimeImages}} workload{{range runtimeImages}} runtime-{{.}}{{end}}{{range authImages}} {{.}}{{end}}{{end}}; do
  nerdctl --namespace k8s.io load --input "downloads/$image.tar"
done
python3 - "$(get_tpm_pubhash)" <<'IDENTITY' > site-prepared.json
import json,sys
json.dump({'tpmEkThumbprint':sys.argv[1]},sys.stdout)
IDENTITY
curl --fail --silent --show-error --connect-timeout 10 --max-time 60 \
  --proto '=https' --cacert handoff-ca.pem --header @handoff-header \
  --header 'X-NVKE-Startup-Status: prepared' --data-binary @site-prepared.json {{quote .HandoffURL}}
`

func (s *nativeSiteAttempt) bootstrapOnHost(ctx context.Context, runner Runner, ip, tools, out string) error {
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
	spec, err := s.capture(ctx, runner, "generate-sitespec", cli, "generate", "sitespec", "nvke-native-site", "--nodes", filepath.Join(out, "hosts.csv"),
		"--set", "cluster.k8sVersion=1.34.2", "--set", "cluster.cniName=ovn-kubernetes", "--set", "cluster.cniVersion="+version,
		"--set", "cluster.kubeletConfig.systemReservedMemory=1Gi", "--set", "spire.trustDomain=nvke-native-site.nke.local",
		"--set", "spire.caCertFile="+filepath.Join(caRoot, "ca.crt"), "--set", "spire.caKeyFile="+filepath.Join(caRoot, "ca.key"))
	if err != nil {
		return err
	}
	specPath := filepath.Join(out, "sitespec.yaml")
	if err := os.WriteFile(specPath, spec, 0600); err != nil {
		return err
	}
	for _, role := range []string{"controller", "provisioned-provider"} {
		if err := runner.Run(ctx, []string{"nerdctl", "load", "--input", s.assets.Images[role].File}, "host-import-"+role, 3*time.Minute); err != nil {
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
	runner.Env = SetEnv(runner.Env, map[string]string{"NVKECTL_BOOTSTRAP_CLUSTER_NAME": cluster, "NVKECTL_BOOTSTRAP_NODE_IMAGE": "kindest/node:v1.36.1@sha256:3489c7674813ba5d8b1a9977baea8a6e553784dab7b84759d1014dbd78f7ebd5", "NVKECTL_BOOTSTRAP_WAIT_READY": "10m"})
	s.bootstrapRunner = &runner
	if err := runner.Run(ctx, []string{cli, "bootstrap", "site", "--sitespec", specPath, "--controller-image", s.assets.Images["controller"].Reference, "--provisioned-provider-image", s.assets.Images["provisioned-provider"].Reference,
		"--kubeconfig-out", filepath.Join(out, "site.kubeconfig"), "--encryption-key-out", filepath.Join(out, "site-etcd-encryption-key.json"), "--diagnostics-dir", filepath.Join(out, "bootstrap-diagnostics"), "--keep-bootstrap-cluster", "--timeout", "45m"}, "host-nvke-bootstrap", 60*time.Minute); err != nil {
		return err
	}
	if err := s.deployFromHost(ctx, runner, cli, tools, out, specPath); err != nil {
		return err
	}
	return s.stopBootstrap(ctx)
}

func (s *nativeSiteAttempt) deployFromHost(ctx context.Context, runner Runner, cli, tools, out, specPath string) error {
	runner.Env = SetEnv(runner.Env, map[string]string{"KUBECONFIG": filepath.Join(out, "site.kubeconfig"), "NVKECTL": cli})
	version, err := nativeCAPIProviderVersion(ctx, s.assets.Installer)
	if err != nil {
		return err
	}
	if err := runner.Run(ctx, nativeCAPIInitArgs(tools, filepath.Join(out, "site.kubeconfig"), version), "host-site-capi", 5*time.Minute); err != nil {
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
	raw, err := os.ReadFile(specPath)
	if err != nil {
		return err
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return err
	}
	password, err := randomScalar()
	if err != nil {
		return err
	}
	for name, value := range map[string]string{"nico-token": "nico-simctl-token", "unused-pull-token": "local-image-import-only", "tenant-password": password} {
		if err := os.WriteFile(filepath.Join(out, name), []byte(value), 0600); err != nil {
			return err
		}
	}
	doc["controllerStack"] = map[string]any{"arch": "arm64", "nico": map[string]any{"endpoint": "http://" + net.JoinHostPort(s.bridge.gateway.String(), "18080"), "orgID": "novxwn552ssc", "apiName": "forge", "tokenFile": filepath.Join(out, "nico-token")},
		"pullSecret": map[string]any{"server": "ghcr.io", "username": "native-demo-local-import", "tokenFile": filepath.Join(out, "unused-pull-token")},
		"tenant":     map[string]any{"user": "nvke-demo", "passwordFile": filepath.Join(out, "tenant-password")}, "database": map[string]any{"passwordFile": filepath.Join(out, "postgres-password")},
		"images": map[string]any{"controller": s.assets.Images["controller"].Reference, "profiles": s.assets.Images["profiles"].Reference}}
	raw, err = yaml.Marshal(doc)
	if err != nil {
		return err
	}
	if err := os.WriteFile(specPath, raw, 0600); err != nil {
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
	if err := runner.Run(ctx, args, "host-nvke-deploy", 30*time.Minute); err != nil {
		return err
	}
	// The source script allows 180 seconds for each of four controller rollouts.
	// Bound the entire script while allowing those individual source deadlines.
	return runner.Run(ctx, []string{"bash", filepath.Join(s.assets.Installer, "controller", "hack", "apply-capi-spiffe-exec-auth.sh")}, "host-site-spiffe-exec", 15*time.Minute)
}

// nativeCAPIProviderVersion binds the reviewed provider selection to the exact
// selected source bytes. This does not parse Make syntax or resolve latest.
// Changing either source input requires reviewing its provider contract again.
func nativeCAPIProviderVersion(ctx context.Context, installer string) (string, error) {
	commit, err := readRunFile(ctx, installer, "SOURCE_COMMIT", 41)
	if err != nil {
		return "", fmt.Errorf("read native CAPI source selection: %w", err)
	}
	versions, err := readRunFile(ctx, installer, "make/tool-versions.mk", 64<<10)
	if err != nil {
		return "", fmt.Errorf("read native CAPI source versions: %w", err)
	}
	return reviewedNativeCAPIProvider(string(commit), plainSHA(versions))
}

func reviewedNativeCAPIProvider(commit, versionsDigest string) (string, error) {
	// Both reviewed revisions' cli/Makefile inject CAPI_VERSION from this exact file;
	// cli/internal/bootstrap/controllerdeploy/capi.go uses it for all three
	// upstream providers. Keep the permanent site aligned with that bootstrap.
	reviewed := commit == "b6154b292a934dbdff7aa17596cbf2d7ef5f0f85\n" || commit == "69ef9924c4bf0487beba8c3613ae6e0a0260d8f3\n"
	if !reviewed || versionsDigest != "ae8980d7b31d9f2f9c355dd7d873854eaba8cfe94f5075ec61dfa9bdbdb5a0f9" {
		return "", errors.New("native CAPI provider selection differs from reviewed source")
	}
	return "v1.13.6", nil
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
