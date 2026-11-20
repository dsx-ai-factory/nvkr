// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package factory

import (
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestIndependentSourceFetchDoesNotSelectGitHubSeed(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../.github/nvkr-dev/plan.json")
	if err != nil {
		t.Fatal(err)
	}
	ordered, _, err := githubSchedulingProjection(plan)
	if err != nil {
		t.Fatal(err)
	}
	seeds, err := githubSourceSeeds(plan, ordered)
	if err != nil {
		t.Fatal(err)
	}
	var selected Node
	for _, node := range ordered {
		if _, ok := seeds[node.ID]; ok {
			selected = node
			break
		}
	}
	if selected.ID == "" {
		t.Fatal("fixture needs a shared source")
	}
	seed, err := githubSourceSeed(plan, selected.ID)
	if err != nil || seed == nil {
		t.Fatalf("GitHub seed=%v, err=%v", seed, err)
	}
	t.Chdir(t.TempDir())
	called := false
	// Tekton uses this independent entry point and restores no GitHub seed.
	err = executeSourceFetch(t.Context(), plan, selected.ID, sourceFetchTestEnvironment(plan, selected), nil, nil,
		func(_ ir.Node, options pipeline.RunOptions) (*pipeline.ArtifactResult, error) {
			called = true
			if options.SourceSeed != nil {
				t.Fatal("independent/Tekton fetch received GitHub seed")
			}
			return nil, nil
		})
	if err != nil || !called {
		t.Fatalf("independent fetch called=%t, err=%v", called, err)
	}
}
