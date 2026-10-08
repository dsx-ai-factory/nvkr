// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

func TestResolvedImageDependenciesRetainParentAndNativeSubjects(t *testing.T) {
	parent := "sha256:" + strings.Repeat("a", 64)
	child := "sha256:" + strings.Repeat("b", 64)
	for _, arch := range []string{"amd64", "arm64"} {
		t.Run(arch, func(t *testing.T) {
			node := ir.Node{ID: "pull-tool", Kind: ir.KindPull, Component: "tool", Artifact: "registry.example/tool@" + parent,
				ImageParentDigest: parent, Digest: child, Platform: "linux/" + arch, Network: ir.NetworkDefault, OutputType: "oci-image", Version: "1.0.0"}
			graph := &ir.Graph{SchemaVersion: ir.SchemaVersion, Platform: node.Platform, Nodes: []ir.Node{node, {ID: "verify", Kind: ir.KindVerify, Needs: []string{node.ID}}}}
			if err := graph.Validate(); err != nil {
				t.Fatal(err)
			}
			deps := resolvedDependencies(&Plan{Builds: []BuildPlan{{Graph: graph}}})
			subjects := map[string]string{}
			for _, dep := range deps {
				subjects[dep.URI] = "sha256:" + dep.Digest["sha256"]
			}
			if len(subjects) != 2 || subjects[node.Artifact] != parent || subjects["registry.example/tool@"+child] != child {
				t.Fatalf("parent/native provenance conflated: %+v", deps)
			}
		})
	}
}
