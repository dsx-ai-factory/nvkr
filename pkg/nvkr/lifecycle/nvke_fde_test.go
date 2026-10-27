// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"sigs.k8s.io/yaml"
)

func TestNVKEFDEOnlyGeneratedSelection(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	const definition = "inputs/definitions/diagnostics/fde-amd64.yaml"
	const environment = "inputs/environments/github-diagnostics.yaml"
	plan, err := CompileFiles(t.Context(), definition, environment, "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.LifecycleOutputs) != 1 || plan.LifecycleOutputs[0].Name != "nvke-node-fde-amd64" || len(plan.Builds) != 1 {
		t.Fatal("FDE iteration must select exactly one named disk build")
	}
	if plan.Environment.Publication != nil || plan.Environment.Signing != nil || plan.Qualification != factory.TierStaticUnqualified {
		t.Fatal("FDE iteration must remain build-only and static-unqualified")
	}
	if len(plan.Environment.GitHubDispatchBranches) != 1 || plan.Environment.GitHubDispatchBranches[0] != "codex/fde-image-pipeline" {
		t.Fatal("FDE iteration lost its exact feature-branch dispatch policy")
	}
	sourcePin, err := plan.Builds[0].CatalogSnapshot.SourcePin("nvke-os")
	if err != nil {
		t.Fatal(err)
	}
	roots := 0
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.BuilderType != ir.BuilderVMImage {
			continue
		}
		roots++
		if node.Component != "nvke-node-fde-amd64" || node.Disk.Encryption != "luks2" || node.ArtifactPath != "fde/nvke-node.qcow2" ||
			node.Packer.BuildName != "nvke-node-fde.qemu.nvke-fde" || node.SourceCommit != sourcePin.Commit || node.SourceTreeHash != sourcePin.TreeHash {
			t.Fatal("FDE producer no longer selects the audited source contract")
		}
		if err := pipeline.ValidateVMImagePackerDiskExecution(node); err != nil {
			t.Fatal(err)
		}
	}
	wantComponents := strings.Fields(`
		nvke-agent nvke-cni-calico nvke-cni-ovn nvke-image-inputs nvke-kubeadm-distro
		nvke-node-fde-amd64 nvke-patched-installer-iso-amd64 nvke-spire-agent ovn-kube-ubuntu ovn-kubernetes-chart
		ubuntu-installer-iso-amd64`)
	if roots != 1 || !slices.Equal(plan.Builds[0].Components, wantComponents) ||
		len(plan.Builds[0].ResolvedComponents) != len(wantComponents) {
		t.Fatal("FDE iteration lost its exact root and material closure")
	}
	root := t.TempDir()
	digest, err := GenerateFiles(t.Context(), definition, environment, "test (deadbeef)", factory.EngineGitHub, root, false)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := GenerateFiles(t.Context(), definition, environment, "test (deadbeef)", factory.EngineGitHub, root, true)
	if err != nil || checked != digest {
		t.Fatalf("FDE generation drift: %s/%s, %v", digest, checked, err)
	}
}

