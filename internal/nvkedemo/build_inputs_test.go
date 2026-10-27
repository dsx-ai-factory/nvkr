// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"maps"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
)

func TestLocalKVMInputsPreserveSelectedOutputsAndSources(t *testing.T) {
	repo := filepath.Join("..", "..")
	for _, network := range []string{"nvidia", "upstream"} {
		t.Run(network, func(t *testing.T) {
			path := filepath.Join(repo, "inputs/definitions/demo/arm64-"+network)
			before, err := definition.LoadDefinition(t.Context(), path+"-tcg.json")
			if err != nil {
				t.Fatal(err)
			}
			after, err := definition.LoadDefinition(t.Context(), path+"-local-kvm.json")
			if err != nil {
				t.Fatal(err)
			}
			if len(before.Outputs) != len(after.Outputs) || !reflect.DeepEqual(before.Catalog, after.Catalog) {
				t.Fatal("KVM selection changed the selected output set or default catalog")
			}
			// The native NVIDIA selection deliberately moves the NVKE closure
			// together. Historical TCG inputs retain their original source scope.
			modern := map[string]definition.Catalog{
				"cluster-api-provider-provisioned-arm64": {Base: "../../catalogs/nvke/native-site-arm64"},
				"controller-chart":                       {Base: "../../catalogs/nvke/native-site-arm64"},
				"controller-profiles":                    {Base: "../../catalogs/controller-profiles/native-nvidia-arm64"},
				"cwe":                                    {Base: "../../catalogs/nvke/native-site-arm64"},
				"nktp":                                   {Base: "../../catalogs/nvke/native-site-arm64"},
				"nvkectl":                                {Base: "../../catalogs/nvke/native-site-arm64"},
				"nvke-node-image-arm64":                  {Base: "../../catalogs/node-images/release", Overlays: []string{"../../catalogs/node-images/variants/native-nvidia.json", "../../catalogs/node-images/variants/native-local-kvm.json"}},
			}
			for _, name := range []string{"postgresql", "spire", "spire-crds", "temporal"} {
				modern[name] = definition.Catalog{Base: "../../catalogs/nvke/native-site-arm64"}
			}
			for _, name := range []string{"csi-node-driver-registrar", "postgresql", "spiffe-csi", "spire-agent", "spire-bash", "spire-controller-manager", "spire-oidc", "spire-server", "spire-tpm-server", "temporal-admin-tools", "temporal-server", "temporal-ui"} {
				modern[name+"-image-arm64"] = definition.Catalog{Base: "../../catalogs/runtime-dependencies"}
			}
			for i, original := range before.Outputs {
				selected := after.Outputs[i]
				if network == "nvidia" {
					if expected, changed := modern[original.Name]; changed {
						if selected.Catalog == nil || !reflect.DeepEqual(*selected.Catalog, expected) {
							t.Fatalf("%s lost its selected native catalog", original.Name)
						}
						delete(modern, original.Name)
						selected.Catalog = original.Catalog
					}
					if !reflect.DeepEqual(original, selected) {
						t.Fatalf("changed named output contract %s", original.Name)
					}
					continue
				}
				if original.Format != "disk-image" {
					if !reflect.DeepEqual(original, selected) {
						t.Fatalf("changed non-disk output %s", original.Name)
					}
					continue
				}
				if selected.Catalog == nil || original.Catalog == nil || selected.Catalog.Base != original.Catalog.Base || len(selected.Catalog.Overlays) != len(original.Catalog.Overlays)+1 {
					t.Fatal("KVM disk must extend exactly the selected catalog stack")
				}
				for j, overlay := range original.Catalog.Overlays {
					if selected.Catalog.Overlays[j] != overlay {
						t.Fatal("KVM replaced or reordered a selected overlay")
					}
				}
				base, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(filepath.Dir(path), original.Catalog.Base)))
				if err != nil {
					t.Fatal(err)
				}
				var overlays []catalog.Overlay
				for _, name := range selected.Catalog.Overlays {
					overlay, err := catalog.LoadOverlayContext(t.Context(), filepath.Join(filepath.Dir(path), name))
					if err != nil {
						t.Fatal(err)
					}
					overlays = append(overlays, overlay)
				}
				previous, err := catalog.ApplyStack(base, overlays[:len(overlays)-1]...)
				if err != nil {
					t.Fatal(err)
				}
				resolved, err := catalog.ApplyStack(base, overlays...)
				if err != nil {
					t.Fatal(err)
				}
				last := overlays[len(overlays)-1]
				if len(last.Components) != 1 || len(last.ToolchainSubstitutions) != 0 {
					t.Fatal("KVM overlay changes more than the disk producer")
				}
				for name, expected := range previous.Catalog.Components {
					if expected.BuilderType == catalog.BuilderVMImage && name == "nvke-node-image" {
						expected.BuildRequirements = execution.Clone(expected.BuildRequirements)
						expected.BuildRequirements.Capabilities = []execution.CapabilityRequirement{{Name: execution.CapabilityKVM, Policy: execution.CapabilityRequired}}
						packer := *expected.Packer
						packer.Variables = maps.Clone(packer.Variables)
						packer.Variables["cpu_model"], packer.Variables["ssh_timeout"] = "host", "60m"
						expected.Packer = &packer
					}
					if !reflect.DeepEqual(expected, resolved.Catalog.Components[name]) {
						t.Fatalf("KVM changed source, material, network or producer %s", name)
					}
				}
				// The named disk and all its members retain their existing contract.
				selected.Catalog = original.Catalog
				if !reflect.DeepEqual(original, selected) {
					t.Fatal("KVM changed the named disk output contract")
				}
			}
			if network == "nvidia" && len(modern) != 0 {
				t.Fatalf("missing native outputs: %v", modern)
			}
		})
	}
}

