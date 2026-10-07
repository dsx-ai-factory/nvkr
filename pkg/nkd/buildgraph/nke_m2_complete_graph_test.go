// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// This opt-in integration authenticates the source-owned chart pin set through
// actual Git acquisition. Static completeness is not runtime admission or a
// successful runtime build. Ordinary tests still require 34 declared outputs and
// independently load/resolve/compile all 24 native image rows.
func TestMaintainedM2All34OutputGraphs(t *testing.T) {
	t.Chdir("../../..")
	const root = "definitions/nke-m2"
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
	environmentPath := filepath.Join(root, "environment-github.yaml")
	environment, err := definition.LoadEnvironment(t.Context(), environmentPath)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise production's ordered overlay admission and complete cross-output
	// packaging graph, not a test-owned catalog loader that omits overlays.
	plan, err := factory.CompileDocuments(t.Context(), filepath.Join(root, "definition.yaml"), authored,
		environmentPath, environment, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := plan.Graph.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(plan.Builds) != len(authored.Outputs) || len(plan.LifecycleOutputs) != len(authored.Outputs) {
		t.Fatal("compiled Plan does not preserve every required output")
	}
	builds := make(map[string]factory.BuildPlan, len(plan.Builds))
	for _, build := range plan.Builds {
		builds[build.Name] = build
	}
	images := 0
	for _, output := range authored.Outputs {
		t.Run(output.Name, func(t *testing.T) {
			build, ok := builds[output.Name]
			if !ok || build.Graph.Platform != output.Platform {
				t.Fatal("named output or native platform missing from compiled Plan")
			}
			if err := build.Graph.Validate(); err != nil {
				t.Fatal(err)
			}
			if output.Catalog != nil && output.Catalog.Base == "./image-catalog" {
				pulls := 0
				for _, node := range build.Graph.Nodes {
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
