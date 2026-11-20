// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"os"
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
				if root["graph"] != nil || root["graphDigest"] == nil {
					t.Fatal("tree must describe its derived graph without storing it")
				}
				for name := range files {
					if strings.Contains(name, "/plan/graph-") {
						t.Fatal("tree retained a graph record", name)
					}
				}
				roots = append(roots, root)
				trees = append(trees, files)
			}
			for _, field := range []string{"lifecycleOutputs"} {
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

func TestPlanStoreEnvironmentGraphsDeriveIndependently(t *testing.T) {
	first := signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
	second := recompileGitHubStabilityPlan(t, first, func(in *githubStabilityInputs) {
		in.environment.GitHubBundle = "unsigned"
		in.environment.Signing = nil
	})
	if bytes.Equal(mustCanonical(t, first.Graph), mustCanonical(t, second.Graph)) {
		t.Fatal("fixture did not change graph structure")
	}
	var bundles []*GeneratedBundle
	for _, plan := range []*Plan{first, second} {
		bundle, err := RenderGitHub(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		bundles = append(bundles, bundle)
	}
	root := t.TempDir()
	if err := WriteGeneratedBundles(t.Context(), root, bundles); err != nil {
		t.Fatal(err)
	}
	for i, expected := range []*Plan{first, second} {
		loaded, err := LoadPlanContext(t.Context(), filepath.Join(root, bundles[i].PlanPath))
		if err != nil || !bytes.Equal(mustCanonical(t, loaded), mustCanonical(t, expected)) {
			t.Fatal("Environment graph was not independently reconstructed", err)
		}
		if err := CheckGeneratedBundle(t.Context(), root, bundles[i]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPlanStoreConflictingSelectedRecords(t *testing.T) {
	plans, bundles := sharedPlanStoreFixtures(t)
	changed := recompileGitHubStabilityPlan(t, plans[1], func(in *githubStabilityInputs) {
		component := in.catalog.Components["kubelet"]
		component.BuildCommand = []string{"sh", "-c", "echo changed-build"}
		in.catalog.Components["kubelet"] = component
	})
	var err error
	bundles[1], err = RenderGitHub(t.Context(), changed)
	if err != nil {
		t.Fatal(err)
	}
	for _, reverse := range []bool{false, true} {
		name := "forward"
		selected := []*GeneratedBundle{bundles[0], bundles[1]}
		if reverse {
			name = "reverse"
			selected[0], selected[1] = selected[1], selected[0]
		}
		t.Run(name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "untouched")
			assertFactoryErrorCode(t, WriteGeneratedBundles(t.Context(), root, selected), e.CodeEngineCapability)
			if _, err := os.Stat(root); !os.IsNotExist(err) {
				t.Fatalf("refused batch created output: %v", err)
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
	for _, storage := range []string{legacyPlanTreeStorage, storedGraphPlanTreeStorage, planTreeStorage} {
		t.Run(storage, func(t *testing.T) {
			planPath := githubLayoutFor(plan).planPath
			files, err := renderPlanTreeFormat(t.Context(), plan, planPath, storage)
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

func TestRuntimePlanRefreshBesideProduction(t *testing.T) {
	production := namedGitHubTestPlan(t, "production")
	nightly := recompileGitHubStabilityPlan(t, production, func(in *githubStabilityInputs) {
		in.environment.GitHubBundle = "nightly"
		in.environment.GitHubRuntimePlan = true
	})
	var baseline []*GeneratedBundle
	for _, plan := range []*Plan{production, nightly} {
		bundle, err := RenderGitHub(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		baseline = append(baseline, bundle)
	}
	fresh := freshGitHubRuntimePins(t, nightly)
	updated, err := RenderGitHub(t.Context(), fresh)
	if err != nil || updated.PlanDigest == baseline[1].PlanDigest {
		t.Fatal("fixture did not refresh nightly", err)
	}
	for _, mode := range []string{"dispatch", "bootstrap", "stage"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			writeRuntimeTrustedInput(t, root, nightly.DefinitionPath, nightly.Definition)
			writeRuntimeTrustedInput(t, root, nightly.EnvironmentPath, nightly.Environment)
			if err := WriteGeneratedBundles(t.Context(), root, baseline); err != nil {
				t.Fatal(err)
			}
			_, productionManifest, err := ManifestFor(baseline[0])
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "dispatch":
				err = PrepareGitHubRuntimeBundle(t.Context(), fresh, root, false)
			case "bootstrap":
				stageGitHubRuntimeInputs(t, root, fresh)
				err = InstallGitHubRuntimeInputs(t.Context(), nightly, root)
			case "stage":
				stageGitHubRuntimeInputs(t, root, fresh)
				err = InstallGitHubStageRuntimeInputs(t.Context(), nightly, root)
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(filepath.Join(root, baseline[0].ManifestPath))
			if err != nil || !bytes.Equal(raw, productionManifest) {
				t.Fatal("runtime refresh changed production manifest", err)
			}
			for _, bundle := range []*GeneratedBundle{baseline[0], updated} {
				if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
					t.Fatal(err)
				}
				if _, err := LoadPlanContext(t.Context(), filepath.Join(root, bundle.PlanPath)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