func TestNVKEM2IncludesBothNativeFDEImages(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	const definitionPath = "inputs/definitions/nvke/definition.yaml"
	const environmentPath = "inputs/environments/github-release.yaml"
	definition, err := LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := LoadEnvironment(t.Context(), environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	wantNames := strings.Fields(`
		capi-controller-image capi-controller-image-arm64
		capi-kubeadm-bootstrap-image capi-kubeadm-bootstrap-image-arm64
		capi-kubeadm-control-plane-image capi-kubeadm-control-plane-image-arm64 capnico-controller
		cert-manager-cainjector-image cert-manager-cainjector-image-arm64
		cert-manager-controller-image cert-manager-controller-image-arm64
		cert-manager-webhook-image cert-manager-webhook-image-arm64
		cluster-api-provider-provisioned controller-chart
		csi-node-driver-registrar-image csi-node-driver-registrar-image-arm64 cwe
		nico-reference-profiles-fixture nktp nktp-collector-image nktp-collector-image-arm64
		nktp-kube-state-metrics-image nktp-kube-state-metrics-image-arm64
		nktp-kubectl-image nktp-kubectl-image-arm64 nktp-lgtm-image nktp-lgtm-image-arm64
		nktp-metrics-server-image nktp-metrics-server-image-arm64 nktp-node-exporter-image nktp-node-exporter-image-arm64
		nktp-operator-image nktp-operator-image-arm64 nktp-target-allocator-image nktp-target-allocator-image-arm64
		nvke-node-fde-amd64 nvke-node-fde-arm64 nvke-node-image-amd64 nvke-node-image-arm64
		nvkectl nvkectl-linux-arm64 operator-tools postgresql postgresql-image postgresql-image-arm64
		spiffe-csi-image spiffe-csi-image-arm64 spire spire-agent-image spire-agent-image-arm64
		spire-bash-image spire-bash-image-arm64 spire-controller-manager-image spire-controller-manager-image-arm64
		spire-crds spire-oidc-image spire-oidc-image-arm64 spire-server-image spire-server-image-arm64
		spire-tpm-server-image spire-tpm-server-image-arm64 temporal temporal-admin-tools-image temporal-admin-tools-image-arm64
		temporal-server-image temporal-server-image-arm64 temporal-ui-image temporal-ui-image-arm64`)
	names := make([]string, 0, len(definition.Outputs))
	var diskOutputs []DefinitionOutput
	for _, output := range definition.Outputs {
		names = append(names, output.Name)
		if output.Format == OutputDiskImage {
			diskOutputs = append(diskOutputs, output)
		}
	}
	if !slices.Equal(names, wantNames) || len(diskOutputs) != 4 {
		t.Fatalf("maintained M2 must retain all 69 outputs and four native disks: names=%v, disks=%d", names, len(diskOutputs))
	}
	// This contract concerns the four native disk closures. Compiling unrelated
	// controllers and charts adds no FDE signal and requires a synthetic Git source.
	definition.Outputs = diskOutputs
	// The disk catalog uses the .git source spelling; the controller-only entry
	// would correctly be refused as unused in this reduced selection.
	delete(environment.SourceAccess, "https://github.com/dsx-ai-factory/nvke")
	plan, err := Compile(t.Context(), definitionPath, definition, environmentPath, environment, "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.LifecycleOutputs) != 4 || len(plan.Builds) != 4 {
		t.Fatalf("native disk selection changed: outputs=%d builds=%d", len(plan.LifecycleOutputs), len(plan.Builds))
	}
	if plan.Qualification != factory.TierPipelineAttested || !factory.PerOutputPublication(plan) ||
		plan.Environment.Signing == nil || plan.Environment.DiskEvidence != "build-provenance-v1" {
		t.Fatal("maintained M2 lost its publication and evidence policy")
	}
	for _, tc := range []struct {
		arch, installer, patched, plain, toolchain, firmwareCode, firmwareVars, memory string
	}{
		{"amd64", "ubuntu-installer-iso-amd64", "nvke-patched-installer-iso-amd64", "nvke-node-image-amd64",
			"ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer-native@sha256:1415193ed0c50cd9c3d5b3bbd6207728262db3469c6ccd36717d47a6f50e3c0e",
			"/usr/share/OVMF/OVMF_CODE_4M.fd", "/usr/share/OVMF/OVMF_VARS_4M.fd", ""},
		{"arm64", "ubuntu-installer-iso", "nvke-patched-installer-iso", "nvke-node-image",
			"ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer-native@sha256:0b849513367b69faaa033aaf602f60d42309d7f42ff0045f8c354a4e804a82a3",
			"/usr/share/AAVMF/AAVMF_CODE.no-secboot.fd", "/usr/share/AAVMF/AAVMF_VARS.fd", "4096"},
	} {
		t.Run(tc.arch, func(t *testing.T) {
			name := "nvke-node-fde-" + tc.arch
			index := slices.IndexFunc(plan.Builds, func(build factory.BuildPlan) bool { return build.Name == name })
			if index < 0 {
				t.Fatal("missing FDE build", name)
			}
			build := plan.Builds[index]
			wantComponents := []string{"nvke-agent", "nvke-cni-calico", "nvke-cni-ovn", "nvke-image-inputs", "nvke-kubeadm-distro",
				name, tc.patched, "nvke-spire-agent", "ovn-kube-ubuntu", "ovn-kubernetes-chart",
				tc.installer}
			if build.Platform != "linux/"+tc.arch || !slices.Equal(build.Components, wantComponents) || len(build.ResolvedComponents) != len(wantComponents) {
				t.Fatalf("unexpected native FDE closure: platform=%s components=%v", build.Platform, build.Components)
			}
			sourcePin, err := build.CatalogSnapshot.SourcePin("nvke-os")
			if err != nil {
				t.Fatal(err)
			}
			roots := 0
			for _, node := range build.Graph.Nodes {
				if node.BuilderType != ir.BuilderVMImage {
					continue
				}
				roots++
				if node.Component != name || node.Disk == nil || node.Packer == nil || node.ExecutionBinding == nil || node.Requirements == nil {
					t.Fatal("incomplete native FDE producer")
				}
				if node.SourceCommit != sourcePin.Commit || node.SourceTreeHash != sourcePin.TreeHash || node.Toolchain != tc.toolchain ||
					node.ToolchainPlatform != build.Platform || node.Disk.Format != "qcow2" || node.Disk.Encryption != "luks2" ||
					node.ArtifactPath != "fde/nvke-node.qcow2" || node.Packer.BuildName != "nvke-node-fde.qemu.nvke-fde" ||
					node.Packer.AcquisitionArchive != "fde/apt-acquisitions.tar" {
					t.Fatal("FDE producer differs from the audited native source contract")
				}
				if node.ExecutionBinding.Class.RunsOn != "arc-"+tc.arch+"-kvm" || string(node.ExecutionBinding.Class.HostPlatform) != build.Platform ||
					!slices.Equal(node.Requirements.Capabilities, []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: execution.CapabilityRequired}}) {
					t.Fatal("FDE producer lost its native required-KVM route")
				}
				variables := make(map[string]string, len(node.Packer.Variables))
				for _, variable := range node.Packer.Variables {
					variables[variable.Name] = variable.Value
				}
				if variables["luks_passphrase"] != "changeit" || variables["capture_apt_acquisitions"] != "true" ||
					variables["efi_firmware_code"] != tc.firmwareCode || variables["efi_firmware_vars"] != tc.firmwareVars ||
					variables["guest_memory_mib"] != tc.memory {
					t.Fatalf("unexpected FDE public variables: %v", variables)
				}
				if len(node.Materials) != 2 || node.Materials[0].Binding != "image-inputs" || node.Materials[0].Component != "nvke-image-inputs" ||
					node.Materials[1].Binding != "installer-iso" || node.Materials[1].Component != tc.patched {
					t.Fatalf("FDE material bindings = %+v", node.Materials)
				}
				if err := pipeline.ValidateVMImagePackerDiskExecution(node); err != nil {
					t.Fatal(err)
				}
			}
			if roots != 1 {
				t.Fatalf("FDE build has %d disk roots, want one", roots)
			}
			plainIndex := slices.IndexFunc(plan.Builds, func(build factory.BuildPlan) bool { return build.Name == "nvke-node-image-"+tc.arch })
			if plainIndex < 0 {
				t.Fatal("missing original non-FDE build")
			}
			plain := plan.Builds[plainIndex]
			rootIndex := slices.Index(wantComponents, name)
			if rootIndex < 0 {
				t.Fatal("expected FDE closure omits its disk root")
			}
			wantComponents[rootIndex] = tc.plain
			if !slices.Equal(plain.Components, wantComponents) || plain.CatalogSnapshot.Components[tc.plain].Disk.Encryption != "none" ||
				plain.BuildDigest == build.BuildDigest {
				t.Fatal("original non-FDE closure changed or shares FDE Build identity")
			}
		})
	}
	// Inspect the in-memory renderer; the maintained bundle is regenerated separately.
	bundle, err := factory.RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			RunsOn string                `json:"runs-on"`
			Steps  []struct{ ID string } `json:"steps"`
		} `json:"jobs"`
	}
	if err := yaml.Unmarshal(bundle.Files[factory.GitHubWorkflowPath], &workflow); err != nil {
		t.Fatal(err)
	}
	diskJobs := map[string]int{}
	for _, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if step.ID == "run-disk" {
				diskJobs[job.RunsOn]++
			}
		}
	}
	if len(diskJobs) != 2 || diskJobs["arc-amd64-kvm"] != 2 || diskJobs["arc-arm64-kvm"] != 2 {
		t.Fatalf("disk jobs = %v, want two per native architecture", diskJobs)
	}
}
