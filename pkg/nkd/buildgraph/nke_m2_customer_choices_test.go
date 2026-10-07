// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"slices"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// Supplier URLs and digests below are deliberate contract fixtures. No download
// or operational compatibility with NKE's composition script is claimed.
func TestMaintainedM2CustomerMaterialReplacement(t *testing.T) {
	const root = "../../../definitions/nke-m2"
	base, err := catalog.LoadContext(t.Context(), catalog.Dir(root+"/os-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	native, err := catalog.LoadOverlayContext(t.Context(), root+"/native-toolchains.yaml")
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base, native)
	if err != nil {
		t.Fatal(err)
	}
	authored, err := definition.LoadDefinition(t.Context(), root+"/definition.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range authored.Outputs {
		if output.Format != definition.OutputDiskImage {
			continue
		}
		for _, supplier := range []string{"source", "prebuilt"} {
			t.Run(output.Platform+"/"+supplier, func(t *testing.T) {
				calico := cloneComponent(stack.Catalog.Components["nke-cni-calico"])
				replacement := catalog.ComponentReplacement{Component: &calico}
				wantKind := ir.KindBuild
				if supplier == "source" {
					calico.BuildCommand[2] = strings.Replace(calico.BuildCommand[2], "DEB_REVISION=1", "DEB_REVISION=2", 1)
				} else {
					wantKind = ir.KindPull
					replacement = catalog.ComponentReplacement{
						Version: "customer-1", SourceDateEpoch: calico.SourceDate,
						OutputType: calico.OutputType,
						Prebuilt: &catalog.Prebuilt{
							URL: "https://supplier.example/calico-{{.Arch}}.tar",
							Digests: map[string]string{
								"amd64": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
								"arm64": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
							},
							Evidence: &catalog.ExternalEvidence{None: true},
						},
					}
				}
				changed, err := catalog.ApplyStack(stack.Catalog, catalog.Overlay{
					APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind,
					Name: "customer-calico", RequiresCatalogDigest: stack.Digest,
					Components: map[string]catalog.ComponentReplacement{"nke-cni-calico": replacement},
				})
				if err != nil {
					t.Fatal(err)
				}
				selection := Selection{Components: output.Components, Platform: output.Platform, OutputFormat: output.Format}
				before := compileCustomerDisk(t, stack.Catalog, selection)
				after := compileCustomerDisk(t, changed.Catalog, selection)
				beforeDigest, err := before.Digest()
				if err != nil {
					t.Fatal(err)
				}
				afterDigest, err := after.Digest()
				if err != nil || afterDigest == beforeDigest {
					t.Fatalf("supplier replacement must change Build identity: %s/%s, %v", beforeDigest, afterDigest, err)
				}
				producer := ""
				for _, node := range after.Nodes {
					if node.Component == "nke-cni-calico" && node.Kind == wantKind {
						if producer != "" {
							t.Fatal("replacement selected more than one Calico producer")
						}
						producer = node.ID
					}
				}
				bindings := 0
				for _, node := range after.Nodes {
					if node.Component != "nke-image-inputs" || node.Kind != ir.KindBuild {
						continue
					}
					for _, material := range node.Materials {
						if material.Component == "nke-cni-calico" {
							bindings++
							if material.Producer != producer || !slices.Contains(node.Needs, producer) {
								t.Fatal("customer material lost its exact selected producer dependency")
							}
						}
					}
				}
				if producer == "" || bindings != 1 || changed.Catalog.Components["nke-agent"].Commit != stack.Catalog.Components["nke-agent"].Commit {
					t.Fatal("supplier replacement changed unrelated source or failed to select the supplier")
				}

				for _, invalid := range []struct {
					name string
					edit func(*Selection)
					code e.Code
				}{
					{"missing material", func(s *Selection) {
						s.Components = slices.DeleteFunc(s.Components, func(n string) bool { return n == "nke-cni-calico" })
					}, e.CodeInvalidCatalog},
					{"unknown supplier", func(s *Selection) { s.Components = append(s.Components, "customer-calico") }, e.CodeInvalidCatalog},
					{"wrong architecture", func(s *Selection) {
						if s.Platform == "linux/amd64" {
							s.Platform = "linux/arm64"
						} else {
							s.Platform = "linux/amd64"
						}
					}, e.CodeEngineCapability},
				} {
					t.Run(invalid.name, func(t *testing.T) {
						candidate := selection
						candidate.Components = slices.Clone(selection.Components)
						invalid.edit(&candidate)
						resolved, err := Resolve(changed.Catalog, candidate)
						if resolved != nil || code(err) != invalid.code {
							t.Fatalf("invalid customer selection returned %+v/%v, want %s", resolved, err, invalid.code)
						}
					})
				}
			})
		}
	}
}

func compileCustomerDisk(t *testing.T, cat *catalog.Catalog, selection Selection) *ir.Graph {
	t.Helper()
	resolved, err := Resolve(cat, selection)
	if err != nil {
		t.Fatal(err)
	}
	root, err := DiskRootName(resolved.Components)
	if err != nil {
		t.Fatal(err)
	}
	graph, err := CompileForHostPlatforms(resolved, map[string]execution.HostPlatform{root: execution.HostPlatform(selection.Platform)})
	if err != nil {
		t.Fatal(err)
	}
	if err := graph.Validate(); err != nil {
		t.Fatal(err)
	}
	return graph
}
