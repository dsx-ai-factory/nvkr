// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPlanStoreMixedLegacyNavigationRetiresOnlyItsOwner(t *testing.T) {
	const script = "printf first\nprintf last"
	oldPlan := recompileGitHubStabilityPlan(t, githubRuntimePlanFixture(t), func(in *githubStabilityInputs) {
		component := in.catalog.Components["kubelet"]
		component.BuildCommand = []string{"sh", "-c", script}
		in.catalog.Components["kubelet"] = component
	})
	old, err := RenderGitHub(t.Context(), oldPlan)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := githubScriptNavigation(oldPlan, old.Files)
	if err != nil {
		t.Fatal(err)
	}
	// Encode the old command-operand shape explicitly. This descriptive fixture
	// exercises migration through a published manifest, not just the decoder.
	legacyJobs := map[string]any{}
	for id, job := range jobs {
		operations := []map[string]any{}
		for _, operation := range job.Operations {
			command := []any{"sh", "-c"}
			for _, name := range operation.Scripts {
				command = append(command, map[string]string{"file": name})
			}
			operations = append(operations, map[string]any{
				"node": operation.Node, "step": operation.Step, "target": operation.Target,
				"producer": operation.Producer, "record": operation.Record, "command": command,
			})
		}
		legacyJobs[id] = map[string]any{"workflow": job.Workflow, "operations": operations}
	}
	legacyBytes := mustCanonical(t, map[string]any{"jobs": legacyJobs})
	legacyPath := filepath.ToSlash(filepath.Join(filepath.Dir(old.PlanPath), "script-index.json"))
	legacy := *old
	legacy.Files = maps.Clone(old.Files)
	legacy.Files[legacyPath] = legacyBytes
	root := t.TempDir()
	if err := WriteGeneratedBundle(t.Context(), root, &legacy); err != nil {
		t.Fatal(err)
	}
	neighborPlan := recompileGitHubStabilityPlan(t, oldPlan, func(in *githubStabilityInputs) {
		in.environment.GitHubBundle = "other-project"
	})
	neighbor, err := RenderGitHub(t.Context(), neighborPlan)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteGeneratedBundle(t.Context(), root, neighbor); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(root, legacyPath)); err != nil || !bytes.Equal(raw, legacyBytes) {
		t.Fatal("new neighbor retired or changed the live legacy index", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, PlanStoreIndexPath))
	if err != nil {
		t.Fatal(err)
	}
	var index planStoreIndexDocument
	if err := json.Unmarshal(raw, &index); err != nil {
		t.Fatal(err)
	}
	scriptPath := testNamedStorePath(t, "scripts/kubelet/build", []byte(script), ".sh")
	foundScript := false
	for id, expected := range jobs {
		for _, operation := range expected.Operations {
			foundScript = foundScript || slices.Contains(operation.Scripts, scriptPath)
		}
		found := false
		for _, variant := range index.Jobs[id] {
			for _, workflow := range variant.Workflows {
				if workflow.Bundle == filepath.Base(filepath.Dir(old.PlanPath)) {
					found = true
					if workflow.File != expected.Workflow || !reflect.DeepEqual(variant.Operations, expected.Operations) {
						t.Fatal("legacy migration lost a job or script binding", id)
					}
				}
			}
		}
		if !found {
			t.Fatal("legacy owner disappeared from shared navigation", id)
		}
	}
	if !foundScript {
		t.Fatal("legacy fixture omitted the exact script link")
	}
	if raw, err := os.ReadFile(filepath.Join(root, scriptPath)); err != nil || string(raw) != script {
		t.Fatal("legacy script bytes changed", err)
	}
	if err := WriteGeneratedBundle(t.Context(), root, old); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, legacyPath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("regenerating the legacy owner did not retire its private index", err)
	}
	for _, bundle := range []*GeneratedBundle{old, neighbor} {
		if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlanStoreCorruptNeighborWorkflowRefusesPublication(t *testing.T) {
	plans, bundles := sharedPlanStoreFixtures(t)
	root := t.TempDir()
	for _, bundle := range bundles {
		if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
	}
	incoming, err := RenderGitHub(t.Context(), freshGitHubRuntimePins(t, plans[0]))
	if err != nil {
		t.Fatal(err)
	}
	neighbor := bundles[1]
	writeGeneratedFixture(t, filepath.Join(root, neighbor.WorkflowPath), []byte("not: [valid yaml\n"))
	before := githubRuntimeTree(t, root)
	assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, neighbor), e.CodePipelineDrift)
	if err := CheckGeneratedBundle(t.Context(), root, bundles[0]); err != nil {
		t.Fatal("neighbor workflow damage blocked an independent check", err)
	}
	err = WriteGeneratedBundle(t.Context(), root, incoming)
	assertFactoryErrorCode(t, err, e.CodePipelineDrift)
	if !strings.Contains(err.Error(), "navigation member differs from manifest") {
		t.Fatal("corrupt workflow was not rejected at the manifest hash boundary", err)
	}
	if !reflect.DeepEqual(before, githubRuntimeTree(t, root)) {
		t.Fatal("refused generation changed payload, manifest or shared index")
	}
	if _, err := LoadPlanContext(t.Context(), filepath.Join(root, bundles[0].PlanPath)); err != nil {
		t.Fatal("neighbor workflow damage blocked own Plan load", err)
	}
	writeGeneratedFixture(t, filepath.Join(root, neighbor.WorkflowPath), neighbor.Files[neighbor.WorkflowPath])
	if err := WriteGeneratedBundle(t.Context(), root, incoming); err != nil {
		t.Fatal(err)
	}
	for _, bundle := range []*GeneratedBundle{incoming, neighbor} {
		if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
	}
	after := githubRuntimeTree(t, root)
	if before[filepath.Join(root, PlanStoreIndexPath)] == after[filepath.Join(root, PlanStoreIndexPath)] {
		t.Fatal("restored neighbor did not permit the new shared index commit")
	}
}

