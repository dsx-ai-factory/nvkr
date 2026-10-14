// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"sigs.k8s.io/yaml"
)

// These are maintained-input regression tests, not special engine requirements
// for CAPNICo, NKTP, profiles or customer catalogs. Producers use generic routes.
func TestMaintainedM2AdditionalReleaseComponents(t *testing.T) {
	const root = "../../../definitions/nvke-m2"
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(root, "release-catalog")))
	if err != nil {
		t.Fatal(err)
	}
	authored, err := definition.LoadDefinition(t.Context(), filepath.Join(root, "definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	outputs := make(map[string]definition.DefinitionOutput, len(authored.Outputs))
	for _, output := range authored.Outputs {
		outputs[output.Name] = output
	}
	rows := []struct {
		output, component string
	}{
		{"capnico-controller", "capnico-controller"},
		{"controller-profiles", "nvke-nico-reference-profiles"},
		{"nktp", "nktp-chart"},
		{"capi-controller-image", "capi-controller-image"},
		{"capi-kubeadm-bootstrap-image", "capi-kubeadm-bootstrap-image"},
		{"capi-kubeadm-control-plane-image", "capi-kubeadm-control-plane-image"},
		{"cert-manager-cainjector-image", "cert-manager-cainjector-image"},
		{"cert-manager-controller-image", "cert-manager-controller-image"},
		{"cert-manager-webhook-image", "cert-manager-webhook-image"},
		{"nktp-collector-image", "nktp-collector-image"},
		{"nktp-kube-state-metrics-image", "nktp-kube-state-metrics-image"},
		{"nktp-kubectl-image", "nktp-kubectl-image"},
		{"nktp-lgtm-image", "nktp-lgtm-image"},
		{"nktp-metrics-server-image", "nktp-metrics-server-image"},
		{"nktp-node-exporter-image", "nktp-node-exporter-image"},
		{"nktp-operator-image", "nktp-operator-image"},
		{"nktp-target-allocator-image", "nktp-target-allocator-image"},
	}
	if len(cat.Components) != len(rows) {
		t.Fatal("additional catalog membership changed")
	}
	for _, row := range rows {
		t.Run(row.output, func(t *testing.T) {
			output, ok := outputs[row.output]
			if !ok || output.Catalog == nil || output.Catalog.Base != "./release-catalog" ||
				!slices.Equal(output.Components, []string{row.component}) {
				t.Fatal("maintained output does not select its exact producer")
			}
			if row.output == "nktp" {
				if output.Format != definition.OutputHelm || output.Platform != "linux/amd64" {
					t.Fatal("NKTP must be one architecture-independent Helm artifact")
				}
			} else if cat.Components[row.component].Prebuilt != nil {
				arm, ok := outputs[row.output+"-arm64"]
				if !ok || output.Format != definition.OutputOCI || output.Platform != "linux/amd64" ||
					arm.Format != definition.OutputOCI || arm.Platform != "linux/arm64" || arm.Catalog == nil ||
					arm.Catalog.Base != "./release-catalog" || !slices.Equal(arm.Components, output.Components) {
					t.Fatal("acquired image must retain separately named AMD64 and ARM64 outputs")
				}
			} else if output.Format != definition.OutputOCI ||
				!slices.Equal(output.Platforms, []string{"linux/amd64", "linux/arm64"}) {
				t.Fatal("image must retain both required native subjects")
			}
			for _, platform := range []string{"linux/amd64", "linux/arm64"} {
				t.Run(platform, func(t *testing.T) {
					resolved, err := Resolve(cat, Selection{
						Components: []string{row.component}, Platform: platform, OutputFormat: output.Format,
					})
					if err != nil {
						t.Fatal(err)
					}
					graph, err := Compile(resolved)
					if err != nil {
						t.Fatal(err)
					}
					if err := graph.Validate(); err != nil {
						t.Fatal(err)
					}
					component := cat.Components[row.component]
					if component.Prebuilt != nil {
						if component.Prebuilt.ImageParentDigest == "" || len(component.Prebuilt.Digests) != 2 {
							t.Fatal("acquired image must bind its parent and both native subjects")
						}
					} else if row.output == "nktp" {
						for _, node := range graph.Nodes {
							if node.Kind == ir.KindBuild && node.Network != ir.NetworkNone {
								t.Fatal("NKTP packaging must consume the verified closure offline")
							}
						}
					}
				})
			}
		})
	}
	provisioned := outputs["cluster-api-provider-provisioned"]
	if provisioned.Catalog == nil || provisioned.Catalog.Base != "../nvke-controller/catalog" ||
		!slices.Equal(provisioned.Components, []string{"cluster-api-provider-provisioned"}) ||
		!slices.Equal(provisioned.Platforms, []string{"linux/amd64", "linux/arm64"}) {
		t.Fatal("provisioned infrastructure provider must retain its existing independent producer")
	}

	baselineBytes, err := os.ReadFile(filepath.Join(root, "../nvke-controller/catalog/pins.lock.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var baseline catalog.PinLock
	if err := yaml.UnmarshalStrict(baselineBytes, &baseline); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{"nvke-content", "nvke-nktp"} {
		pin, err := cat.SourcePin(source)
		if err != nil || !reflect.DeepEqual(pin, baseline.Pins["nvke"]) {
			t.Fatalf("%s must match the maintained controller source, pin=%+v err=%v", source, pin, err)
		}
	}
	profilesCommand := strings.Join(cat.Components["nvke-nico-reference-profiles"].BuildCommand, " ")
	if !strings.Contains(profilesCommand, "nico-vms-cp.yaml") || !strings.Contains(profilesCommand, "nico-vms-worker.yaml") ||
		strings.Contains(profilesCommand, "osProfiles") || strings.Contains(profilesCommand, "gofail") ||
		strings.Contains(profilesCommand, "profiles/example") || len(cat.Components["nvke-nico-reference-profiles"].Patches) != 0 {
		t.Fatal("reference bundle must select only NICO config profiles and source-owned packaging without patches")
	}
}
