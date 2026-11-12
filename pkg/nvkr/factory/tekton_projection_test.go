// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"reflect"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestTektonProjectionPreservesSharedOperationMembership(t *testing.T) {
	plan := githubTestPlan(t)
	before, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	projection, err := projectTektonExecution(t.Context(), plan, plan.Environment.UtilityPlatform)
	if err != nil {
		t.Fatal(err)
	}
	github, owners, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(owners, projection.owners) || len(github) != len(projection.stages) {
		t.Fatal("Tekton changed logical stage membership")
	}
	_, artifacts := planIndex(plan)
	seen := make(map[string]bool)
	for i, stage := range projection.stages {
		if stage.ID != github[i].ID || stage.executionCondition() != github[i].executionCondition() {
			t.Fatal("Tekton changed logical operation scheduling policy")
		}
		if !reflect.DeepEqual(stage.inputs(artifacts, owners), github[i].inputs(artifacts, owners)) {
			t.Fatal("Tekton changed transported inputs")
		}
		for _, node := range stage.Nodes {
			if seen[node.ID] {
				t.Fatalf("operation %s occurs more than once", node.ID)
			}
			seen[node.ID] = true
			if node.RunsOn != "tekton-native" {
				t.Fatal("Tekton retained a GitHub runner route")
			}
		}
	}
	if len(seen) != len(plan.Graph.Nodes) {
		t.Fatal("Tekton omitted required operations")
	}
	after, err := plan.Digest()
	if err != nil || before != after {
		t.Fatal("provider projection changed Plan identity")
	}
}

func TestTektonProjectionRefusesUnsupportedSelection(t *testing.T) {
	for _, tc := range []struct {
		name, platform string
		missing        bool
		cancel         bool
		code           e.Code
	}{
		{"nil Plan", "linux/amd64", true, false, e.CodeUsage},
		{"unspecified platform", "", false, false, e.CodeEngineCapability},
		{"foreign platform", "linux/arm64", false, false, e.CodeEngineCapability},
		{"non-Linux", "darwin/arm64", false, false, e.CodeEngineCapability},
		{"canceled", "linux/amd64", false, true, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := githubTestPlan(t)
			if tc.missing {
				plan = nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			projection, err := projectTektonExecution(ctx, plan, tc.platform)
			assertFactoryErrorCode(t, err, tc.code)
			if projection != nil {
				t.Fatal("unsupported selection returned partial scheduling")
			}
		})
	}
}