func TestLocalKVMEnvironmentPreservesBuildSettings(t *testing.T) {
	root := filepath.Join("..", "..", "inputs/environments")
	before, err := definition.LoadEnvironment(t.Context(), filepath.Join(root, "local-arm64-tcg.json"))
	if err != nil {
		t.Fatal(err)
	}
	after, err := definition.LoadEnvironment(t.Context(), filepath.Join(root, "local-arm64-kvm.json"))
	if err != nil {
		t.Fatal(err)
	}
	if before.UtilityContainer != after.UtilityContainer || before.UtilityPlatform != after.UtilityPlatform || !reflect.DeepEqual(before.Runtime, after.Runtime) || len(after.ExecutionClasses) != 1 {
		t.Fatal("KVM changed the admitted build runtime or utility inputs")
	}
	if after.ExecutionClasses[0].HostPlatform != before.ExecutionClasses[0].HostPlatform || !reflect.DeepEqual(after.ExecutionClasses[0].Resources, before.ExecutionClasses[0].Resources) || after.ExecutionClasses[0].MaxTimeoutSeconds != before.ExecutionClasses[0].MaxTimeoutSeconds {
		t.Fatal("KVM changed the admitted host or capacity budget")
	}
	if !reflect.DeepEqual(after.ExecutionClasses[0].Capabilities, []execution.CapabilityName{execution.CapabilityKVM}) {
		t.Fatal("KVM capability is not explicitly advertised")
	}
}

func TestPreparedInvocationRejectsTCGSubstitution(t *testing.T) {
	for _, network := range []string{"ovn", "upstream-ovn"} {
		for _, field := range []string{"definition", "environment"} {
			t.Run(network+"/"+field, func(t *testing.T) {
				cfg := BuildConfig{Repository: privateTestDir(t), AttemptPrefix: "fresh", Jobs: 4, Network: network}
				args := preparedInvocationFixture(cfg)
				if field == "environment" {
					args[6] = "inputs/environments/local-arm64-tcg.json"
				} else {
					args[4] = "inputs/definitions/demo/arm64-nvidia-tcg.json"
					if network == "upstream-ovn" {
						args[4] = "inputs/definitions/demo/arm64-upstream-tcg.json"
					}
				}
				writeInvocationFixture(t, cfg, args)
				if got, err := preparedBuildInvocation(cfg); err == nil || got != nil {
					t.Fatal("KVM build accepted a TCG Environment")
				}
			})
		}
	}
}
