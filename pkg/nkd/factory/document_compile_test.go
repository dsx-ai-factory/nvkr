// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestCompileDocumentsResolvesCatalogRelativeToDefinition(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	var buildDigest string
	for _, tc := range []struct {
		name                        string
		absolute, override, missing bool
	}{
		{name: "relative base"},
		{name: "absolute base", absolute: true},
		{name: "target override", override: true},
		{name: "missing target catalog", override: true, missing: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			catalogPath := "../testdata/catalogs/kubernetes-layered"
			if tc.absolute {
				catalogPath = filepath.Join(repo, "testdata/catalogs/kubernetes-layered")
			}
			definition, environment := fixtureInputs("documents", "worker", "ubuntu-24.04")
			definition.Catalog.Base = catalogPath
			if tc.override {
				definition.Outputs[0].Catalog = &authored.Catalog{Base: catalogPath}
			}
			if tc.missing {
				definition.Outputs[0].Catalog.Base = "missing"
			}
			definitionPath := filepath.Join("definitions", "fixture.yaml")
			plan, err := CompileDocuments(t.Context(), definitionPath, definition, "environment.yaml", environment, "test")
			if tc.missing {
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
				if plan != nil {
					t.Fatal("failed compilation returned plan")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if plan.DefinitionPath != filepath.ToSlash(definitionPath) || plan.EnvironmentPath != "environment.yaml" {
				t.Fatalf("plan lost authored paths: %+v", plan)
			}
			if len(plan.Builds) != 1 {
				t.Fatalf("wrong build set: %+v", plan.Builds)
			}
			if buildDigest == "" {
				buildDigest = plan.Builds[0].BuildDigest
			}
			if plan.Builds[0].BuildDigest != buildDigest {
				t.Fatal("catalog transport path changed build identity")
			}
			if err := plan.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDocumentCatalogStackBindsOverlayInputAndOutput(t *testing.T) {
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	inputDigest, err := base.SnapshotDigest()
	if err != nil {
		t.Fatal(err)
	}
	component := base.Components["crictl"]
	component.Tag = "v99.0.0"
	overlay := catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "version-change", RequiresCatalogDigest: inputDigest,
		Components: map[string]catalog.ComponentReplacement{"crictl": {Component: &component}}}
	expected, err := catalog.ApplyStack(base, overlay)
	if err != nil {
		t.Fatal(err)
	}
	repo, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name                        string
		absolute, missing, wrongPin bool
	}{
		{name: "relative overlay"},
		{name: "absolute overlay", absolute: true},
		{name: "missing overlay", missing: true},
		{name: "wrong predecessor", wrongPin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			copyOverlay := overlay
			if tc.wrongPin {
				copyOverlay.RequiresCatalogDigest = "sha256:" + strings.Repeat("a", 64)
			}
			raw, err := canonical.Marshal(copyOverlay)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "overlay.json.yaml")
			if !tc.missing {
				if err := os.WriteFile(path, raw, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			selected := filepath.Base(path)
			if tc.absolute {
				selected = path
			}
			stack, err := loadFactoryCatalogStack(t.Context(), filepath.Join(root, "definition.yaml"), authored.Catalog{Base: filepath.Join(repo, "testdata/catalogs/kubernetes-layered"), Overlays: []string{selected}})
			if tc.missing || tc.wrongPin {
				want := e.CodeInvalidCatalog
				if tc.wrongPin {
					want = e.CodeUnverifiedRef
				}
				assertFactoryErrorCode(t, err, want)
				if stack != nil {
					t.Fatal("invalid overlay returned stack")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if stack.Digest != expected.Digest || stack.Digest == inputDigest || len(stack.Applied) != 1 || stack.Applied[0].InputDigest != inputDigest || stack.Applied[0].OutputDigest != stack.Digest {
				t.Fatalf("overlay provenance lost exact state: %+v", stack)
			}
			if stack.Catalog.Components["crictl"].Tag != "v99.0.0" {
				t.Fatal("overlay replacement was not applied")
			}
		})
	}
}

func TestCompilerOwnsAuthoredInputsAndReturnsCompleteBindings(t *testing.T) {
	base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, environment := fixtureInputs("ownership", "worker", "")
	environment.Runtime.Variables = map[string]string{"MODE": "original"}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.LifecycleOutputs) != len(definition.Outputs) || plan.DefinitionDigest == "" || plan.EnvironmentDigest == "" {
		t.Fatal("compiler returned an incomplete plan")
	}
	before, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	definition.Outputs[0].Components[0] = "changed"
	environment.Runtime.Variables["MODE"] = "changed"
	after, err := plan.Digest()
	if err != nil || after != before {
		t.Fatalf("caller changed compiled input ownership: %s %v", after, err)
	}
}

func TestPlanLoadRejectsRetiredCompilerDocuments(t *testing.T) {
	plan := buildOnlyPlan(t, DefaultRunsOn)
	raw, err := canonical.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(string) string
		code   e.Code
	}{
		{"old plan schema", func(raw string) string { return strings.Replace(raw, PlanSchemaVersion, "nkd.dev/run-plan/v3", 1) }, e.CodeInvalidCatalog},
		{"retired compile digest", func(raw string) string {
			return strings.Replace(raw, `"definitionDigest":`, `"compileSpecDigest":"sha256:`+strings.Repeat("a", 64)+`","definitionDigest":`, 1)
		}, e.CodePipelineDrift},
		{"retired execution settings", func(raw string) string {
			return strings.Replace(raw, `"environmentDigest":`, `"executionSettings":{},"environmentDigest":`, 1)
		}, e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(path, []byte(tc.change(string(raw))), 0o600); err != nil {
				t.Fatal(err)
			}
			loaded, err := LoadPlanContext(t.Context(), path)
			assertFactoryErrorCode(t, err, tc.code)
			if loaded != nil {
				t.Fatal("retired plan returned partial plan")
			}
		})
	}
}

func TestCompilerAdmitsCapabilitiesBeforeCatalogIO(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*authored.Definition, *authored.Environment)
		code e.Code
	}{
		{"unpinned signer", func(_ *authored.Definition, environment *authored.Environment) {
			environment.Publication = &authored.Publication{RepositoryPrefix: "ghcr.io/example/nkd", Anonymous: true}
			environment.Signing = &authored.Signing{Mode: SigningModeCosign, Identity: "^fixture$", Issuer: "https://issuer.example"}
		}, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition, environment := fixtureInputs("admission", "worker", "")
			definition.Catalog.Base = "missing-catalog"
			tc.edit(&definition, &environment)
			plan, err := CompileDocuments(t.Context(), "missing-definition.yaml", definition, "environment.yaml", environment, "test")
			assertFactoryErrorCode(t, err, tc.code)
			if plan != nil {
				t.Fatal("rejected compilation returned partial plan")
			}
		})
	}
}

func TestPlanRejectsStalePackageAfterDefinitionIdentityChanges(t *testing.T) {
	for _, field := range []string{"distribution", "component"} {
		t.Run(field, func(t *testing.T) {
			base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
			if err != nil {
				t.Fatal(err)
			}
			stack, err := catalog.ApplyStack(base)
			if err != nil {
				t.Fatal(err)
			}
			definition, environment := fixtureInputs("packaging", "worker", "")
			output := &definition.Outputs[0]
			output.Format = authored.OutputAPT
			output.Components = []string{"containerd"}
			output.APT = &authored.APTOutput{Distribution: "stable", Component: "main"}
			plan, err := Compile(t.Context(), definition, environment, stack, "test")
			if err != nil {
				t.Fatal(err)
			}
			if field == "distribution" {
				plan.Definition.Outputs[0].APT.Distribution = "testing"
			} else {
				plan.Definition.Outputs[0].APT.Component = "extra"
			}
			digest, err := plan.Definition.Digest()
			if err != nil {
				t.Fatal(err)
			}
			plan.DefinitionDigest = digest
			assertFactoryErrorCode(t, plan.Validate(), e.CodePipelineDrift)
		})
	}
}
