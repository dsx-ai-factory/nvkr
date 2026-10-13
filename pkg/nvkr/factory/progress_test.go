// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"slices"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestProgressProjectsIndexMembersWithoutSharedToolSiblingReuse(t *testing.T) {
	plan := &Plan{Definition: authored.Definition{Outputs: []authored.DefinitionOutput{{Name: "images"}, {Name: "cli", Platform: "linux/amd64"}}},
		LifecycleOutputs: []LifecycleOutputPlan{
			{Name: "images", Kind: "oci", ArtifactID: "index", ImageIndex: &ImageIndexPlan{Children: []ImageIndexChildPlan{{BuildTarget: "images/linux/amd64"}, {BuildTarget: "images/linux/arm64"}}}},
			{Name: "cli", Kind: "executable", ArtifactID: "cli-payload"},
		}, Graph: ArtifactGraph{Nodes: []Node{
			{ID: "tool", Kind: NodeAcquire, Outputs: []string{"tool-payload"}},
			{ID: "amd64", Kind: NodeBuild, BuildTarget: "images/linux/amd64", Inputs: []string{"tool-payload"}, Outputs: []string{"amd64-payload"}},
			{ID: "arm64", Kind: NodeBuild, BuildTarget: "images/linux/arm64", Inputs: []string{"tool-payload"}, Outputs: []string{"arm64-payload"}},
			{ID: "index", Kind: NodePackage, Inputs: []string{"amd64-payload", "arm64-payload"}, Outputs: []string{"index"}},
			{ID: "cli", Kind: NodeBuild, BuildTarget: "cli", Inputs: []string{"tool-payload"}, Outputs: []string{"cli-payload"}},
			{ID: "publish-cli", Kind: NodePublish, Inputs: []string{"cli-payload", "tool-payload"}, Outputs: []string{"cli-reference"}},
			{ID: "assemble", Kind: NodeAssembleRelease, Inputs: []string{"index", "cli-reference"}},
		}, Artifacts: []Artifact{
			{ID: "tool-payload", Kind: "pipeline-tool", Producer: "tool"}, {ID: "amd64-payload", Producer: "amd64"},
			{ID: "arm64-payload", Producer: "arm64"}, {ID: "index", Producer: "index"}, {ID: "cli-payload", Producer: "cli"}, {ID: "cli-reference", Producer: "publish-cli"},
		}}}
	got := projectProgress(plan, "attempt", "digest")
	want := map[string][]string{"tool": {"cli", "images"}, "amd64": {"images"}, "arm64": {"images"}, "index": {"images"}, "cli": {"cli"}, "publish-cli": {"cli"}, "assemble": {"cli", "images"}}
	for _, operation := range got.Operations {
		if !slices.Equal(operation.Outputs, want[operation.ID]) {
			t.Fatalf("%s membership=%v want=%v", operation.ID, operation.Outputs, want[operation.ID])
		}
	}
	if got.Mode != "inspection" || got.Phase != "plan" || !got.StartedAt.IsZero() {
		t.Fatalf("static Plan claimed execution: %+v", got)
	}
}

func TestNewProgressValidatesPlanAndRetainsExactRows(t *testing.T) {
	if _, err := NewProgress(nil, ""); err == nil {
		t.Fatal("nil Plan accepted")
	}
	plan := githubTestPlan(t)
	m, err := NewProgress(plan, "attempt")
	if err != nil {
		t.Fatal(err)
	}
	got := m.Snapshot()
	if len(got.Operations) != len(plan.Graph.Nodes) || len(got.Outputs) != len(plan.LifecycleOutputs) {
		t.Fatal("projection omitted admitted rows")
	}
	plan.SchemaVersion = "invalid"
	if _, err := NewProgress(plan, "attempt"); err == nil {
		t.Fatal("invalid Plan accepted")
	}
}

func TestLocalProgressPreservesSchedulerResult(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "causal failure"}[fail], func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan := githubTestPlan(t)
			m, err := NewProgress(plan, "attempt")
			if err != nil {
				t.Fatal(err)
			}
			m.Start("local")
			cause := e.New(e.CodeVerifyFailed, "private provider cause")
			deps := localRunDeps{prepareRunner: func(context.Context, *Plan, LocalRunOptions, []string) (map[string]string, error) { return nil, nil }, executeNode: func(_ context.Context, _ *Plan, node Node, _ LocalRunOptions, _ map[string]string) error {
				for _, operation := range m.Snapshot().Operations {
					if operation.ID == node.ID && operation.State != "running" {
						t.Errorf("executing node absent running state: %+v", operation)
					}
				}
				if fail && node.Kind == NodeFetch {
					return cause
				}
				return nil
			}}
			result, err := runLocal(t.Context(), plan, withLocalDefaults(LocalRunOptions{Workspace: ".nvkr/progress", Jobs: 2, Progress: m}), deps)
			if fail {
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
				if !errors.Is(err, cause) {
					t.Fatal("progress changed causal error")
				}
			} else if err != nil || result == nil {
				t.Fatalf("progress changed successful result: %v", err)
			}
			for _, operation := range m.Snapshot().Operations {
				if operation.State == "running" {
					t.Fatal("operation returned without terminal state")
				}
			}
		})
	}
}
