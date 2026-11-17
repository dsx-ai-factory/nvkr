// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func sharedPlanStoreFixtures(t *testing.T) ([]*Plan, []*GeneratedBundle) {
	t.Helper()
	first := githubRuntimePlanFixture(t)
	second := recompileGitHubStabilityPlan(t, first, func(in *githubStabilityInputs) {
		in.environment.GitHubBundle = "other-project"
	})
	plans := []*Plan{first, second}
	bundles := make([]*GeneratedBundle, len(plans))
	for i, plan := range plans {
		var err error
		bundles[i], err = RenderGitHub(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
	}
	return plans, bundles
}

func TestPlanStoreSharesFrozenRecords(t *testing.T) {
	plans, bundles := sharedPlanStoreFixtures(t)
	var first, second planFields
	if err := json.Unmarshal(bundles[0].Files[bundles[0].PlanPath], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bundles[1].Files[bundles[1].PlanPath], &second); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"graph", "catalogSnapshots"} {
		if !bytes.Equal(first[field], second[field]) {
			t.Fatalf("identical %s is not shared across bundles", field)
		}
	}
	const body = "printf shared-command\\n\nprintf done\n"
	plan := recompileGitHubStabilityPlan(t, plans[0], func(in *githubStabilityInputs) {
		component := in.catalog.Components["kubelet"]
		component.BuildCommand = []string{"sh", "-c", body}
		in.catalog.Components["kubelet"] = component
	})
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	scriptPath := planStorePath([]byte(body), ".sh")
	if string(bundle.Files[scriptPath]) != body {
		t.Fatal("exact script bytes were not stored")
	}
	componentPath := ""
	for name, raw := range bundle.Files {
		if !isPlanStoreRecord(name) {
			continue
		}
		if bytes.Contains(raw, mustCanonical(t, body)) {
			t.Fatal("a frozen JSON object retained an inline copy of the script")
		}
		var record planFields
		if json.Unmarshal(raw, &record) == nil && bytes.Contains(record["buildCommand"], []byte(scriptPath)) {
			componentPath = name
		}
	}
	if componentPath == "" {
		t.Fatal("snapshot component did not reference the script")
	}
	references := 0
	for _, raw := range bundle.Files {
		references += strings.Count(string(raw), componentPath)
	}
	if references < 2 {
		t.Fatal("snapshot and resolved-component list did not share the component record")
	}
	_, packed, err := joinPlanTree(t.Context(), bundle.PlanPath, bundle.Files)
	if err != nil || !bytes.Equal(packed, mustCanonical(t, plan)) {
		t.Fatal("shared frozen records changed the Plan", err)
	}
}

func TestPlanStoreRetirementPreservesOtherOwners(t *testing.T) {
	plans, bundles := sharedPlanStoreFixtures(t)
	root := t.TempDir()
	for _, bundle := range bundles {
		if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
	}
	fresh := make([]*GeneratedBundle, len(plans))
	for i, plan := range plans {
		var err error
		fresh[i], err = RenderGitHub(t.Context(), freshGitHubRuntimePins(t, plan))
		if err != nil {
			t.Fatal(err)
		}
	}
	var retired []string
	for name := range bundles[0].Files {
		if isPlanStoreRecord(name) && bundles[1].Files[name] != nil && fresh[0].Files[name] == nil && fresh[1].Files[name] == nil {
			retired = append(retired, name)
		}
	}
	if len(retired) == 0 {
		t.Fatal("fixture has no shared objects retired by both owners")
	}
	t.Run("missing shared object is not an interrupted retirement", func(t *testing.T) {
		broken := copyGitHubRuntimeRoot(t, root)
		if err := os.Remove(filepath.Join(broken, retired[0])); err != nil {
			t.Fatal(err)
		}
		before := githubRuntimeTree(t, broken)
		assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), broken, fresh[0]), e.CodePipelineDrift)
		if !bytes.Equal(mustCanonical(t, before), mustCanonical(t, githubRuntimeTree(t, broken))) {
			t.Fatal("rejected retirement changed bundle payloads")
		}
	})
	if err := WriteGeneratedBundle(t.Context(), root, fresh[0]); err != nil {
		t.Fatal(err)
	}
	if err := CheckGeneratedBundle(t.Context(), root, bundles[1]); err != nil {
		t.Fatal("first refresh damaged the other bundle", err)
	}
	for _, name := range retired {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatal("shared object retired before its last owner", err)
		}
	}
	if err := WriteGeneratedBundle(t.Context(), root, fresh[1]); err != nil {
		t.Fatal(err)
	}
	for _, name := range retired {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("last-owner retirement left an obsolete object", err)
		}
	}
	for _, bundle := range fresh {
		if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlanStoreConcurrentWriters(t *testing.T) {
	plans, bundles := sharedPlanStoreFixtures(t)
	root := t.TempDir()
	results := make(chan error, len(bundles))
	for _, bundle := range bundles {
		go func() { results <- WriteGeneratedBundle(t.Context(), root, bundle) }()
	}
	for range bundles {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	if t.Failed() {
		return
	}
	for i, bundle := range bundles {
		if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
			t.Fatal(err)
		}
		loaded, err := LoadPlanContext(t.Context(), filepath.Join(root, bundle.PlanPath))
		if err != nil || !bytes.Equal(mustCanonical(t, loaded), mustCanonical(t, plans[i])) {
			t.Fatal("concurrent publication changed frozen Plan bytes", err)
		}
	}
}

func TestPlanStoreChecksOtherOwnerAndUnownedObjects(t *testing.T) {
	_, bundles := sharedPlanStoreFixtures(t)
	original := t.TempDir()
	for _, bundle := range bundles {
		if err := WriteGeneratedBundle(t.Context(), original, bundle); err != nil {
			t.Fatal(err)
		}
	}
	other := ""
	for name := range bundles[1].Files {
		if isPlanStoreRecord(name) && bundles[0].Files[name] == nil {
			other = name
			break
		}
	}
	if other == "" {
		t.Fatal("fixture must have a record owned only by the other bundle")
	}
	for _, test := range []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"missing", func(t *testing.T, root string) {
			if err := os.Remove(filepath.Join(root, other)); err != nil {
				t.Fatal(err)
			}
		}},
		{"tampered", func(t *testing.T, root string) {
			writeGeneratedFixture(t, filepath.Join(root, other), []byte("{}\n"))
		}},
		{"extra", func(t *testing.T, root string) {
			raw := []byte("unowned\n")
			writeGeneratedFixture(t, filepath.Join(root, planStorePath(raw, ".sh")), raw)
		}},
		{"mode", func(t *testing.T, root string) {
			if err := os.Chmod(filepath.Join(root, other), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := copyGitHubRuntimeRoot(t, original)
			test.change(t, root)
			assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, bundles[0]), e.CodePipelineDrift)
			_, err := LoadPlanContext(t.Context(), filepath.Join(root, bundles[0].PlanPath))
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
			assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), root, bundles[0]), e.CodePipelineDrift)
		})
	}
}
