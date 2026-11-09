// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// LocalToolchainBinding declares compatibility, not image availability. Catalog
// is an explicit catalog metadata selector; compilation binds each match to its
// complete validated snapshot and exact predecessor pin. Equal image strings
// or catalog names alone never authorize another component.
type LocalToolchainBinding struct {
	Catalog    string   `json:"catalog"`
	Arch       string   `json:"arch"`
	From       string   `json:"from"`
	Components []string `json:"components"`
}

// LocalToolchainScope is one catalog overlay and its exact selected outputs.
type LocalToolchainScope struct {
	Outputs []string        `json:"outputs"`
	Overlay catalog.Overlay `json:"overlay"`
}

// LocalToolchainPreparation retains admitted input bytes privately. It describes
// local diagnostic substitutions; it is neither a runtime nor release receipt.
type LocalToolchainPreparation struct {
	baseline *Plan
	groups   []LocalToolchainScope
}

// LoadLocalToolchainBindings strictly reads a bounded compatibility declaration.
func LoadLocalToolchainBindings(ctx context.Context, path string) ([]LocalToolchainBinding, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "local toolchain bindings require a context")
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "local-toolchain-bindings", MaxBytes: 1 << 20})
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "open local toolchain bindings")
	}
	raw, err := held.ReadAllContext(ctx, 1<<20)
	closeErr := held.Close()
	if err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "read local toolchain bindings")
	}
	if closeErr != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, closeErr, "close local toolchain bindings")
	}
	if err := canonical.ValidateFoldedJSONDocument(raw); err != nil {
		return nil, e.Wrap(e.CodeInvalidCatalog, err, "invalid local toolchain binding document")
	}
	var bindings []LocalToolchainBinding
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bindings); err != nil {
		return nil, e.New(e.CodeInvalidCatalog, "invalid local toolchain bindings")
	}
	return bindings, nil
}

// CompileLocalToolchainPreparation checks complete image coverage before callers
// build a replacement. A speculative in-memory recompile uses an inert local ID;
// the ordinary ImageUses owner admits all remaining consumers and access routes.
func CompileLocalToolchainPreparation(ctx context.Context, plan *Plan, bindings []LocalToolchainBinding, arch string) (*LocalToolchainPreparation, error) {
	if ctx == nil || plan == nil || (arch != "amd64" && arch != "arm64") {
		return nil, e.New(e.CodeUsage, "local toolchain preparation requires a Plan, context and native architecture")
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	if plan.Composition != nil || plan.Environment.Publication != nil || plan.Environment.Signing != nil {
		return nil, e.New(e.CodeEngineCapability, "local toolchain preparation requires a plain unsigned diagnostic Definition")
	}
	raw, err := canonical.Marshal(plan)
	if err != nil {
		return nil, err
	}
	var baseline Plan
	if err := json.Unmarshal(raw, &baseline); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "retain preparation Plan")
	}
	p := &LocalToolchainPreparation{baseline: &baseline}
	for _, node := range baseline.Graph.Nodes {
		use, err := baseline.executionForProducer(node)
		if err != nil {
			return nil, err
		}
		if use.Container == localPreparationProbe {
			return nil, e.New(e.CodeEngineCapability, "local preparation cannot use its reserved probe ID as an input")
		}
	}
	rules := map[string]LocalToolchainBinding{}
	for _, binding := range bindings {
		if binding.Catalog == "" || strings.TrimSpace(binding.Catalog) != binding.Catalog || (binding.Arch != "amd64" && binding.Arch != "arm64") || len(binding.Components) == 0 || !slices.IsSorted(binding.Components) {
			return nil, e.New(e.CodeInvalidCatalog, "local toolchain binding requires catalog, architecture and sorted explicit components")
		}
		probe := catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "local-binding", RequiresCatalogDigest: localPreparationProbe, ToolchainSubstitutions: []catalog.ToolchainSubstitution{{Arch: binding.Arch, From: binding.From, To: localPreparationProbe, Components: binding.Components}}}
		if err := probe.Validate(); err != nil {
			return nil, err
		}
		for _, component := range binding.Components {
			key := binding.Catalog + "\x00" + binding.Arch + "\x00" + component
			if _, exists := rules[key]; exists {
				return nil, e.New(e.CodeInvalidCatalog, "ambiguous local toolchain binding for %s/%s", binding.Catalog, component)
			}
			rules[key] = binding
		}
	}
	groups := map[string]*LocalToolchainScope{}
	for _, build := range baseline.Builds {
		for _, name := range build.Components {
			rule, ok := rules[build.CatalogSnapshot.Metadata.Name+"\x00"+arch+"\x00"+name]
			if !ok {
				continue
			}
			component := build.CatalogSnapshot.Components[name]
			if !component.FromSource || component.ToolchainImages[arch] != rule.From {
				return nil, e.New(e.CodeUnverifiedRef, "local toolchain predecessor differs for %s/%s: expected %q, observed %q", build.Name, name, rule.From, component.ToolchainImages[arch])
			}
			group := groups[build.CatalogDigest]
			if group == nil {
				group = &LocalToolchainScope{Overlay: catalog.Overlay{APIVersion: catalog.OverlayAPIVersion, Kind: catalog.OverlayKind, Name: "local-toolchain-" + arch, RequiresCatalogDigest: build.CatalogDigest}}
				groups[build.CatalogDigest] = group
			}
			if !slices.Contains(group.Outputs, build.Name) {
				group.Outputs = append(group.Outputs, build.Name)
			}
			index := slices.IndexFunc(group.Overlay.ToolchainSubstitutions, func(s catalog.ToolchainSubstitution) bool { return s.From == rule.From })
			if index < 0 {
				group.Overlay.ToolchainSubstitutions = append(group.Overlay.ToolchainSubstitutions, catalog.ToolchainSubstitution{Arch: arch, From: rule.From})
				index = len(group.Overlay.ToolchainSubstitutions) - 1
			}
			substitution := &group.Overlay.ToolchainSubstitutions[index]
			if !slices.Contains(substitution.Components, name) {
				substitution.Components = append(substitution.Components, name)
			}
		}
	}
	for _, key := range sortedKeys(groups) {
		group := groups[key]
		slices.Sort(group.Outputs)
		slices.SortFunc(group.Overlay.ToolchainSubstitutions, func(a, b catalog.ToolchainSubstitution) int { return strings.Compare(a.From, b.From) })
		for i := range group.Overlay.ToolchainSubstitutions {
			slices.Sort(group.Overlay.ToolchainSubstitutions[i].Components)
		}
		p.groups = append(p.groups, *group)
	}
	candidate, _, err := p.substitute(ctx, localPreparationProbe)
	if err != nil {
		return nil, err
	}
	uses, err := candidate.ImageUses("local")
	if err != nil {
		return nil, err
	}
	for _, use := range uses {
		if use.Route == ImageMeasuredLocal && use.Reference != localPreparationProbe {
			return nil, e.New(e.CodeEngineCapability, "local image consumer %q has no declared materialization binding", use.ConsumerID)
		}
	}
	return p, nil
}

