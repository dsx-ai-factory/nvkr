// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/testutil"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// Compile the maintained selections through the owning boundary: a modern CLI
// paired with legacy disk packages or a different controller cannot bootstrap.
func TestNativeNVIDIADefinitionsAlignRenamedSourceContracts(t *testing.T) {
	testutil.RequireExtended(t)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// Compare compiled source identities with the owning locks. Pin updates
	// remain data changes without another revision list in Go.
	locks := make(map[string]map[string]catalog.Pin)
	for _, dir := range []string{"node-images/release", "nvke/native-site-arm64", "controller-profiles/native-nvidia-arm64", "ovn-nvidia"} {
		lock, err := catalog.LoadLockContext(t.Context(), filepath.Join(repo, "inputs/catalogs", dir))
		if err != nil {
			t.Fatal(err)
		}
		locks[dir] = lock.Pins
	}
	osPins, sitePins := locks["node-images/release"], locks["nvke/native-site-arm64"]
	profilePins, ovnPins := locks["controller-profiles/native-nvidia-arm64"], locks["ovn-nvidia"]
	for _, conformance := range []bool{false, true} {
		name := "demo"
		if conformance {
			name = "conformance"
		}
		t.Run(name, func(t *testing.T) {
			dpath, epath, err := LocalArtifactInputsFor("ovn", conformance, "linux")
			if err != nil {
				t.Fatal(err)
			}
			plan, _, err := factory.CaptureCompileFiles(t.Context(), repo, dpath, epath, "test")
			if err != nil {
				t.Fatal(err)
			}
			definition, err := authored.LoadDefinition(t.Context(), filepath.Join(repo, dpath))
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.LifecycleOutputs) != len(definition.Outputs) {
				t.Fatalf("outputs=%d, want%d", len(plan.LifecycleOutputs), len(definition.Outputs))
			}
			rows := []struct {
				output, component string
				pin               catalog.Pin
			}{
				{"nvke-node-image-arm64", "nvke-agent", osPins["nvke-os"]},
				{"nvke-node-image-arm64", "nvke-kubeadm-distro", osPins["nvke-kubeadm-distro-source"]},
				{"nvke-node-image-arm64", "nvke-cni-calico", osPins["nvke-cni-calico-source"]},
				{"nvke-node-image-arm64", "nvke-node-image", osPins["nvke-os"]},
				{"nvke-node-image-arm64", "nvke-image-inputs", osPins["nvke-os"]},
				{"nvke-node-image-arm64", "nvke-spire-agent", osPins["nvke-spire-agent-source"]},
				{"nvke-node-image-arm64", "nvke-cni-ovn", ovnPins["ovn-kubernetes"]},
				{"nvke-node-image-arm64", "ovn-kube-ubuntu", ovnPins["ovn-kubernetes"]},
				{"nvke-node-image-arm64", "ovn-kubernetes-chart", ovnPins["ovn-kubernetes"]},
				{"controller-chart", "nvke-controller-chart", sitePins["nvke-controller-helm"]},
				{"controller-profiles", "nvke-local-profiles", profilePins["nvke-profiles"]},
				{"cwe", "nvke-controller", sitePins["nvke-controller"]},
				{"nvkectl", "nvkectl", sitePins["nvke-cli"]},
				{"cluster-api-provider-provisioned-arm64", "cluster-api-provider-provisioned", sitePins["nvke-controller"]},
				{"ovn-image-arm64", "ovn-kube-ubuntu", ovnPins["ovn-kubernetes"]},
				{"ovn-chart-arm64", "ovn-kubernetes-chart", ovnPins["ovn-kubernetes"]},
			}
			for _, row := range rows {
				matches := 0
				for _, build := range plan.Builds {
					if build.Name != row.output {
						continue
					}
					for _, c := range build.ResolvedComponents {
						if c.Name != row.component {
							continue
						}
						matches++
						if c.Commit != row.pin.Commit || c.TreeHash != row.pin.TreeHash || c.SourceDate != row.pin.SourceDateEpoch {
							t.Fatalf("%s/%s source identity differs from its selected catalog lock", row.output, row.component)
						}
						if (c.Name == "nvke-node-image" || c.Name == "nvke-local-profiles") && len(c.Patches) != 0 {
							t.Fatalf("%s must consume unchanged upstream source", c.Name)
						}
						if c.Name == "nvke-node-image" {
							if c.Packer == nil || c.Packer.BuildName != "nvke-node-non-fde.qemu.nvke-non-fde" || c.ArtifactPath != "non-fde/nvke-node.qcow2" {
								t.Fatal("disk producer does not match renamed Packer source")
							}
						}
					}
				}
				if matches != 1 {
					t.Fatalf("%s/%s matched %d times", row.output, row.component, matches)
				}
			}
		})
	}
}

