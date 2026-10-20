// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"go.yaml.in/yaml/v3"
)

const installerCAPIVersion = "v1.13.6"
const installerProviderVersion = "v0.0.45"
const installerProviderRepository = "ghcr.io/dsx-ai-factory/cluster-api-provider-nico/controller"

// The beta renderer refuses credentials under shared temporary directories.
// Admit that requirement before creating the management cluster or registry.
func admitStackStateRoot(path string) error {
	for _, root := range []string{"/tmp", "/var/tmp", "/private/tmp", "/private/var/tmp", os.TempDir()} {
		if insidePath(path, root) {
			return errors.New("beta installer state must be outside shared temporary directories")
		}
	}
	return nil
}

// projectProviderImage changes only the private deploy copy. The original
// manifest and its source-owned image/artifact binding remain intact.
func projectProviderImage(ctx context.Context, deploy, reference string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	raw, err := boundedRegularRead(filepath.Join(deploy, "capnico/release.yaml"), diagnosticLimit)
	if err != nil {
		return err
	}
	var binding struct {
		Image    string `yaml:"image"`
		Artifact string `yaml:"artifact"`
	}
	if err = yaml.Unmarshal(raw, &binding); err != nil {
		return err
	}
	repository, digest, ok := strings.Cut(reference, "@")
	hash, hashErr := v1.NewHash(digest)
	if !ok || repository == "" || hashErr != nil || hash.Algorithm != "sha256" || binding.Artifact != "capnico-controller" || binding.Image != installerProviderRepository+":"+installerProviderVersion {
		return errors.New("provider image differs from selected deploy binding")
	}
	if _, err = boundedRegularRead(filepath.Join(deploy, "capnico/capnico.yaml"), diagnosticLimit); err != nil {
		return err
	}
	return privateJSON(filepath.Join(deploy, "capnico/kustomization.yaml"), map[string]any{
		"resources": []string{"capnico.yaml"},
		"images":    []any{map[string]string{"name": installerProviderRepository, "newName": repository, "digest": digest}},
	})
}

// prepareStackSiteSpec supplies the current source-owned renderer with the
// site's stack inputs. Its bootstrap fields are never executed: the separately
// admitted Kind configuration owns the management cluster and its CNI.
func prepareStackSiteSpec(ctx context.Context, session ManagementSession, vip string) (string, error) {
	if err := requireContext(ctx); err != nil {
		return "", err
	}
	path := filepath.Join(session.Work, "stack-sitespec.json")
	config := map[string]any{
		"clusterName": session.Name,
		"cluster": map[string]any{
			"nodes":      []any{map[string]string{"name": session.Name + "-control-plane", "ip": vip}},
			"k8sVersion": "1.37.0", "cniName": "ovn-kubernetes", "podCidr": managementPodCIDR,
			"serviceCidr": managementServiceCIDR, "controlPlaneVIP": vip,
		},
		"spire": map[string]any{"trustDomain": "nvkr.local", "upstreamCA": true},
	}
	return path, privateJSON(path, config)
}

// prepareManagementStack binds credentials only to the private process environment.
func prepareManagementStack(ctx context.Context, session ManagementSession, refs map[string]string, connection SiteConnection) (Runner, string, error) {
	if err := requireContext(ctx); err != nil {
		return session.Runner, "", err
	}
	password, err := randomScalar()
	if err != nil {
		return session.Runner, "", err
	}
	databasePassword, err := randomScalar()
	if err != nil {
		return session.Runner, "", err
	}
	runner := bindNativeSpireCA(session.Runner, session.Work)
	runner.Env = SetEnv(runner.Env, map[string]string{
		"NVKECTL_NICO_TOKEN": connection.Token, "NVKECTL_NODE_USER": "nvkr-demo",
		"NVKECTL_NODE_PASSWORD": password, "NVKECTL_DATABASE_PASSWORD": databasePassword,
	})
	path := filepath.Join(session.Work, "stack-config.json")
	config := map[string]any{
		"nico":   map[string]string{"endpoint": connection.Endpoint, "orgID": connection.Org, "apiName": "forge"},
		"images": map[string]string{"controller": refs["cwe"], "profiles": refs["profiles"]},
	}
	return runner, path, privateJSON(path, config)
}

// prepareStackNode removes only the source-documented control-plane scheduling
// taint, and only from the single node already owned by this diagnostic.
func prepareStackNode(ctx context.Context, session ManagementSession) error {
	kubectl, kube := filepath.Join(session.Tools, "kubectl"), environmentValue(session.Runner.Env, "SITE_KUBECONFIG")
	type schedulingNodes struct {
		Items []struct {
			Metadata struct{ Name string }
			Spec     struct {
				Taints []struct{ Key, Effect string }
			}
		} `json:"items"`
	}
	nodes, err := captureJSON[schedulingNodes](ctx, session.Runner, []string{kubectl, "--kubeconfig", kube, "get", "nodes", "-o", "json"}, "site-scheduling-node")
	if err != nil {
		return err
	}
	if len(nodes.Items) != 1 || nodes.Items[0].Metadata.Name != session.Name+"-control-plane" {
		return errors.New("site scheduling requires the single owned management node")
	}
	for _, taint := range nodes.Items[0].Spec.Taints {
		if taint.Key == "node-role.kubernetes.io/control-plane" && taint.Effect == "NoSchedule" {
			return session.Runner.Run(ctx, []string{kubectl, "--kubeconfig", kube, "taint", "node", nodes.Items[0].Metadata.Name, "node-role.kubernetes.io/control-plane:NoSchedule-"}, "site-enable-scheduling", 30*time.Second)
		}
	}
	return nil
}

