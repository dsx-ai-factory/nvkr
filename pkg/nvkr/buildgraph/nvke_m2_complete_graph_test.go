// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// This opt-in integration authenticates the source-owned chart pin set through
// actual Git acquisition. Static completeness is not runtime admission or a
// successful runtime build. Ordinary tests still require 37 declared outputs and
// independently load/resolve/compile all 24 native image rows.
func TestMaintainedM2AllOutputGraphs(t *testing.T) {
	t.Chdir("../../..")
	const root = "definitions/nvke-m2"
	authored, err := definition.LoadDefinition(t.Context(), filepath.Join(root, "definition.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(authored.Outputs) != 37 {
		t.Fatal("required output membership changed")
	}
	declaredImages := 0
	disks := map[string]string{
		"nvke-node-fde-amd64": "linux/amd64", "nvke-node-fde-arm64": "linux/arm64",
		"nvke-node-image-amd64": "linux/amd64", "nvke-node-image-arm64": "linux/arm64",
	}
	declaredDisks := 0
	for _, output := range authored.Outputs {
		if output.Catalog != nil && output.Catalog.Base == "./image-catalog" {
			declaredImages++
		}
		if output.Format == definition.OutputDiskImage {
			if disks[output.Name] != output.Platform {
				t.Fatalf("unexpected disk output %s/%s", output.Name, output.Platform)
			}
			declaredDisks++
		}
	}
	if declaredImages != 24 {
		t.Fatalf("declared %d required image rows, want 24", declaredImages)
	}
	if declaredDisks != len(disks) {
		t.Fatal("both native architectures must retain FDE and non-FDE outputs")
	}
	if os.Getenv("NVKR_TEST_M2_ALL_OUTPUT_GRAPHS") != "1" {
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
	if len(plan.Builds) != 38 || len(plan.LifecycleOutputs) != len(authored.Outputs) {
		t.Fatal("compiled Plan does not preserve every required output")
	}
	builds := make(map[string]factory.BuildPlan, len(plan.Builds))
	for _, build := range plan.Builds {
		if _, duplicate := builds[build.Name]; duplicate {
			t.Fatalf("duplicate compiled Build %q", build.Name)
		}
		builds[build.Name] = build
	}
	images := 0
	graphs := 0
	for _, output := range authored.Outputs {
		t.Run(output.Name, func(t *testing.T) {
			if len(output.Platforms) != 0 {
				for _, platform := range output.Platforms {
					build, ok := builds[output.Name+"/"+platform]
					if !ok || build.Graph.Platform != platform {
						t.Fatalf("aggregate native build missing: %s", platform)
					}
					if err := build.Graph.Validate(); err != nil {
						t.Fatal(err)
					}
					graphs++
				}
				return
			}
			build, ok := builds[output.Name]
			if !ok || build.Graph.Platform != output.Platform {
				t.Fatal("named output or native platform missing from compiled Plan")
			}
			if err := build.Graph.Validate(); err != nil {
				t.Fatal(err)
			}
			graphs++
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
	if graphs != 38 {
		t.Fatalf("validated %d required build graphs, want 38", graphs)
	}
}
