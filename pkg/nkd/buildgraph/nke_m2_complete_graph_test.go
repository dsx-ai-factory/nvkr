// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/execution"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// This opt-in integration authenticates the source-owned chart pin set through
// actual Git acquisition. Static completeness is not Environment admission or a
// successful runtime build. Ordinary tests still require 34 declared outputs and
// independently load/resolve/compile all 24 native image rows.
func TestMaintainedM2All34OutputGraphs(t *testing.T) {
	const root = "../../../definitions/nke-m2"
	authored, err := definition.LoadDefinition(t.Context(), filepath.Join(root, "definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(authored.Outputs) != 34 {
		t.Fatal("required output membership changed")
	}
	declaredImages := 0
	for _, output := range authored.Outputs {
		if output.Catalog != nil && output.Catalog.Base == "./image-catalog" {
			declaredImages++
		}
	}
	if declaredImages != 24 {
		t.Fatalf("declared %d required image rows, want 24", declaredImages)
	}
	if os.Getenv("NKD_TEST_M2_ALL_OUTPUT_GRAPHS") != "1" {
		t.Skip("explicit pinned-source Git integration was not requested; native image graphs are tested separately")
	}
	images := 0
	for _, output := range authored.Outputs {
		t.Run(output.Name, func(t *testing.T) {
			selection := authored.Catalog
			if output.Catalog != nil {
				selection = *output.Catalog
			}
			if len(selection.Overlays) != 0 {
				t.Fatal("fixture must use actual owning stack admission for new overlays")
			}
			cat, err := catalog.LoadContext(t.Context(), catalog.Dir(filepath.Join(root, selection.Base)))
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := Resolve(cat, Selection{Components: output.Components, Platform: output.Platform, OutputFormat: output.Format})
			if err != nil {
				t.Fatal(err)
			}
			hosts := map[string]execution.HostPlatform{}
			if output.Format == definition.OutputDiskImage {
				name, err := DiskRootName(resolved.Components)
				if err != nil {
					t.Fatal(err)
				}
				hosts[name] = execution.HostPlatform(output.Platform)
			}
			graph, err := CompileForHostPlatforms(resolved, hosts)
			if err != nil {
				t.Fatal(err)
			}
			if err := graph.Validate(); err != nil {
				t.Fatal(err)
			}
			if graph.Platform != output.Platform {
				t.Fatal("output platform changed")
			}
			if selection.Base == "./image-catalog" {
				pulls := 0
				for _, node := range graph.Nodes {
					if node.Kind == ir.KindPull && node.OutputType == "oci-image" {
						pulls++
					}
				}
				if pulls != 1 {
					t.Fatalf("image output has %d acquired producers", pulls)
				}
				images++
			}
		})
	}
	if images != 24 {
		t.Fatalf("compiled %d required image rows, want 24", images)
	}
}