// installSelectedStack uses the beta deployment contract with already verified
// local charts and native image digests. It neither constructs a Release nor
// invokes the development wrapper that rebuilds those products.
func installSelectedStack(ctx context.Context, session ManagementSession, checkout, deploy, vip, gateway string, refs map[string]string, connection SiteConnection, capture chartCapture) error {
	if err := projectProviderImage(ctx, deploy, refs["provider"]); err != nil {
		return err
	}
	if err := appendChartValues(ctx, capture, filepath.Join(checkout, installerDeployReader), deploy, "controller", "nvkr-profiles-image.json", profilesImageValues(refs["profiles"])); err != nil {
		return err
	}
	spec, err := prepareStackSiteSpec(ctx, session, vip)
	if err != nil {
		return err
	}
	runner, stack, err := prepareManagementStack(ctx, session, refs, connection)
	if err != nil {
		return err
	}
	telemetry, err := sourceBytes(ctx, session.Runner, checkout, "nktp/hack/e2e-values.yaml", "kind-nktp-values")
	if err != nil {
		return err
	}
	var telemetryValues map[string]any
	if err = yaml.Unmarshal(telemetry, &telemetryValues); err != nil {
		return err
	}
	if err = appendChartValues(ctx, capture, filepath.Join(checkout, installerDeployReader), deploy, "nktp", "nvkr-kind-values.json", telemetryValues); err != nil {
		return err
	}
	session.Runner = runner
	state := filepath.Join(session.Work, "nvke-state")
	if err = os.Mkdir(state, 0o700); err != nil {
		return err
	}
	cli := filepath.Join(session.Work, "cli", installerCLIName)
	kubectl, kube := filepath.Join(session.Tools, "kubectl"), environmentValue(session.Runner.Env, "SITE_KUBECONFIG")
	session.Runner.Env = SetEnv(session.Runner.Env, map[string]string{"NVKECTL": cli, "NVKE_STATE_DIR": state})
	if err = prepareStackNode(ctx, session); err != nil {
		return err
	}
	commands := []struct {
		phase string
		args  []string
		limit time.Duration
	}{
		{"site-readiness", []string{"bash", filepath.Join(checkout, "deploy/check-site.sh"), "--sitespec", spec, "--stack-config", stack, "--kubeconfig", kube}, 5 * time.Minute},
		{"site-cluster-api", []string{filepath.Join(session.Tools, "clusterctl"), "init", "--kubeconfig", kube, "--core", "cluster-api:" + installerCAPIVersion, "--bootstrap", "kubeadm:" + installerCAPIVersion, "--control-plane", "kubeadm:" + installerCAPIVersion, "--wait-providers"}, 10 * time.Minute},
		{"site-install", []string{"bash", filepath.Join(checkout, "deploy/nvke-deploy.sh"), "install", "--deploy-path", deploy, "--sitespec", spec, "--stack-config", stack, "--kubeconfig", kube, "--timeout", "15m"}, 30 * time.Minute},
		{"site-capi-identity", []string{"bash", filepath.Join(checkout, "controller/hack/apply-capi-spiffe-exec-auth.sh"), "--kubeconfig", kube}, 10 * time.Minute},
	}
	for _, command := range commands {
		if err = session.Runner.Run(ctx, command.args, command.phase, command.limit); err != nil {
			return err
		}
	}
	// The source helper tolerates absent CAPI deployments; this consumer requires
	// all three and observes their completed rollouts after the identity overlay.
	for _, provider := range []string{"capi-system/capi-controller-manager", "capi-kubeadm-bootstrap-system/capi-kubeadm-bootstrap-controller-manager", "capi-kubeadm-control-plane-system/capi-kubeadm-control-plane-controller-manager"} {
		ns, name, _ := strings.Cut(provider, "/")
		if err = session.Runner.Run(ctx, []string{kubectl, "--kubeconfig", kube, "-n", ns, "rollout", "status", "deployment/" + name, "--timeout=180s"}, "site-ready-"+ns, 4*time.Minute); err != nil {
			return err
		}
	}
	policy := filepath.Join(session.Work, "api-clients.json")
	if err = privateJSON(policy, map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]string{"name": "nvkr-api-clients", "namespace": installerControllerName},
		"spec": map[string]any{
			"podSelector": map[string]any{"matchLabels": map[string]string{"app.kubernetes.io/name": installerControllerName}},
			"policyTypes": []string{"Ingress"},
			"ingress":     []any{map[string]any{"from": []any{map[string]any{"ipBlock": map[string]string{"cidr": gateway + "/32"}}}, "ports": []any{map[string]any{"protocol": "TCP", "port": 8080}}}},
		},
	}); err != nil {
		return err
	}
	if err = session.Runner.Run(ctx, []string{kubectl, "--kubeconfig", kube, "apply", "-f", policy}, "site-client-policy", 30*time.Second); err != nil {
		return err
	}
	return session.Runner.Run(ctx, []string{cli, "config", "set-site", "--name", session.Name, "--url", fmt.Sprintf("http://%s:30080", vip), "--kubeconfig", kube}, "site-register", 30*time.Second)
}
