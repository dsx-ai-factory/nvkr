// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
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
	const definition = "definitions/nvke-fde/definition.yaml"
	const environment = "definitions/nvke-fde/environment-github.yaml"
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
	roots := 0
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.BuilderType != ir.BuilderVMImage {
			continue
		}
		roots++
		if node.Component != "nvke-node-fde-amd64" || node.Disk.Encryption != "luks2" || node.ArtifactPath != "fde/nke-node.qcow2" ||
			node.Packer.BuildName != "nke-node-fde.qemu.nke-fde" || node.SourceCommit != "ff21ba8c07cafff74b68e238a8876af11e5beded" {
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
	const definitionPath = "definitions/nvke-m2/definition.yaml"
	const environmentPath = "definitions/nvke-m2/environment-github.yaml"
	definition, err := LoadDefinition(t.Context(), definitionPath)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := LoadEnvironment(t.Context(), environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	controllerCatalog := nvkeM2ControllerCatalogFixture(t)
	for i := range definition.Outputs {
		output := &definition.Outputs[i]
		if output.Catalog != nil && output.Catalog.Base == "../nvke-controller/catalog" {
			output.Catalog.Base = controllerCatalog
		}
	}
	plan, err := Compile(t.Context(), definitionPath, definition, environmentPath, environment, "test (deadbeef)")
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
	names := make([]string, 0, len(plan.LifecycleOutputs))
	for _, output := range plan.LifecycleOutputs {
		names = append(names, output.Name)
	}
	if !slices.Equal(names, wantNames) || len(plan.Builds) != 73 {
		t.Fatalf("maintained M2 must retain all 69 outputs and 73 builds: names=%v, builds=%d", names, len(plan.Builds))
	}
	if plan.Qualification != factory.TierPipelineAttested || !factory.PerOutputPublication(plan) ||
		plan.Environment.Signing == nil || plan.Environment.DiskEvidence != "build-provenance-v1" {
		t.Fatal("maintained M2 lost its publication and evidence policy")
	}
	for _, tc := range []struct {
		arch, installer, patched, plain, toolchain, firmwareCode, firmwareVars, memory string
	}{
		{"amd64", "ubuntu-installer-iso-amd64", "nvke-patched-installer-iso-amd64", "nvke-node-image-amd64",
			"ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/packer@sha256:5bea678dc304d7d6814cd60646cc67b59194644335e83d7fe0a81920e8996013",
			"/usr/share/OVMF/OVMF_CODE_4M.fd", "/usr/share/OVMF/OVMF_VARS_4M.fd", ""},
		{"arm64", "ubuntu-installer-iso", "nvke-patched-installer-iso", "nvke-node-image",
			"ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/packer@sha256:ad467e43ae0ee7f3aaf5b646a117003623b9ea23de035630dc7f5c844758e0db",
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
			roots := 0
			for _, node := range build.Graph.Nodes {
				if node.BuilderType != ir.BuilderVMImage {
					continue
				}
				roots++
				if node.Component != name || node.Disk == nil || node.Packer == nil || node.ExecutionBinding == nil || node.Requirements == nil {
					t.Fatal("incomplete native FDE producer")
				}
				if node.SourceCommit != "ff21ba8c07cafff74b68e238a8876af11e5beded" || node.Toolchain != tc.toolchain ||
					node.ToolchainPlatform != build.Platform || node.Disk.Format != "qcow2" || node.Disk.Encryption != "luks2" ||
					node.ArtifactPath != "fde/nke-node.qcow2" || node.Packer.BuildName != "nke-node-fde.qemu.nke-fde" ||
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

// nvkeM2ControllerCatalogFixture preserves the authored controller producers and
// complete chart pin set while rederiving it from checked-in deploy metadata.
// Only the copied controller source pins change; the FDE catalogs and the full
// output selection remain intact. This is compilation evidence, not an upstream
// checkout or producer execution, and must work without private Git credentials.
func nvkeM2ControllerCatalogFixture(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.CopyFS(directory, os.DirFS("definitions/nvke-controller/catalog")); err != nil {
		t.Fatal(err)
	}
	lock, err := catalog.LoadLockContext(t.Context(), directory)
	if err != nil {
		t.Fatal(err)
	}
	const sourceCommit = "33fb71b2f4224b470ebc628a9165fc4b92870406"
	for _, name := range []string{"nvke", "nvke-cli", "nvke-helm"} {
		if lock.Pins[name].Commit != sourceCommit {
			t.Fatalf("authored %s source=%s, want %s", name, lock.Pins[name].Commit, sourceCommit)
		}
	}
	repo := t.TempDir()
	if err := os.CopyFS(repo, os.DirFS("pkg/nvkr/catalog/testdata/nvke-pin-set")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	t.Setenv("GIT_CONFIG_PARAMETERS", "")
	t.Setenv("GIT_ALLOW_PROTOCOL", "file")
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	t.Setenv("GIT_AUTHOR_DATE", "1733939955 +0000")
	t.Setenv("GIT_COMMITTER_DATE", "1733939955 +0000")
	run := func(args ...string) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...)
		cmd.WaitDelay = time.Second
		raw, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git %v: %v: %s", args, err, raw)
		}
		return strings.TrimSpace(string(raw))
	}
	run("init", "--template=", "-q")
	run("config", "user.name", "M2 Catalog Test")
	run("config", "user.email", "m2-catalog@example.invalid")
	run("add", ".")
	run("-c", "commit.gpgsign=false", "commit", "-q", "-m", "M2 deploy metadata fixture")
	commit, tree := run("rev-parse", "HEAD"), run("rev-parse", "HEAD^{tree}")
	for _, name := range []string{"nvke", "nvke-cli", "nvke-helm"} {
		pin := lock.Pins[name]
		pin.Commit, pin.TreeHash, pin.SourceDateEpoch = commit, tree, 1733939955
		lock.Pins[name] = pin
	}
	for name, pin := range lock.Artifacts {
		if pin.DerivedFrom != "nvke@"+sourceCommit {
			t.Fatalf("authored chart %s has unexpected pin-set owner %q", name, pin.DerivedFrom)
		}
		pin.DerivedFrom = "nvke@" + commit
		lock.Artifacts[name] = pin
	}
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+repo+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", "https://github.com/dsx-ai-factory/nvke")
	if err := catalog.WriteLockContext(t.Context(), directory, lock); err != nil {
		t.Fatal(err)
	}
	return directory
}
