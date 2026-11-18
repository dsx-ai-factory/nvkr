// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

func TestCatalogListRequiresOneWholePlatformSet(t *testing.T) {
	output := authored.DefinitionOutput{Name: "image", Components: []string{"image"}, Platforms: []string{"linux/amd64", "linux/arm64"}, Format: authored.OutputOCI}
	for _, tc := range []struct {
		name      string
		platforms [][]string
		code      e.Code
	}{
		{"cannot union native catalogs", [][]string{{"linux/amd64"}, {"linux/arm64"}}, e.CodeNoMatch},
		{"one complete stack", [][]string{{"linux/amd64"}, {"linux/amd64", "linux/arm64"}}, ""},
		{"two complete stacks", [][]string{{"linux/amd64", "linux/arm64"}, {"linux/amd64", "linux/arm64"}}, e.CodeAmbiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var candidates []CatalogCandidate
			for _, platforms := range tc.platforms {
				candidates = append(candidates, CatalogCandidate{CatalogSnapshot: &catalog.Catalog{Components: map[string]catalog.Component{"image": {Platforms: platforms}}}})
			}
			_, err := selectCatalogCandidate(output, candidates)
			if factoryErrorCode(err) != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
		})
	}
}

func TestCatalogListUnusedPinSetSourceAccess(t *testing.T) {
	unused, _, pinEnv := pinSetSemanticInputs(t)
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	def, env := fixtureInputs("ai-factory", "containerd-tools", "")
	def.Outputs[0].Components = []string{"containerd"}
	def.Catalogs = []authored.Catalog{{Base: "selected"}, {Base: "unused"}}
	env.SourceAccess = pinEnv.SourceAccess
	plan, err := compileDocumentsWithLoader(t.Context(), "definition.yaml", def, "environment.yaml", env, "test", func(_ context.Context, spec authored.Catalog) (*catalog.Stack, error) {
		if spec.Base == "unused" {
			return catalog.ApplyStack(unused)
		}
		return catalog.ApplyStack(base)
	})
	if err != nil {
		t.Fatal(err)
	}
	for repository := range env.SourceAccess {
		if !plan.catalogPinSetRepositories()[repository] {
			t.Fatal("unused pin-set source lost revalidation credential scope")
		}
	}
}

func TestCatalogListMigrationKeepsAcquiredImagesNative(t *testing.T) {
	base, def, env := pinSetSemanticInputs(t)
	def.Outputs[0].Platform = ""
	def.Outputs[0].Platforms = []string{"linux/amd64"}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), def, env, stack, "test")
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if plan != nil {
		t.Fatal("acquired image aggregate emitted a Plan")
	}
}

func TestCompositeCatalogListReplay(t *testing.T) {
	root, _ := compositeFactoryFixture(t, func(alpha, beta *authored.Definition) {
		alpha.Catalogs = []authored.Catalog{alpha.Catalog}
		beta.Catalogs = []authored.Catalog{beta.Catalog}
	})
	plan, snapshot, err := CaptureCompileFiles(t.Context(), root, compositeFixturePath, snapshotEnvironment, "test")
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := CompileSnapshot(t.Context(), snapshot, "test")
	if err != nil {
		t.Fatal(err)
	}
	want, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	got, err := replayed.Digest()
	if err != nil || got != want {
		t.Fatalf("composite list replay drift: %v", err)
	}
}

func TestCatalogListSnapshotReplay(t *testing.T) {
	root, def, _ := snapshotFixture(t)
	before, _, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, "snapshot-test")
	if err != nil {
		t.Fatal(err)
	}
	def.Catalogs = []authored.Catalog{def.Catalog, {Base: "../unused"}}
	provider := testCatalogProvider(t)
	raw, err := provider.ReadFileContext(t.Context(), catalog.RegistryFile)
	if err != nil {
		t.Fatal(err)
	}
	var registry map[string]any
	if err := yaml.UnmarshalStrict(raw, &registry); err != nil {
		t.Fatal(err)
	}
	delete(registry["components"].(map[string]any), "containerd")
	writeSnapshotFixtureDocument(t, root, "unused/registry.yaml", registry)
	lock, err := provider.ReadFileContext(t.Context(), catalog.LockFile)
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshotFixtureFile(t, root, "unused/pins.lock.yaml", lock)
	writeSnapshotFixtureDocument(t, root, snapshotDefinition, def)
	after, snapshot, err := CaptureCompileFiles(t.Context(), root, snapshotDefinition, snapshotEnvironment, "snapshot-test")
	if err != nil {
		t.Fatal(err)
	}
	for i, build := range before.Builds {
		if build.BuildDigest != after.Builds[i].BuildDigest || !reflect.DeepEqual(build.ResolvedComponents, after.Builds[i].ResolvedComponents) {
			t.Fatal("catalog list changed producer identity")
		}
	}
	want, err := after.Digest()
	if err != nil {
		t.Fatal(err)
	}
	writeSnapshotFixtureFile(t, root, "catalog/registry.yaml", []byte("changed after capture"))
	replayed, err := CompileSnapshot(t.Context(), snapshot, "snapshot-test")
	if err != nil {
		t.Fatal(err)
	}
	missing := snapshot
	missing.Files = slices.DeleteFunc(slices.Clone(snapshot.Files), func(file InputFile) bool { return file.Path == "unused/registry.yaml" })
	if bad, err := CompileSnapshot(t.Context(), missing, "snapshot-test"); err == nil || bad != nil {
		t.Fatal("replay accepted omitted unused candidate")
	}
	got, err := replayed.Digest()
	if err != nil || got != want {
		t.Fatalf("catalog list replay changed Plan: %v", err)
	}
	if _, err := Compile(t.Context(), def, after.Environment, after.CatalogCandidates[0].stack(), "test"); factoryErrorCode(err) != e.CodeUsage {
		t.Fatalf("direct compilation accepted unresolved list: %v", err)
	}
	if _, err := compileWithTargetStacks(t.Context(), def, after.Environment, after.CatalogCandidates[0].stack(), nil, "test", "definition.yaml", "environment.yaml"); factoryErrorCode(err) != e.CodeUsage {
		t.Fatalf("internal compilation accepted missing candidates: %v", err)
	}
}

