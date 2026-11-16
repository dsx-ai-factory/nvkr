// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package buildgraph_test

import (
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// executeSourceFixture runs the fetches and independent source builds used by the
// opt-in OS/OVN fixtures. Factory remains the production whole-Run scheduler.
func executeSourceFixture(t *testing.T, graph *ir.Graph, options pipeline.RunOptions) []pipeline.ArtifactResult {
	t.Helper()
	if err := graph.Validate(); err != nil {
		t.Fatal(err)
	}
	session, err := pipeline.NewNodeSession(options.Ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	options.Launch = true
	fetched := map[string]bool{}
	for _, node := range graph.Nodes {
		if node.Kind == ir.KindFetch {
			if len(node.Needs) != 0 {
				t.Fatal("source fixture fetch has dependencies")
			}
			if _, err := session.ExecNode(node, options); err != nil {
				t.Fatal(err)
			}
			fetched[node.ID] = true
		}
	}
	var artifacts []pipeline.ArtifactResult
	for _, node := range graph.Nodes {
		switch node.Kind {
		case ir.KindFetch, ir.KindVerify:
			continue
		case ir.KindBuild:
			for _, dependency := range node.Needs {
				if !fetched[dependency] {
					t.Fatalf("source fixture build %s requires non-fetch dependency %s", node.ID, dependency)
				}
			}
			artifact, err := session.ExecNode(node, options)
			if err != nil {
				t.Fatal(err)
			}
			if artifact == nil {
				t.Fatal("source fixture build returned no artifact")
			}
			artifacts = append(artifacts, *artifact)
		default:
			t.Fatalf("unsupported source fixture node %s", node.Kind)
		}
	}
	return artifacts
}
