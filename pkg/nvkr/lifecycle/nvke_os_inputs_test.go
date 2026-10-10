// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestNVKEOSExecutionModesSelectArchitectureSpecificDiskMaterials(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repositoryRoot)
	for _, test := range []struct {
		name        string
		definition  string
		environment string
		arch        string
		root        string
		installer   string
		patched     string
		wantKVM     bool
		wantCPU     string
	}{
		{name: "native KVM", definition: "definition-arm64-kvm.yaml",
			environment: "environment-local-linux-arm64-docker.yaml", arch: "arm64",
			root: "nvke-node-image", installer: "ubuntu-installer-iso",
			patched: "nvke-patched-installer-iso", wantKVM: true},
		{name: "amd64 native KVM", definition: "definition-amd64-kvm.yaml",
			environment: "environment-local-linux-amd64-docker.yaml", arch: "amd64",
			root: "nvke-node-image-amd64", installer: "ubuntu-installer-iso-amd64",
			patched: "nvke-patched-installer-iso-amd64", wantKVM: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join("definitions", "nvke-os")
			plan, err := CompileFiles(t.Context(), filepath.Join(root, test.definition),
				filepath.Join(root, test.environment), "test")
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Builds) != 1 || plan.Builds[0].Graph == nil {
				t.Fatalf("NVKE OS plan has %d builds, want one complete build", len(plan.Builds))
			}
			var producer ir.Node
			for _, node := range plan.Builds[0].Graph.Nodes {
				if node.Kind == ir.KindFetch && strings.Contains(strings.Join(node.VendorCmd, "\n"), "{{.Arch}}") {
					t.Fatalf("fetch node %q retained an architecture placeholder", node.ID)
				}
				if node.Component == test.root {
					producer = node
				}
			}
			if producer.Packer == nil || producer.ExecutionBinding == nil {
				t.Fatal("NVKE node-image producer lost Packer or execution binding")
			}
			wantPolicy := execution.CapabilityForbidden
			if test.wantKVM {
				wantPolicy = execution.CapabilityRequired
			}
			if producer.Requirements == nil || len(producer.Requirements.Capabilities) != 1 ||
				producer.Requirements.Capabilities[0].Policy != wantPolicy {
				t.Fatalf("KVM requirement = %+v, want policy %q", producer.Requirements, wantPolicy)
			}
			if producer.ToolchainPlatform != "linux/"+test.arch {
				t.Fatalf("toolchain platform = %q, want linux/%s", producer.ToolchainPlatform, test.arch)
			}
			if len(producer.ExecutionBinding.Capabilities) != 1 ||
				producer.ExecutionBinding.Capabilities[0].Name != execution.CapabilityKVM ||
				producer.ExecutionBinding.Capabilities[0].Enabled != test.wantKVM {
				t.Fatalf("KVM resolution = %+v, want enabled=%t",
					producer.ExecutionBinding.Capabilities, test.wantKVM)
			}
			variables := make(map[string]string, len(producer.Packer.Variables))
			for _, variable := range producer.Packer.Variables {
				variables[variable.Name] = variable.Value
			}
			if got := variables["cpu_model"]; got != test.wantCPU {
				t.Fatalf("cpu_model = %q, want %q", got, test.wantCPU)
			}
			if !slices.Equal(plan.Builds[0].Components, []string{
				"nvke-agent", "nvke-cni-calico", "nvke-cni-ovn", "nvke-image-inputs", "nvke-kubeadm-distro",
				test.root, test.patched, "nvke-spire-agent", "ovn-kube-ubuntu",
				"ovn-kubernetes-chart", test.installer,
			}) {
				t.Fatalf("selected component graph = %v", plan.Builds[0].Components)
			}
			if test.wantKVM {
				baseline := plan.Builds[0].CatalogSnapshot.Components[test.root].ToolchainImages[test.arch]
				attemptID := "sha256:" + strings.Repeat("e", 64)
				var components []string
				for _, name := range plan.Builds[0].Components {
					component := plan.Builds[0].CatalogSnapshot.Components[name]
					if component.FromSource && component.ToolchainImages[test.arch] == baseline {
						components = append(components, name)
					}
				}
				overlay := catalog.Overlay{
					APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind,
					Name: "local-toolchain-" + test.arch, RequiresCatalogDigest: plan.Builds[0].CatalogDigest,
					ToolchainSubstitutions: []catalog.ToolchainSubstitution{{
						Arch: test.arch, From: baseline, To: attemptID, Components: components,
					}},
				}
				stack, err := catalog.ApplyStack(plan.Builds[0].CatalogSnapshot, overlay)
				if err != nil {
					t.Fatalf("attempt-frozen toolchain overlay: %v", err)
				}
				if got := stack.Catalog.Components[test.root].ToolchainImages[test.arch]; got != attemptID {
					t.Fatalf("disk toolchain after attempt binding = %q, want %q", got, attemptID)
				}
			}
		})
	}
}

func TestNVKEOSExecutionModesRejectCrossedEnvironments(t *testing.T) {
	repositoryRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repositoryRoot)
	for _, test := range []struct {
		name, definition, environment string
	}{
		{name: "ARM64 cannot select AMD64 host", definition: "definition-arm64-kvm.yaml",
			environment: "environment-local-linux-amd64-docker.yaml"},
		{name: "AMD64 cannot select ARM64 host", definition: "definition-amd64-kvm.yaml",
			environment: "environment-local-linux-arm64-docker.yaml"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join("definitions", "nvke-os")
			plan, err := CompileFiles(t.Context(), filepath.Join(root, test.definition),
				filepath.Join(root, test.environment), "test")
			assertCode(t, err, e.CodeEngineCapability)
			if plan != nil {
				t.Fatal("crossed execution mode returned a Plan")
			}
		})
	}
}