func TestCatalogListSelection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates []authored.Catalog
		override   *authored.Catalog
		code       e.Code
	}{
		{"unique", []authored.Catalog{{Base: "missing"}, {Base: "selected"}}, nil, ""},
		{"missing", []authored.Catalog{{Base: "missing"}}, nil, e.CodeNoMatch},
		{"ambiguous identical", []authored.Catalog{{Base: "selected"}, {Base: "other"}}, nil, e.CodeAmbiguous},
		{"explicit override", []authored.Catalog{{Base: "selected"}, {Base: "other"}}, &authored.Catalog{Base: "selected"}, ""},
		{"invalid unselected", []authored.Catalog{{Base: "selected"}, {Base: "invalid"}}, &authored.Catalog{Base: "selected"}, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
			if err != nil {
				t.Fatal(err)
			}
			def, env := fixtureInputs("ai-factory", "containerd-tools", "")
			def.Outputs[0].Components = []string{"containerd"}
			def.Outputs[0].Catalog = tc.override
			def.Catalogs = tc.candidates
			loads := map[string]int{}
			load := func(_ context.Context, spec authored.Catalog) (*catalog.Stack, error) {
				loads[spec.Base]++
				if spec.Base == "invalid" {
					return nil, e.New(e.CodeInvalidCatalog, "invalid candidate")
				}
				stack, err := catalog.ApplyStack(base)
				if err != nil {
					return nil, err
				}
				if spec.Base == "missing" {
					delete(stack.Catalog.Components, "containerd")
					return catalog.ApplyStack(stack.Catalog)
				}
				return stack, nil
			}
			plan, err := compileDocumentsWithLoader(t.Context(), "definition.yaml", def, "environment.yaml", env, "test", load)
			if tc.code != "" {
				if factoryErrorCode(err) != tc.code || plan != nil {
					t.Fatalf("got plan=%v error=%v; want %s", plan != nil, err, tc.code)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, count := range loads {
				if count != 1 {
					t.Fatal("loaded stack more than once")
				}
			}
			if len(plan.CatalogCandidates) != len(tc.candidates) {
				t.Fatal("missing candidate snapshots")
			}
			legacy := def.Clone()
			legacy.Catalogs = nil
			legacy.Outputs[0].Catalog = &authored.Catalog{Base: "selected"}
			before, err := compileDocumentsWithLoader(t.Context(), "definition.yaml", legacy, "environment.yaml", env, "test", load)
			if err != nil {
				t.Fatal(err)
			}
			if before.Builds[0].BuildDigest != plan.Builds[0].BuildDigest || before.Builds[0].CatalogDigest != plan.Builds[0].CatalogDigest {
				t.Fatal("migration changed selected producer identity")
			}
			if before.DefinitionDigest == plan.DefinitionDigest {
				t.Fatal("authored selection change must change Definition identity")
			}
			for _, tamper := range []struct {
				name   string
				mutate func(*Plan)
			}{
				{"locator", func(p *Plan) { p.CatalogCandidates[0].Selection.Base = "substituted" }},
				{"selected locator", func(p *Plan) { p.Builds[0].SelectedCatalog.Base = "substituted" }},
				{"selected locator missing", func(p *Plan) { p.Builds[0].SelectedCatalog = nil }},
				{"candidate removed", func(p *Plan) { p.CatalogCandidates = p.CatalogCandidates[1:] }},
				{"candidate reordered", func(p *Plan) {
					p.CatalogCandidates[0], p.CatalogCandidates[1] = p.CatalogCandidates[1], p.CatalogCandidates[0]
				}},
				{"build catalog digest", func(p *Plan) { p.Builds[0].CatalogDigest = "sha256:" + strings.Repeat("9", 64) }},
				{"snapshot changed", func(p *Plan) { p.CatalogCandidates[0].CatalogSnapshot.Metadata.Description = "changed" }},
			} {
				t.Run(tamper.name, func(t *testing.T) {
					raw, err := json.Marshal(plan)
					if err != nil {
						t.Fatal(err)
					}
					var copy Plan
					if err := json.Unmarshal(raw, &copy); err != nil {
						t.Fatal(err)
					}
					tamper.mutate(&copy)
					assertFactoryErrorCode(t, copy.Validate(), e.CodeUnverifiedRef)
				})
			}
			if tc.override == nil {
				plan.Builds[0].AppliedOverlays = []catalog.AppliedOverlay{{Name: "substituted"}}
				assertFactoryErrorCode(t, plan.Validate(), e.CodeUnverifiedRef)
			}
			before.Builds[0].SelectedCatalog = &authored.Catalog{Base: "selected"}
			assertFactoryErrorCode(t, before.Validate(), e.CodeUnverifiedRef)
		})
	}
}
