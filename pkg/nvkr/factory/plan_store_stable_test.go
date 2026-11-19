// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestPlanTreeEnvironmentProjectionPreservesIdentity(t *testing.T) {
	definition, environment, stack := derivedTestInputs(t)
	first, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, variation := range []string{"publication prefix", "utility container", "runner", "native utility routing"} {
		t.Run(variation, func(t *testing.T) {
			env := first.Environment.Clone()
			env.GitHubBundle = "other"
			switch variation {
			case "publication prefix":
				env.Publication.RepositoryPrefix = "localhost:5000/other"
			case "utility container":
				env.UtilityContainer = "busybox@sha256:" + strings.Repeat("b", 64)
			case "runner":
				env.RunsOn = "ubuntu-22.04"
			case "native utility routing":
				env.UtilityContainers = map[string]string{"linux/amd64": env.UtilityContainer}
			}
			second, err := Compile(t.Context(), definition, env, stack, "test")
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(mustCanonical(t, first.Graph), mustCanonical(t, second.Graph)) {
				t.Fatal("fixture did not change an environment-bound graph value")
			}
			var roots []planFields
			var trees []map[string][]byte
			for _, plan := range []*Plan{first, second} {
				planPath := githubLayoutFor(plan).planPath
				files, err := renderPlanTree(t.Context(), plan, planPath)
				if err != nil {
					t.Fatal(err)
				}
				joined, raw, err := joinPlanTree(t.Context(), planPath, files)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(raw, mustCanonical(t, plan)) {
					t.Fatal("storage changed canonical Plan bytes")
				}
				before, err := plan.Digest()
				if err != nil {
					t.Fatal(err)
				}
				after, err := joined.Digest()
				if err != nil || before != after {
					t.Fatal("storage changed Plan identity", err)
				}
				var root planFields
				if err := json.Unmarshal(files[planPath], &root); err != nil {
					t.Fatal(err)
				}
				roots = append(roots, root)
				trees = append(trees, files)
			}
			for _, field := range []string{"graph", "lifecycleOutputs"} {
				var ref map[string]string
				if err := json.Unmarshal(roots[0][field], &ref); err != nil {
					t.Fatal(err)
				}
				name := ref["file"]
				if strings.Contains(name, "@") || !bytes.Equal(roots[0][field], roots[1][field]) || !bytes.Equal(trees[0][name], trees[1][name]) {
					t.Fatalf("%s is not one stable shared record", field)
				}
				if bytes.Contains(trees[0][name], []byte("repositoryPrefix")) {
					t.Fatal("shared record retained environment namespace")
				}
			}
		})
	}
}

func TestPlanTreeStorageVersions(t *testing.T) {
	definition, environment, stack := derivedTestInputs(t)
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{true, false} {
		storage := planTreeStorage
		if legacy {
			storage = legacyPlanTreeStorage
		}
		t.Run(storage, func(t *testing.T) {
			planPath := githubLayoutFor(plan).planPath
			files, err := renderPlanTreeFormat(t.Context(), plan, planPath, legacy)
			if err != nil {
				t.Fatal(err)
			}
			_, raw, err := joinPlanTree(t.Context(), planPath, files)
			if err != nil || !bytes.Equal(raw, mustCanonical(t, plan)) {
				t.Fatalf("storage version changed canonical Plan: %v", err)
			}
		})
	}
}

func TestPlanTreePrefixUpdateTouchesOnlyEnvironmentRecords(t *testing.T) {
	definition, environment, stack := derivedTestInputs(t)
	var previous map[string][]byte
	for _, prefix := range []string{"localhost:5000/candidate", "localhost:5000/nightly"} {
		environment.Publication.RepositoryPrefix = prefix
		plan, err := Compile(t.Context(), definition, environment, stack, "test")
		if err != nil {
			t.Fatal(err)
		}
		planPath := githubLayoutFor(plan).planPath
		files, err := renderPlanTree(t.Context(), plan, planPath)
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil {
			if len(files) != len(previous) {
				t.Fatal("prefix update changed the object inventory")
			}
			changed := 0
			for name, raw := range files {
				old, exists := previous[name]
				if !exists {
					t.Fatal("prefix update renamed an object", name)
				}
				if bytes.Equal(old, raw) {
					continue
				}
				changed++
				if name != planPath && !strings.Contains(name, "/plan/environment-") && !strings.Contains(name, "/plan/targets-") {
					t.Fatal("prefix update changed shared producer data", name)
				}
			}
			if changed != 3 {
				t.Fatalf("changed %d records; want only root, environment and targets", changed)
			}
		}
		previous = files
	}
}

func TestPlanStoreStableTamperAndSweep(t *testing.T) {
	_, bundles := sharedPlanStoreFixtures(t)
	for _, mutation := range []string{"tamper", "swap", "unreferenced"} {
		t.Run(mutation, func(t *testing.T) {
			root := t.TempDir()
			bundle := bundles[0]
			if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
				t.Fatal(err)
			}
			step := treeStepPath(t, bundle.PlanPath, bundle.Files, 0)
			if strings.Contains(step, "@") {
				t.Fatal("fixture is not a stable record")
			}
			switch mutation {
			case "tamper":
				writeGeneratedFixture(t, filepath.Join(root, step), append(bytes.Clone(bundle.Files[step]), '\n'))
			case "swap":
				writeGeneratedFixture(t, filepath.Join(root, step), bundle.Files[bundle.PlanPath])
			case "unreferenced":
				writeGeneratedFixture(t, filepath.Join(root, PlanStoreDirectory, "plan", "unused.json"), []byte("{}\n"))
			}
			_, err := LoadPlanContext(t.Context(), filepath.Join(root, bundle.PlanPath))
			if mutation != "unreferenced" {
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), root, bundle), e.CodePipelineDrift)
				return
			}
			if err != nil {
				t.Fatal("unreferenced file blocked own load", err)
			}
			assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, bundle), e.CodePipelineDrift)
			if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
				t.Fatal(err)
			}
			if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPlanStoreCoordinatedInterruptedPublication(t *testing.T) {
	plans, old := sharedPlanStoreFixtures(t)
	fresh := make([]*GeneratedBundle, len(plans))
	for i, plan := range plans {
		var err error
		fresh[i], err = RenderGitHub(t.Context(), freshGitHubRuntimePins(t, plan))
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, phase := range []string{"payloads", "first manifest"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			if err := WriteGeneratedBundles(t.Context(), root, old); err != nil {
				t.Fatal(err)
			}
			for _, bundle := range fresh {
				for name, raw := range bundle.Files {
					writeGeneratedFixture(t, filepath.Join(root, name), raw)
				}
			}
			if phase == "first manifest" {
				_, raw, err := ManifestFor(fresh[0])
				if err != nil {
					t.Fatal(err)
				}
				writeGeneratedFixture(t, filepath.Join(root, fresh[0].ManifestPath), raw)
			}
			_, err := LoadPlanContext(t.Context(), filepath.Join(root, old[1].PlanPath))
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
			if err := WriteGeneratedBundles(t.Context(), root, fresh); err != nil {
				t.Fatal("same batch could not recover", err)
			}
			for _, bundle := range fresh {
				if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
