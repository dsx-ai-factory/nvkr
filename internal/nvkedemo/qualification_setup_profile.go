// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"slices"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

const demoCosignDigest = "sha256:c5d324e091826b0d7a78eb16fef316450b4eb9aaec045611c08ba06f5e73220a"
const demoCosignURL = "https://github.com/sigstore/cosign/releases/download/v3.1.3/cosign-linux-arm64"
const demoSmokeImage = "index.docker.io/library/debian@sha256:a2ca65377c251208de42e1a2722cb949211236c161b634f6f3663754e2d06850"

// This authored demo scope is independent of candidate artifacts. Never infer
// exemptions or executable profiles from a candidate's observed output set.
func demoQualificationPolicy(plan *factory.Plan, network string, keys [3]string) (lifecycle.LocalQualificationPolicy, error) {
	p := lifecycle.LocalQualificationPolicy{
		SchemaVersion: lifecycle.LocalQualificationPolicySchema, Lane: "unqualified-local-execution",
		Platform: "linux/arm64", CosignDigest: demoCosignDigest,
		BuilderKey: keys[0], SmokeKey: keys[1], QualifierKey: keys[2],
		Smoke: lifecycle.QualificationSmokePolicy{
			Images:         map[string]string{"linux/arm64": demoSmokeImage},
			Tools:          map[string]string{"clusterctl": "clusterctl-version-json", "helm": "helm-version", "kind": "kind-version", "kubectl": "kubectl-client-version-json", "nico-sim": "nicosim-patched-version-json", "nico-simctl": "nicosim-patched-version-json", "nvkectl": "nvkectl-version-json", "oras": "oras-version"},
			NonToolMembers: []lifecycle.QualificationNonToolMember{{Output: "nicosim-tools-arm64", Member: "nico-sim-runtime-assets", Reason: "runtime-assets-archive"}},
		},
	}
	if err := bindDemoQualificationScope(&p, plan, network); err != nil {
		return p, err
	}
	var err error
	p.PlanDigest, err = plan.Digest()
	if err != nil {
		return p, err
	}
	p.DefinitionDigest, p.EnvironmentDigest = plan.DefinitionDigest, plan.EnvironmentDigest
	return p, p.RequirePlan(plan)
}

// Scope binding is descriptive admission; full Plan validation and policy
// matching remain in the lifecycle owner.
func bindDemoQualificationScope(p *lifecycle.LocalQualificationPolicy, plan *factory.Plan, network string) error {
	if plan == nil {
		return configUsage("qualification setup requires a prepared Plan")
	}
	mode, err := normalizeNetwork(network)
	if err != nil {
		return err
	}
	images := map[string]string{
		"capnico-controller-arm64": "capnico-controller", "cluster-api-provider-provisioned-arm64": "cluster-api-provider-provisioned",
		"capi-exec-init-image-arm64": "native-capi-exec-init-image", "capi-exec-plugin-image-arm64": "native-capi-exec-plugin-image",
		"workload-image-arm64": "native-workload-image",
		"controller-profiles":  "nvke-local-profiles", "csi-node-driver-registrar-image-arm64": "csi-node-driver-registrar-image",
		"cwe": "nvke-controller", "ovn-image-arm64": "ovn-kube-ubuntu", "postgresql-image-arm64": "postgresql-image",
		"spiffe-csi-image-arm64": "spiffe-csi-image", "spire-agent-image-arm64": "spire-agent-image", "spire-bash-image-arm64": "spire-bash-image",
		"spire-controller-manager-image-arm64": "spire-controller-manager-image", "spire-oidc-image-arm64": "spire-oidc-image",
		"spire-server-image-arm64": "spire-server-image", "spire-tpm-server-image-arm64": "spire-tpm-server-image",
		"temporal-admin-tools-image-arm64": "temporal-admin-tools-image", "temporal-server-image-arm64": "temporal-server-image", "temporal-ui-image-arm64": "temporal-ui-image",
	}
	charts := map[string]string{"controller-chart": "nvke-controller-chart", "nktp": "nktp-chart", "ovn-chart-arm64": "ovn-kubernetes-chart", "postgresql": "postgresql-chart", "spire": "spire-chart", "spire-crds": "spire-crds-chart", "temporal": "temporal-chart"}
	disk := "nvke-node-image-arm64"
	if mode == "upstream-ovn" {
		delete(images, "ovn-image-arm64")
		delete(images, "controller-profiles")
		delete(charts, "ovn-chart-arm64")
		images["controller-profiles-upstream-ovn"] = "nvke-local-profiles"
		images["upstream-ovn-image-arm64"] = "ovn-kube-ubuntu"
		charts["upstream-ovn-chart-arm64"], disk = "ovn-kubernetes-chart", "nvke-node-image-upstream-ovn-arm64"
	}
	expected := map[string]factory.LifecycleOutputPlan{}
	for name, component := range images {
		expected[name] = factory.LifecycleOutputPlan{Name: name, Kind: lifecycle.OutputOCI, Components: []string{component}}
		p.Smoke.NonToolOutputs = append(p.Smoke.NonToolOutputs, lifecycle.QualificationNonToolOutput{Output: name, Kind: lifecycle.OutputOCI, Components: []string{component}, Reason: "deployment-image"})
	}
	for name, component := range charts {
		expected[name] = factory.LifecycleOutputPlan{Name: name, Kind: "helm-oci", Components: []string{component}}
	}
	for name, components := range map[string][]string{
		"installer-tools-arm64": {"clusterctl", "helm", "kind", "kubectl", "oras"},
		"nicosim-tools-arm64":   {"nico-sim", "nico-sim-runtime-assets", "nico-simctl"},
		"nvkectl":               {"nvkectl"}, "operator-tools": {"helm", "kubectl"},
	} {
		expected[name] = factory.LifecycleOutputPlan{Name: name, Kind: "executable", Components: components}
	}
	expected[disk] = factory.LifecycleOutputPlan{Name: disk, Kind: lifecycle.OutputDiskImage, Components: []string{"nvke-agent", "nvke-cni-calico", "nvke-cni-ovn", "nvke-image-inputs", "nvke-kubeadm-distro", "nvke-node-image", "nvke-patched-installer-iso", "nvke-spire-agent", "ovn-kube-ubuntu", "ovn-kubernetes-chart", "ubuntu-installer-iso"}}
	if len(plan.LifecycleOutputs) != len(expected) {
		return configUsage("qualification setup requires the complete maintained 32-output local demo")
	}
	for _, output := range plan.LifecycleOutputs {
		want, ok := expected[output.Name]
		got := slices.Clone(output.Components)
		sort.Strings(got)
		if !ok || want.Kind != output.Kind || !slices.Equal(want.Components, got) {
			return configUsage("qualification setup Plan differs from the maintained demo output scope")
		}
		delete(expected, output.Name)
	}
	sort.Slice(p.Smoke.NonToolOutputs, func(i, j int) bool { return p.Smoke.NonToolOutputs[i].Output < p.Smoke.NonToolOutputs[j].Output })
	return nil
}
