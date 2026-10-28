// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestConformanceCatalogInputsCompile(t *testing.T) {
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	const root = "inputs/definitions/kubernetes-conformance-arm64"
	plan, err := CompileFiles(t.Context(), root+"/definition.yaml", "inputs/environments/local-conformance-tools-arm64.yaml", "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, output := range plan.LifecycleOutputs {
		names = append(names, output.Name)
	}
	if !slices.Equal(names, []string{"busybox-image-arm64", "conformance-image-arm64", "hydrophone-arm64", "kubectl-arm64"}) || len(plan.Builds) != 4 {
		t.Fatalf("unexpected tool and image selection: %v builds=%d", names, len(plan.Builds))
	}
	if plan.Qualification != factory.TierStaticUnqualified || plan.Environment.Publication != nil || plan.Environment.Signing != nil {
		t.Fatal("local tool Definition must remain an unqualified build with no publication or signing")
	}
	patches := 0
	for _, build := range plan.Builds {
		if build.Platform != "linux/arm64" {
			t.Fatalf("unexpected native platform: %s", build.Platform)
		}
		for _, node := range build.Graph.Nodes {
			if node.Kind == ir.KindBuild && node.Network != ir.NetworkNone {
				t.Fatal("tool build must remain offline")
			}
			for _, patch := range node.Patches {
				if node.Component != "hydrophone" || patch.Name != "patches/bounded-download.patch" {
					t.Fatalf("unexpected source patch: %s %s", node.Component, patch.Name)
				}
				raw, err := os.ReadFile(filepath.Join("inputs/catalogs/kubernetes-conformance/hydrophone", patch.Name))
				if err != nil {
					t.Fatal(err)
				}
				if fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != patch.SHA256 || string(raw) != patch.Content {
					t.Fatal("compiled patch bytes do not match declared catalog digest and content")
				}
				patches++
			}
		}
	}
	if patches != 1 {
		t.Fatalf("expected one bounded Hydrophone source patch, got %d", patches)
	}
}
