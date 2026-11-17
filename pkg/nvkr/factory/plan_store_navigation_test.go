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
