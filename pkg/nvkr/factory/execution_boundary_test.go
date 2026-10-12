// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestExecuteNodeObservesAndPackagesCompiledInputs(t *testing.T) {
	plan := githubTestPlan(t)
	t.Chdir(t.TempDir())
	ctx := context.Background()
	nodes, artifacts := planIndex(plan)
	var terminal Node
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeVerify {
			terminal = node
			break
		}
	}
	if terminal.ID == "" {
		t.Fatal("fixture has no terminal observation")
	}
	for _, id := range terminal.Inputs {
		if artifacts[id].Kind == "producer-result" {
			continue
		}
		if err := writeFactoryFile(ctx, artifacts[id].Path, []byte("fixture for "+id), 0o755); err != nil {
			t.Fatal(err)
		}
		writeProducerRecordFixture(t, plan, artifacts[id], artifacts["result/"+terminal.BuildTarget+"/"+artifacts[id].Component].Path)
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("plan.json", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPlanContext(ctx, "plan.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	got, err := loaded.Digest()
	if err != nil || got != want {
		t.Fatalf("loaded digest = %q, error = %v", got, err)
	}
	if err := ExecuteNode(ctx, loaded, terminal.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(artifacts[terminal.Outputs[0]].Path)
	if err != nil {
		t.Fatal(err)
	}
	var observation factoryObservation
	if err := json.Unmarshal(raw, &observation); err != nil {
		t.Fatal(err)
	}
	if observation.Qualification != TierStaticUnqualified || observation.BuildDigest != plan.Builds[0].BuildDigest || len(observation.Inputs) != len(terminal.Inputs) {
		t.Fatalf("observation = %+v", observation)
	}
	for i, input := range observation.Inputs {
		id := terminal.Inputs[i]
		digest, err := digestFactoryPath(ctx, artifacts[id].Path)
		if err != nil {
			t.Fatal(err)
		}
		if input.ArtifactID != id || input.Kind != artifacts[id].Kind || input.Digest != digest {
			t.Fatalf("input = %+v", input)
		}
	}
	packageNode := nodes["package/worker"]
	if err := ExecuteNode(ctx, loaded, packageNode.ID, nil, nil); err != nil {
		t.Fatal(err)
	}
	bundle, _, err := delivery.InspectBinaryBundle(ctx, artifacts[packageNode.Outputs[0]].Path)
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Entries) == 0 {
		t.Fatal("package contains no executable payloads")
	}
	assertFactoryErrorCode(t, ExecuteNode(ctx, loaded, "missing", nil, nil), e.CodeInvalidCatalog)

}

func writeProducerRecordFixture(t *testing.T, plan *Plan, artifact Artifact, resultPath string) pipeline.ArtifactResult {
	t.Helper()
	_, instruction, err := plan.producerInstruction(planNode(plan, artifact.Producer))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := digestFactoryPath(t.Context(), artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	result := pipeline.ArtifactResult{Component: instruction.Component, OutputType: instruction.OutputType,
		Qualification: pipeline.ArtifactUnqualified, Toolchain: instruction.Toolchain, BuilderType: instruction.BuilderType,
		Path: instruction.ArtifactPath, SHA256: digest, Size: info.Size()}
	raw, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeFactoryFile(t.Context(), resultPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestTerminalObservationRechecksExactProducerResults(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*pipeline.ArtifactResult)
	}{
		{name: "changed payload"},
		{name: "missing payload"},
		{name: "missing result"},
		{name: "wrong component", mutate: func(r *pipeline.ArtifactResult) { r.Component = "sibling" }},
		{name: "wrong type", mutate: func(r *pipeline.ArtifactResult) { r.OutputType = "tarball" }},
		{name: "wrong path", mutate: func(r *pipeline.ArtifactResult) { r.Path = "sibling/payload" }},
		{name: "wrong size", mutate: func(r *pipeline.ArtifactResult) { r.Size++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := githubTestPlan(t)
			t.Chdir(t.TempDir())
			_, artifacts := planIndex(plan)
			terminal := planNode(plan, "worker/terminal-observe")
			var selected Artifact
			for _, id := range terminal.Inputs {
				artifact := artifacts[id]
				if artifact.Kind == "producer-result" {
					continue
				}
				if err := writeFactoryFile(t.Context(), artifact.Path, []byte("built bytes"), 0o755); err != nil {
					t.Fatal(err)
				}
				writeProducerRecordFixture(t, plan, artifact, artifacts["result/worker/"+artifact.Component].Path)
				selected = artifact
			}
			resultPath := artifacts["result/worker/"+selected.Component].Path
			if tc.mutate != nil {
				record, err := readProducerResult(t.Context(), resultPath)
				if err != nil {
					t.Fatal(err)
				}
				tc.mutate(&record)
				raw, err := canonical.Marshal(record)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(resultPath, raw, 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				var err error
				switch tc.name {
				case "changed payload":
					err = os.WriteFile(selected.Path, []byte("other bytes"), 0o755)
				case "missing payload":
					err = os.Rename(selected.Path, selected.Path+".missing")
				case "missing result":
					err = os.Rename(resultPath, resultPath+".missing")
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			assertFactoryErrorCode(t, ExecuteNode(t.Context(), plan, terminal.ID, nil, nil), e.CodeVerifyFailed)
			if _, err := os.Stat(artifacts[terminal.Outputs[0]].Path); !os.IsNotExist(err) {
				t.Fatalf("failed producer observation persisted success: %v", err)
			}
		})
	}
}

func TestObservedProducerCannotBorrowSiblingResult(t *testing.T) {
	plan, artifacts := lifecyclePackageFixture(t, "apt-repository")
	duplicate := plan.Builds[0]
	duplicate.Name = "sibling"
	plan.Builds = append(plan.Builds, duplicate)
	record := artifacts["result"]
	record.BuildTarget = duplicate.Name
	artifacts["result"] = record
	_, err := observeProducerPayload(t.Context(), plan, artifacts["raw"], artifacts)
	assertFactoryErrorCode(t, err, e.CodeReleaseIncomplete)
}

func TestFactoryPlanLoaderRejectsMalformedFiles(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"invalid json", "{"},
		{"unknown field", `{"unknown":true}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadPlanContext(context.Background(), path)
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
		})
	}
	_, err := LoadPlanContext(context.Background(), filepath.Join(t.TempDir(), "absent"))
	assertFactoryErrorCode(t, err, e.CodePipelineDrift)
	assertFactoryErrorCode(t, ExecuteNode(context.Background(), nil, "missing", nil, nil), e.CodeUsage)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	assertFactoryErrorCode(t, ExecuteNode(ctx, nil, "missing", nil, nil), e.CodeTimeout)
}

func TestFactoryTreeObservationBindsBytesPathsAndModes(t *testing.T) {
	ctx := context.Background()
	makeTree := func() string {
		t.Helper()
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "sub", "tool"), []byte("tool"), 0o755); err != nil {
			t.Fatal(err)
		}
		return root
	}
	base, err := digestFactoryPath(ctx, makeTree())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		change       func(string) error
		same, reject bool
	}{
		{name: "relocation", same: true},
		{name: "changed bytes", change: func(root string) error {
			return os.WriteFile(filepath.Join(root, "sub", "tool"), []byte("changed"), 0o755)
		}},
		{name: "changed mode", change: func(root string) error { return os.Chmod(filepath.Join(root, "sub", "tool"), 0o644) }},
		{name: "changed path", change: func(root string) error {
			return os.Rename(filepath.Join(root, "sub", "tool"), filepath.Join(root, "sub", "renamed"))
		}},
		{name: "symlink", change: func(root string) error { return os.Symlink("tool", filepath.Join(root, "sub", "alias")) }, reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := makeTree()
			if tc.change != nil {
				if err := tc.change(root); err != nil {
					t.Fatal(err)
				}
			}
			got, err := digestFactoryPath(ctx, root)
			if tc.reject {
				if err == nil {
					t.Fatal("unsupported entry accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if (got == base) != tc.same {
				t.Fatalf("digest = %s, base = %s", got, base)
			}
		})
	}
}
