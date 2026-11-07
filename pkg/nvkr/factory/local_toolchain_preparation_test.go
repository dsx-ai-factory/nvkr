// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func localPreparationFixture(t *testing.T) (*Plan, []LocalToolchainBinding) {
	t.Helper()
	definition, environment, base := vmFactoryInputs(t, "linux/amd64", execution.CapabilityRequired)
	output := definition.Outputs[0]
	definition.Outputs = nil
	stacks := map[string]*catalog.Stack{}
	bindings := []LocalToolchainBinding{}
	for i := range 4 {
		copy, err := catalog.ApplyStack(base.Catalog)
		if err != nil {
			t.Fatal(err)
		}
		copy.Catalog.Metadata.Name = fmt.Sprintf("catalog-%d", i)
		component := copy.Catalog.Components["image"]
		component.ToolchainImages["amd64"] = "sha256:" + strings.Repeat("a", 64)
		copy.Catalog.Components["image"] = component
		copy.Catalog.Components["unselected"] = component
		stack, err := catalog.ApplyStack(copy.Catalog)
		if err != nil {
			t.Fatal(err)
		}
		selected := output
		selected.Name = fmt.Sprintf("arbitrary-output-%d", i)
		selected.Catalog = &authored.Catalog{Base: fmt.Sprintf("catalog-%d", i)}
		definition.Outputs = append(definition.Outputs, selected)
		stacks[selected.Name] = stack
		bindings = append(bindings, LocalToolchainBinding{Catalog: stack.Catalog.Metadata.Name, Arch: "amd64", From: component.ToolchainImages["amd64"], Components: []string{"image"}})
	}
	plan, err := compileWithTargetStacks(t.Context(), definition, environment, base, stacks, "test", "definition.json", "environment.json")
	if err != nil {
		t.Fatal(err)
	}
	return plan, bindings
}

func TestLocalToolchainPreparationCompleteCompiledSelections(t *testing.T) {
	for _, sameImage := range []bool{false, true} {
		t.Run(fmt.Sprint("already-measured=", sameImage), func(t *testing.T) {
			plan, bindings := localPreparationFixture(t)
			preparation, err := CompileLocalToolchainPreparation(t.Context(), plan, bindings, "amd64")
			if err != nil {
				t.Fatal(err)
			}
			image := "sha256:" + strings.Repeat("b", 64)
			if sameImage {
				image = bindings[0].From
			}
			scopes, err := preparation.Scopes(t.Context(), image)
			if err != nil {
				t.Fatal(err)
			}
			if sameImage && len(scopes) != 0 || !sameImage && len(scopes) != 4 {
				t.Fatalf("scopes=%d", len(scopes))
			}
			for _, scope := range scopes {
				if len(scope.Outputs) != 1 || len(scope.Overlay.ToolchainSubstitutions) != 1 || !slices.Equal(scope.Overlay.ToolchainSubstitutions[0].Components, []string{"image"}) {
					t.Fatalf("overbroad scope: %+v", scope)
				}
			}
			// Apply the emitted overlays independently, then use the ordinary compiler.
			stacks := map[string]*catalog.Stack{}
			for _, build := range plan.Builds {
				var overlays []catalog.Overlay
				for _, scope := range scopes {
					if slices.Contains(scope.Outputs, build.Name) {
						overlays = append(overlays, scope.Overlay)
					}
				}
				stack, err := catalog.ApplyStack(build.CatalogSnapshot, overlays...)
				if err != nil {
					t.Fatal(err)
				}
				if stack.Catalog.Components["unselected"].ToolchainImages["amd64"] != bindings[0].From {
					t.Fatal("equal image authorized an unselected component")
				}
				stacks[build.Name] = stack
			}
			selected, err := compileWithTargetStacks(t.Context(), plan.Definition, plan.Environment, &catalog.Stack{Catalog: plan.CatalogSnapshot, Digest: plan.CatalogDigest}, stacks, "test", "selected.json", "environment.json")
			if err != nil {
				t.Fatal(err)
			}
			for _, build := range selected.Builds {
				for _, node := range build.Graph.Nodes {
					if node.Kind == ir.KindBuild && node.Toolchain != image {
						t.Fatalf("fourth/multiple disk consumer omitted: %s/%s", build.Name, node.ID)
					}
				}
			}
			if err := preparation.Verify(t.Context(), selected, image); err != nil {
				t.Fatal(err)
			}
			if !sameImage {
				assertFactoryErrorCode(t, preparation.Verify(t.Context(), plan, image), e.CodeVerifyFailed)
			}
		})
	}
}

func TestLocalToolchainPreparationFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Plan, *[]LocalToolchainBinding)
		code   e.Code
	}{
		{"uncovered fourth catalog", func(_ *Plan, b *[]LocalToolchainBinding) { *b = (*b)[:3] }, e.CodeEngineCapability},
		{"missing exact catalog", func(_ *Plan, b *[]LocalToolchainBinding) { (*b)[3].Catalog = "another" }, e.CodeEngineCapability},
		{"wrong predecessor", func(_ *Plan, b *[]LocalToolchainBinding) { (*b)[3].From = "sha256:" + strings.Repeat("c", 64) }, e.CodeUnverifiedRef},
		{"wrong component despite equal image", func(_ *Plan, b *[]LocalToolchainBinding) { (*b)[3].Components = []string{"unselected"} }, e.CodeEngineCapability},
		{"ambiguous binding", func(_ *Plan, b *[]LocalToolchainBinding) { *b = append(*b, (*b)[0]) }, e.CodeInvalidCatalog},
		{"unclassified bootstrap image", func(p *Plan, _ *[]LocalToolchainBinding) {
			p.Environment.ToolchainAccess = nil
			digest, err := p.Environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			p.EnvironmentDigest = digest
		}, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, bindings := localPreparationFixture(t)
			tc.mutate(plan, &bindings)
			prepared, err := CompileLocalToolchainPreparation(t.Context(), plan, bindings, "amd64")
			assertFactoryErrorCode(t, err, tc.code)
			if prepared != nil {
				t.Fatal("failed admission returned a preparation")
			}
		})
	}
}

func TestLocalToolchainPreparationRejectsImageIDs(t *testing.T) {
	plan, bindings := localPreparationFixture(t)
	preparation, err := CompileLocalToolchainPreparation(t.Context(), plan, bindings, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	for _, image := range []string{"", "local:tag", "registry.example/tool@sha256:" + strings.Repeat("a", 64), "sha256:" + strings.Repeat("A", 64)} {
		t.Run(image, func(t *testing.T) {
			_, err := preparation.Scopes(t.Context(), image)
			assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestLocalToolchainPreparationMultiplePinsInOneCatalog(t *testing.T) {
	definition, environment, base := vmFactoryInputs(t, "linux/amd64", execution.CapabilityRequired)
	base.Catalog.Metadata.Name = "shared"
	component := base.Catalog.Components["image"]
	first := component.ToolchainImages["amd64"]
	component.ToolchainImages = map[string]string{"amd64": "sha256:" + strings.Repeat("d", 64), "arm64": component.ToolchainImages["arm64"]}
	base.Catalog.Components["second"] = component
	other := definition.Outputs[0]
	other.Name = "renamed-second-disk"
	other.Components = []string{"second"}
	definition.Outputs = append(definition.Outputs, other)
	stack, err := catalog.ApplyStack(base.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	bindings := []LocalToolchainBinding{
		{Catalog: "shared", Arch: "amd64", From: first, Components: []string{"image"}},
		{Catalog: "shared", Arch: "amd64", From: component.ToolchainImages["amd64"], Components: []string{"second"}},
	}
	prepared, err := CompileLocalToolchainPreparation(t.Context(), plan, bindings, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	image := "sha256:" + strings.Repeat("e", 64)
	scopes, err := prepared.Scopes(t.Context(), image)
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 1 || len(scopes[0].Outputs) != 2 || len(scopes[0].Overlay.ToolchainSubstitutions) != 2 {
		t.Fatalf("different predecessors collapsed: %+v", scopes)
	}
	applied, err := catalog.ApplyStack(stack.Catalog, scopes[0].Overlay)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"image", "second"} {
		if applied.Catalog.Components[name].ToolchainImages["amd64"] != image {
			t.Fatal("component omitted", name)
		}
	}
	// Returned descriptions cannot mutate the retained binding.
	scopes[0].Overlay.ToolchainSubstitutions[0].Components[0] = "forged"
	scopes, err = prepared.Scopes(t.Context(), image)
	if err != nil || scopes[0].Overlay.ToolchainSubstitutions[0].Components[0] == "forged" {
		t.Fatal("mutable description changed preparation", err)
	}
}

func TestLoadLocalToolchainBindingsRejectsAmbiguity(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"duplicate catalog", `[{"catalog":"one","catalog":"two"}]`},
		{"folded duplicate", `[{"catalog":"one","Catalog":"two"}]`},
		{"unknown field", `[{"wildcard":true}]`},
		{"trailing value", `[] []`},
		{"malformed", `[`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bindings.json")
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadLocalToolchainBindings(t.Context(), path)
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
		})
	}
}
