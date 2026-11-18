// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func familyFixture(t *testing.T) (Config, func(string)) {
	t.Helper()
	cfg, promote := maintenanceFixture(t)
	for _, name := range []string{"other", "unrelated"} {
		if err := os.Mkdir(name, 0700); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{catalog.RegistryFile, catalog.LockFile} {
			writeFixture(t, name+"/"+file, readFixture(t, "catalog/"+file))
		}
	}
	cfg.Family = &FamilyPolicy{Name: "fixture", Catalogs: []FamilyCatalog{{Catalog: "catalog", Sources: cfg.Sources}, {Catalog: "other", Sources: cfg.Sources}}}
	writeJSON(t, "maintenance.yaml", cfg)
	return cfg, promote
}

func TestFamilyBumpCrossCatalogJourney(t *testing.T) {
	cfg, promote := familyFixture(t)
	before := map[string][]byte{}
	for _, name := range []string{"catalog/pins.lock.yaml", "other/pins.lock.yaml", "unrelated/pins.lock.yaml", ".github/nvkr-run/plan.json"} {
		before[name] = readFixture(t, name)
	}
	promote("1.1.0")
	preview, err := BumpFamily(t.Context(), "maintenance.yaml", "fixture", "test (deadbeef)", true)
	assertCode(t, err, e.CodeDiff)
	if preview.Complete || preview.Changed != 6 || len(preview.Catalogs) != 2 || len(preview.Consumers) != 1 {
		t.Fatalf("incomplete preview: %+v", preview)
	}
	for name, raw := range before {
		if !bytes.Equal(raw, readFixture(t, name)) {
			t.Fatalf("dry run wrote %s", name)
		}
	}
	result, err := BumpFamily(t.Context(), "maintenance.yaml", "fixture", "test (deadbeef)", false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || len(result.CommittedCatalogs) != 2 {
		t.Fatalf("incomplete result: %+v", result)
	}
	for _, name := range []string{"catalog", "other"} {
		lock, err := catalog.LoadLockContext(t.Context(), name)
		if err != nil {
			t.Fatal(err)
		}
		if lock.Pins["root"].Tag != "release/v1.1.0" || lock.Pins["role"].Commit != lock.Pins["root"].Commit {
			t.Fatalf("inconsistent %s: %+v", name, lock.Pins)
		}
	}
	if !bytes.Equal(before["unrelated/pins.lock.yaml"], readFixture(t, "unrelated/pins.lock.yaml")) {
		t.Fatal("changed an excluded cohort")
	}
	if _, err := lifecycle.GenerateFiles(t.Context(), cfg.Definition, cfg.Environment, "test (deadbeef)", "github", ".", true); err != nil {
		t.Fatal(err)
	}
}

func TestFamilyPreparationFailureLeavesOriginals(t *testing.T) {
	_, promote := familyFixture(t)
	promote("1.1.0")
	before := map[string][]byte{}
	for _, name := range []string{"catalog/pins.lock.yaml", "other/pins.lock.yaml", ".github/nvkr-run/plan.json"} {
		before[name] = readFixture(t, name)
	}
	calls := 0
	result, err := bumpFamily(t.Context(), "maintenance.yaml", "fixture", "test (deadbeef)", false, func(ctx context.Context, req catalog.SourcePinRequest) (*catalog.PreparedSourcePins, catalog.SourcePinUpdate, error) {
		calls++
		if calls == 2 {
			return nil, catalog.SourcePinUpdate{}, e.New(e.CodeFetchFailed, "injected second catalog resolution failure")
		}
		return catalog.PrepareSourcePins(ctx, req)
	})
	assertCode(t, err, e.CodeFetchFailed)
	if calls != 2 || result.Complete || len(result.CommittedCatalogs) != 0 {
		t.Fatalf("partial success: %+v", result)
	}
	for name, raw := range before {
		if !bytes.Equal(raw, readFixture(t, name)) {
			t.Fatalf("failed preparation wrote %s", name)
		}
	}
}

func TestFamilyRejectsStaleOverlayPredecessorBeforeResolution(t *testing.T) {
	filename, input := producerOverlayFixture(t)
	name := input.Overlays[1].Destination
	overlay, err := catalog.LoadOverlayContext(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	overlay.RequiresCatalogDigest = "sha256:" + strings.Repeat("a", 64)
	writeJSON(t, name, overlay)
	before := readFixture(t, name)
	config := Config{APIVersion: SchemaVersion, Name: "test", Catalog: "inputs/catalogs/nvidia", Sources: []string{"source"}, Definition: "absent.yaml", Environment: "absent-env.yaml", Validation: "static", ProducerOverlays: filename,
		Family: &FamilyPolicy{Name: "test", Catalogs: []FamilyCatalog{{Catalog: "inputs/catalogs/nvidia", Sources: []string{"source"}}}}}
	writeJSON(t, "maintenance.yaml", config)
	called := false
	result, err := bumpFamily(t.Context(), "maintenance.yaml", "test", "test", false, func(context.Context, catalog.SourcePinRequest) (*catalog.PreparedSourcePins, catalog.SourcePinUpdate, error) {
		called = true
		return nil, catalog.SourcePinUpdate{}, nil
	})
	assertCode(t, err, e.CodePipelineDrift)
	if called || result.Complete || !bytes.Equal(before, readFixture(t, name)) {
		t.Fatal("stale predecessor reached pin work or changed files")
	}
}

func TestFamilyRegeneratesRuntimeAndQualificationOwners(t *testing.T) {
	for _, kind := range []string{"runtime", "qualification"} {
		t.Run(kind, func(t *testing.T) {
			var cfg Config
			var promote func(string)
			if kind == "qualification" {
				cfg, promote = qualificationFixture(t)
			} else {
				cfg, promote = maintenanceFixture(t)
				const repository = "https://github.com/example/family"
				t.Setenv("GIT_CONFIG_VALUE_0", repository)
				writeFixture(t, "catalog/registry.yaml", bytes.ReplaceAll(readFixture(t, "catalog/registry.yaml"), []byte("https://maintenance.example.invalid/source"), []byte(repository)))
				if _, err := lifecycle.GenerateFiles(t.Context(), cfg.Definition, cfg.Environment, "test (deadbeef)", "github", ".", false); err != nil {
					t.Fatal(err)
				}
				writeFixture(t, runtimeFixtureEnvironment, append(readFixture(t, cfg.Environment), ("githubBundle: runtime\ngithubRuntimePlan: true\ngithubRuntimeSource: {repository: "+repository+", ref: refs/heads/main}\n")...))
				cfg.AdditionalRuns = []RunInput{{Definition: cfg.Definition, Environment: runtimeFixtureEnvironment}}
				if _, err := lifecycle.GenerateFiles(t.Context(), cfg.Definition, runtimeFixtureEnvironment, "test (deadbeef)", "github", ".", false); err != nil {
					t.Fatal(err)
				}
			}
			cfg.Family = &FamilyPolicy{Name: "fixture", Catalogs: []FamilyCatalog{{Catalog: cfg.Catalog, Sources: cfg.Sources}}}
			writeJSON(t, "maintenance.yaml", cfg)
			promote("1.1.0")
			result, err := BumpFamily(t.Context(), "maintenance.yaml", "fixture", "test (deadbeef)", false)
			if err != nil || !result.Complete {
				t.Fatalf("incomplete family owner regeneration: %+v %v", result, err)
			}
			if _, err := generateClosure(t.Context(), cfg, "test (deadbeef)", true, lifecycle.GenerateFiles); err != nil {
				t.Fatal(err)
			}
			if kind == "runtime" {
				plan, err := factory.LoadPlanContext(t.Context(), runtimeFixturePlan)
				if err != nil {
					t.Fatal(err)
				}
				if plan.RuntimeInputs != nil || plan.RuntimeSourceCommit != "" {
					t.Fatal("family maintenance froze dispatch authority")
				}
				_, _, err = factory.CaptureCompileFiles(t.Context(), ".", cfg.Definition, runtimeFixtureEnvironment, "test (deadbeef)")
				assertCode(t, err, e.CodeEngineCapability)
				// Maintenance authored a static bundle, not a dispatch/source receipt.
				_, err = lifecycle.GenerateRuntimeFiles(t.Context(), cfg.Definition, runtimeFixtureEnvironment, "test (deadbeef)", "github", ".", false)
				assertCode(t, err, e.CodeUnpinnedSource)
			}
		})
	}
}