// Read only authored selections and lock records. Full compilation also
// authenticates source-derived chart pins and belongs to the extended suite.
func TestNativeNVIDIASelectionsAndPinsOffline(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	osLock, err := catalog.LoadLockContext(t.Context(), filepath.Join(repo, "inputs/catalogs/node-images/release"))
	if err != nil {
		t.Fatal(err)
	}
	nvke := osLock.Pins["nvke-os"]
	for _, tc := range []struct {
		dir     string
		sources []string
	}{
		{"node-images/release", []string{"nvke-os", "nvke-cni-calico-source", "nvke-kubeadm-distro-source", "nvke-spire-agent-source"}},
		{"nvke/native-site-arm64", []string{"nvke-cli", "nvke-controller", "nvke-controller-helm", "nvke-nktp-helm", "nvke-deploy"}},
		{"controller-profiles/native-nvidia-arm64", []string{"nvke-profiles"}},
		{"tools/native", []string{"nvke-cli", "nvke-deploy", "nvke-nicosim"}},
		{"runtime-dependencies", []string{"nvke-deployment-selection"}},
	} {
		lock, err := catalog.LoadLockContext(t.Context(), filepath.Join(repo, "inputs/catalogs", tc.dir))
		if err != nil {
			t.Fatal(err)
		}
		for _, source := range tc.sources {
			if pin, ok := lock.Pins[source]; !ok || pin.Commit == "" || pin.TreeHash == "" || pin.SourceDateEpoch <= 0 {
				t.Fatalf("%s/%s lost selected NVKE source", tc.dir, source)
			} else if pin.Commit != nvke.Commit || pin.TreeHash != nvke.TreeHash || pin.SourceDateEpoch != nvke.SourceDateEpoch {
				t.Fatalf("%s/%s differs from the selected native OS source identity", tc.dir, source)
			}
		}
	}
	for _, conformance := range []bool{false, true} {
		path, _, err := LocalArtifactInputsFor("ovn", conformance, "linux")
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(repo, path)
		doc, err := authored.LoadDefinition(t.Context(), file)
		if err != nil {
			t.Fatal(err)
		}
		expected := map[string]string{"nvke-node-image-arm64": "inputs/catalogs/node-images/release", "cwe": "inputs/catalogs/nvke/native-site-arm64", "nvkectl": "inputs/catalogs/nvke/native-site-arm64", "controller-chart": "inputs/catalogs/nvke/native-site-arm64", "controller-profiles": "inputs/catalogs/controller-profiles/native-nvidia-arm64"}
		hostPlatforms := map[string]string{}
		if conformance {
			expected["conformance-image-arm64"] = "definitions/kubernetes-conformance-arm64/demo-1.36/image-catalog"
			for _, host := range []struct{ suffix, platform string }{{"arm64", "linux/arm64"}, {"darwin-arm64", "darwin/arm64"}} {
				expected["hydrophone-"+host.suffix] = "definitions/kubernetes-conformance-arm64/hydrophone-catalog"
				expected["kubectl-"+host.suffix] = "definitions/kubernetes-conformance-arm64/kubectl-catalog"
				hostPlatforms["hydrophone-"+host.suffix] = host.platform
				hostPlatforms["kubectl-"+host.suffix] = host.platform
			}
		}
		for _, output := range doc.Outputs {
			dir, selected := expected[output.Name]
			if !selected {
				continue
			}
			if output.Catalog == nil || filepath.Clean(filepath.Join(filepath.Dir(file), output.Catalog.Base)) != filepath.Join(repo, dir) {
				t.Fatalf("%s lost its native catalog selection", output.Name)
			}
			if platform, host := hostPlatforms[output.Name]; host && output.Platform != platform {
				t.Fatalf("%s platform=%s, want %s", output.Name, output.Platform, platform)
			}
			delete(expected, output.Name)
		}
		if len(expected) != 0 {
			t.Fatalf("missing native outputs: %v", expected)
		}
	}
}

// Loading the selected native catalogs and overlay requires no derived private
// source. Keep accelerator, material and unchanged-source checks in default CI.
func TestNativeNVIDIAProducerRequirementsOffline(t *testing.T) {
	root := filepath.Join("..", "..", "inputs", "catalogs")
	base, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(root, "node-images/release")))
	if err != nil {
		t.Fatal(err)
	}
	var overlays []catalog.Overlay
	for _, name := range []string{"native-nvidia.json", "native-local-kvm.json"} {
		overlay, err := catalog.LoadOverlayContext(t.Context(), filepath.Join(root, "node-images/variants", name))
		if err != nil {
			t.Fatal(err)
		}
		overlays = append(overlays, overlay)
	}
	stack, err := catalog.ApplyStack(base, overlays...)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(stack.Catalog.Components["ovn-kube-ubuntu"].BuildCommand, "\n"), "build-arg:BUILDER_IMAGE=") {
		t.Fatal("native fork compiler must not carry an unused builder override")
	}
	disk := stack.Catalog.Components["nvke-node-image"]
	if disk.BuilderType != catalog.BuilderVMImage || disk.Packer == nil || disk.BuildRequirements == nil {
		t.Fatal("native disk lost its VM/Packer execution contract")
	}
	if !reflect.DeepEqual(disk.BuildRequirements.Capabilities, []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: execution.CapabilityRequired}}) ||
		!reflect.DeepEqual(disk.BuildRequirements.AllowedHostPlatforms, []execution.HostPlatform{execution.HostPlatformLinuxARM64}) {
		t.Fatal("native Linux disk no longer requires ARM64 KVM")
	}
	if disk.Packer.BuildName != "nvke-node-non-fde.qemu.nvke-non-fde" || disk.Packer.Variables["cpu_model"] != "host" || disk.Packer.Variables["ssh_timeout"] != "60m" || disk.ArtifactPath != "non-fde/nvke-node.qcow2" {
		t.Fatal("native disk lost its selected Packer build, native CPU or artifact path")
	}
	if disk.Packer.Variables["ubuntu_series"] != "26.04" {
		t.Fatal("native disk must select Ubuntu 26.04 to match its installer ISO")
	}
	if !reflect.DeepEqual(disk.Materials, map[string]string{"image-inputs": "nvke-image-inputs", "installer-iso": "nvke-patched-installer-iso"}) {
		t.Fatal("native disk material selection changed")
	}
	profiles, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(root, "controller-profiles/native-nvidia-arm64")))
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range []catalog.Component{disk, profiles.Components["nvke-local-profiles"]} {
		if len(component.Patches) != 0 {
			t.Fatal("native source producer must not apply source patches")
		}
	}
}
