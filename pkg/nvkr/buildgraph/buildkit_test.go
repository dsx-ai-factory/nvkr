// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph

import (
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestCompileRootlessBuildKitRecordsNetworkAndBuilder(t *testing.T) {
	catalog := nixCatalog(ir.BuilderBuildKit)
	component := catalog.Components["tool"]
	component.OutputType = "oci-image"
	component.BuildCommand = []string{"buildctl-daemonless.sh", "build", "--frontend", "dockerfile.v0"}
	catalog.Components["tool"] = component
	resolved, err := Resolve(catalog, Selection{Components: []string{"tool"}, Platform: "linux/amd64"})
	if err != nil {
		t.Fatal(err)
	}
	graph := mustCompile(t, resolved)
	for _, node := range graph.Nodes {
		if node.Kind != ir.KindBuild {
			continue
		}
		if node.BuilderType != ir.BuilderBuildKit || node.Network != ir.NetworkDefault || node.OutputType != "oci-image" {
			t.Fatalf("incorrect wrapped build: %#v", node)
		}
		changed := node
		changed.Network = ir.NetworkNone
		if err := changed.Validate(); err == nil {
			t.Fatal("wrapped builder accepted a false hermetic declaration")
		}
		return
	}
	t.Fatal("no image build node")
}