func TestPlanStoreNavigationStructureAndOwnerScope(t *testing.T) {
	previous := githubForcePartitionFallback
	githubForcePartitionFallback = true
	t.Cleanup(func() { githubForcePartitionFallback = previous })
	first := recompileGitHubStabilityPlan(t, githubRuntimePlanFixture(t), func(in *githubStabilityInputs) {
		in.environment.GitHubExecutionPartitions = true
	})
	second := recompileGitHubStabilityPlan(t, first, func(in *githubStabilityInputs) {
		in.environment.GitHubBundle = "other-project"
	})
	root := t.TempDir()
	var bundles []*GeneratedBundle
	for _, plan := range []*Plan{first, freshGitHubRuntimePins(t, second)} {
		bundle, err := RenderGitHub(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
		bundles = append(bundles, bundle)
	}
	indexPath := filepath.Join(root, PlanStoreIndexPath)
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	decode := func(t *testing.T) planStoreIndexDocument {
		t.Helper()
		var index planStoreIndexDocument
		if err := json.Unmarshal(raw, &index); err != nil {
			t.Fatal(err)
		}
		return index
	}
	index := decode(t)
	shared, divergent := "", ""
	for _, id := range sortedKeys(index.Jobs) {
		variants := index.Jobs[id]
		if len(variants) == 2 {
			divergent = id
		}
		if len(variants) == 1 && len(variants[0].Workflows) == 2 {
			shared = id
		}
	}
	if shared == "" || divergent == "" {
		t.Fatal("fixture needs shared and divergent partitioned jobs")
	}
	for _, bundle := range bundles {
		if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal("valid partitioned navigation failed", err)
		}
	}
	// Prefer neighbor-only damage so the selected-job equality check cannot
	// hide a missing structural guard.
	for _, test := range []struct {
		name   string
		change func(*planStoreIndexDocument)
	}{
		{"empty variants", func(index *planStoreIndexDocument) { index.Jobs["empty-variants"] = []planStoreJobVariant{} }},
		{"empty workflow users", func(index *planStoreIndexDocument) { index.Jobs[divergent][1].Workflows = []planStoreWorkflow{} }},
		{"duplicate operations", func(index *planStoreIndexDocument) {
			index.Jobs[divergent][1].Operations = index.Jobs[divergent][0].Operations
		}},
		{"unordered variants", func(index *planStoreIndexDocument) { slices.Reverse(index.Jobs[divergent]) }},
		{"unordered users", func(index *planStoreIndexDocument) { slices.Reverse(index.Jobs[shared][0].Workflows) }},
		{"unknown owner", func(index *planStoreIndexDocument) {
			index.Jobs[divergent][1].Workflows[0] = planStoreWorkflow{Bundle: "nvkr-run-unknown", File: ".github/workflows/nvkr-run-unknown.yml"}
		}},
		{"repeated owner", func(index *planStoreIndexDocument) {
			users := index.Jobs[shared][0].Workflows
			index.Jobs[shared][0].Workflows = append(users, users[len(users)-1])
		}},
		{"invalid workflow namespace", func(index *planStoreIndexDocument) {
			index.Jobs[divergent][1].Workflows[0].File = ".github/workflows/nvkr-run-foreign.yml"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := decode(t)
			test.change(&changed)
			encoded, err := treeJSON(changed)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(encoded, raw) {
				t.Fatal("navigation fixture did not change bytes")
			}
			writeGeneratedFixture(t, indexPath, encoded)
			assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, bundles[0]), e.CodePipelineDrift)
		})
	}
	t.Run("well-formed neighbor-only mutation", func(t *testing.T) {
		changed := decode(t)
		neighbor := filepath.Base(filepath.Dir(bundles[1].PlanPath))
		mutated := false
		for i, variant := range changed.Jobs[divergent] {
			if len(variant.Workflows) == 1 && variant.Workflows[0].Bundle == neighbor && len(variant.Operations) != 0 {
				changed.Jobs[divergent][i].Operations[0].Node += "-changed"
				mutated = true
			}
		}
		if !mutated {
			t.Fatal("fixture lacks a neighbor-only job variant")
		}
		encoded, err := treeJSON(changed)
		if err != nil {
			t.Fatal(err)
		}
		writeGeneratedFixture(t, indexPath, encoded)
		if err := CheckGeneratedBundle(t.Context(), root, bundles[0]); err != nil {
			t.Fatal("neighbor-only navigation drift blocked selected bundle", err)
		}
		assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, bundles[1]), e.CodePipelineDrift)
	})
}