const localPreparationProbe = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

// Scopes produces predecessor-bound overlays for the measured image ID. Runtime
// bytes must still be authenticated by the local image execution owner.
func (p *LocalToolchainPreparation) Scopes(ctx context.Context, image string) ([]LocalToolchainScope, error) {
	_, scopes, err := p.substitute(ctx, image)
	return scopes, err
}

func (p *LocalToolchainPreparation) substitute(ctx context.Context, image string) (*Plan, []LocalToolchainScope, error) {
	if p == nil || p.baseline == nil || !canonical.IsDigest(image) {
		return nil, nil, e.New(e.CodeVerifyFailed, "local toolchain replacement requires an exact measured image ID")
	}
	if ctx == nil {
		return nil, nil, e.New(e.CodeUsage, "local toolchain preparation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, e.Wrap(e.CodeTimeout, err, "local toolchain preparation canceled")
	}
	baseline := p.baseline
	stacks := map[string]*catalog.Stack{}
	scopes := []LocalToolchainScope{}
	for _, group := range p.groups {
		overlay := group.Overlay
		overlay.ToolchainSubstitutions = nil
		for _, original := range group.Overlay.ToolchainSubstitutions {
			if original.From == image {
				continue
			}
			original.To = image
			original.Components = slices.Clone(original.Components)
			overlay.ToolchainSubstitutions = append(overlay.ToolchainSubstitutions, original)
		}
		if len(overlay.ToolchainSubstitutions) == 0 {
			continue
		}
		index := slices.IndexFunc(baseline.Builds, func(b BuildPlan) bool { return b.CatalogDigest == overlay.RequiresCatalogDigest })
		stack, err := catalog.ApplyStack(baseline.Builds[index].CatalogSnapshot, overlay)
		if err != nil {
			return nil, nil, err
		}
		for _, output := range group.Outputs {
			buildIndex := slices.IndexFunc(baseline.Builds, func(b BuildPlan) bool { return b.Name == output })
			stacks[output] = &catalog.Stack{Catalog: stack.Catalog, Digest: stack.Digest, Applied: append(slices.Clone(baseline.Builds[buildIndex].AppliedOverlays), stack.Applied...)}
		}
		scopes = append(scopes, LocalToolchainScope{Outputs: slices.Clone(group.Outputs), Overlay: overlay})
	}
	for _, build := range baseline.Builds {
		if stacks[build.Name] == nil {
			stacks[build.Name] = &catalog.Stack{Catalog: build.CatalogSnapshot, Digest: build.CatalogDigest, Applied: build.AppliedOverlays}
		}
	}
	definition := baseline.Definition.Clone()
	// Explicit target stacks keep default-catalog siblings independently scoped.
	for i := range definition.Outputs {
		if definition.Outputs[i].Catalog == nil {
			selection := definition.Catalog
			definition.Outputs[i].Catalog = &selection
		}
	}
	base := &catalog.Stack{Catalog: baseline.CatalogSnapshot, Digest: baseline.CatalogDigest, Applied: baseline.AppliedOverlays}
	candidate, err := compileWithTargetStacks(ctx, definition, baseline.Environment, base, stacks, baseline.GeneratorVersion, baseline.DefinitionPath, baseline.EnvironmentPath, baseline.RuntimeSourceCommit)
	return candidate, scopes, err
}

// Verify recompiles the expected substitutions and checks every named output,
// complete catalog, image consumer and unchanged authored output contract. A
// valid digest-shaped ID alone is not evidence that a substitution was applied.
func (p *LocalToolchainPreparation) Verify(ctx context.Context, selected *Plan, image string) error {
	expected, _, err := p.substitute(ctx, image)
	if err != nil {
		return err
	}
	if selected == nil {
		return e.New(e.CodeVerifyFailed, "missing recompiled local Plan")
	}
	if err := selected.Validate(); err != nil {
		return err
	}
	if selected.EnvironmentDigest != expected.EnvironmentDigest || selected.RuntimeSourceCommit != expected.RuntimeSourceCommit || selected.CatalogDigest != expected.CatalogDigest || !reflect.DeepEqual(selected.AppliedOverlays, expected.AppliedOverlays) || selected.Composition != nil || len(selected.Builds) != len(expected.Builds) {
		return e.New(e.CodeVerifyFailed, "local toolchain preparation changed Environment or output membership")
	}
	actualDefinition := selected.Definition.Clone()
	expectedDefinition := expected.Definition.Clone()
	actualDefinition.Catalog = expectedDefinition.Catalog
	for i := range actualDefinition.Outputs {
		actualDefinition.Outputs[i].Catalog = nil
	}
	for i := range expectedDefinition.Outputs {
		expectedDefinition.Outputs[i].Catalog = nil
	}
	if !reflect.DeepEqual(actualDefinition, expectedDefinition) {
		return e.New(e.CodeVerifyFailed, "local toolchain preparation changed output contracts")
	}
	for i, build := range selected.Builds {
		want := expected.Builds[i]
		if build.Name != want.Name || build.CatalogDigest != want.CatalogDigest || build.BuildDigest != want.BuildDigest || !reflect.DeepEqual(build.AppliedOverlays, want.AppliedOverlays) {
			return e.New(e.CodeVerifyFailed, "local substitution coverage differs for output %q", build.Name)
		}
	}
	actualUses, err := selected.ImageUses("local")
	if err != nil {
		return err
	}
	expectedUses, err := expected.ImageUses("local")
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actualUses, expectedUses) {
		return e.New(e.CodeVerifyFailed, "local image consumer coverage changed after recompilation")
	}
	return nil
}

// DescribeLocalToolchainPreparation is the common CLI adapter boundary. Without
// image it performs admission only; with selected it verifies the final Plan.
func DescribeLocalToolchainPreparation(ctx context.Context, planPath, bindingsPath, arch, image, selectedPath string) ([]LocalToolchainScope, error) {
	plan, err := LoadPlanContext(ctx, planPath)
	if err != nil {
		return nil, err
	}
	bindings, err := LoadLocalToolchainBindings(ctx, bindingsPath)
	if err != nil {
		return nil, err
	}
	preparation, err := CompileLocalToolchainPreparation(ctx, plan, bindings, arch)
	if err != nil {
		return nil, err
	}
	if image == "" {
		if selectedPath != "" {
			return nil, e.New(e.CodeUsage, "selected Plan verification requires an image ID")
		}
		image = localPreparationProbe
	}
	if selectedPath != "" {
		selected, err := LoadPlanContext(ctx, selectedPath)
		if err != nil {
			return nil, err
		}
		if err := preparation.Verify(ctx, selected, image); err != nil {
			return nil, err
		}
	}
	scopes, err := preparation.Scopes(ctx, image)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "prepare local %s substitutions", arch)
	}
	return scopes, nil
}
