// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// CompileDocuments resolves authored catalog paths and returns the complete Run
// plan, including required outputs, environment routes, and production evidence.
func CompileDocuments(ctx context.Context, definitionPath string, definition authored.Definition,
	environmentPath string, delivery authored.Environment, generatorVersion string,
) (*Plan, error) {
	return compileDocumentsWithLoader(ctx, definitionPath, definition, environmentPath, delivery, generatorVersion,
		func(ctx context.Context, spec authored.Catalog) (*catalog.Stack, error) {
			return loadFactoryCatalogStack(ctx, definitionPath, spec)
		})
}

func compileDocumentsWithLoader(ctx context.Context, definitionPath string, definition authored.Definition,
	environmentPath string, delivery authored.Environment, generatorVersion string,
	loadStack func(context.Context, authored.Catalog) (*catalog.Stack, error),
) (*Plan, error) {
	return compileDocumentsFor(ctx, definitionPath, definition, environmentPath, delivery, generatorVersion, loadStack, nil)
}

// compileDocumentsFor compiles a plain Definition (nil record) or a composite's
// effective Definition, binding the composition record before Plan validation.
func compileDocumentsFor(ctx context.Context, definitionPath string, definition authored.Definition,
	environmentPath string, delivery authored.Environment, generatorVersion string,
	loadStack func(context.Context, authored.Catalog) (*catalog.Stack, error), record *CompositionRecord,
) (*Plan, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "definition compilation requires a context")
	}
	if err := admitInputs(definition, delivery); err != nil {
		return nil, err
	}
	if err := admitDefinitionForm(delivery, record); err != nil {
		return nil, err
	}
	stack, err := loadStack(ctx, definition.Catalog)
	if err != nil {
		return nil, err
	}
	targetStacks := make(map[string]*catalog.Stack)
	for _, target := range definition.Outputs {
		if target.Catalog == nil {
			continue
		}
		targetStack, err := loadStack(ctx, *target.Catalog)
		if err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "loading catalog stack for build %q", target.Name)
		}
		targetStacks[target.Name] = targetStack
	}
	return compileWithTargetStacksFor(ctx, definition, delivery, stack, targetStacks, generatorVersion,
		filepath.ToSlash(filepath.Clean(definitionPath)), filepath.ToSlash(filepath.Clean(environmentPath)), record)

}

func loadFactoryCatalogStack(ctx context.Context, definitionPath string, catalogSpec authored.Catalog) (*catalog.Stack, error) {
	basePath := catalogSpec.Base
	if !filepath.IsAbs(basePath) {
		basePath = filepath.Join(filepath.Dir(definitionPath), basePath)
	}
	provider := catalog.Dir(basePath)
	base, err := catalog.LoadContext(ctx, provider)
	if err != nil {
		return nil, err
	}
	overlays := make([]catalog.Overlay, 0, len(catalogSpec.Overlays))
	for _, overlayPath := range catalogSpec.Overlays {
		if !filepath.IsAbs(overlayPath) {
			overlayPath = filepath.Join(filepath.Dir(definitionPath), overlayPath)
		}
		overlay, err := catalog.LoadOverlayContext(ctx, overlayPath)
		if err != nil {
			return nil, err
		}
		overlays = append(overlays, overlay)
	}
	stack, err := catalog.ApplyStack(base, overlays...)
	if err != nil {
		return nil, err
	}
	return stack, nil
}

// Compile derives the complete Run plan from admitted inputs and a resolved catalog.
// Catalog overrides require CompileDocuments so each path has an explicit base.
func Compile(ctx context.Context, definition authored.Definition, delivery authored.Environment, stack *catalog.Stack, generatorVersion string) (*Plan, error) {
	for _, target := range definition.Outputs {
		if target.Catalog != nil {
			return nil, e.New(e.CodeUsage,
				"factory build target %q has a catalog override; compile it through CompileDocuments", target.Name)
		}
	}
	return compileWithTargetStacks(ctx, definition, delivery, stack, nil, generatorVersion, "", "")
}

func compileWithTargetStacks(ctx context.Context, definition authored.Definition, delivery authored.Environment, stack *catalog.Stack,
	targetStacks map[string]*catalog.Stack, generatorVersion, definitionPath, environmentPath string) (*Plan, error) {
	return compileWithTargetStacksFor(ctx, definition, delivery, stack, targetStacks, generatorVersion, definitionPath, environmentPath, nil)
}

func compileWithTargetStacksFor(ctx context.Context, definition authored.Definition, delivery authored.Environment, stack *catalog.Stack,
	targetStacks map[string]*catalog.Stack, generatorVersion, definitionPath, environmentPath string, record *CompositionRecord) (*Plan, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "factory compilation requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "factory compilation canceled")
	}
	if stack == nil || stack.Catalog == nil {
		return nil, e.New(e.CodeInvalidCatalog, "factory compilation requires a resolved catalog stack")
	}
	if err := admitInputs(definition, delivery); err != nil {
		return nil, err
	}
	if err := admitDefinitionForm(delivery, record); err != nil {
		return nil, err
	}
	// Hold independent authored input values so callers cannot change executable identity.
	definition = definition.Clone()
	delivery = delivery.Clone()
	definitionDigest, err := definition.Digest()
	if err != nil {
		return nil, err
	}
	environmentDigest, err := delivery.Digest()
	if err != nil {
		return nil, err
	}
	snapshot, err := catalog.ApplyStack(stack.Catalog)
	if err != nil {
		return nil, err
	}
	tools, err := selectedTools(definition, delivery, stack.Catalog)
	if err != nil {
		return nil, err
	}
	plan := &Plan{Actions: cloneActionPins(stack.Catalog.Actions), SchemaVersion: PlanSchemaVersion, Name: definition.Metadata.Name,
		GeneratorVersion: generatorVersion, Qualification: planTier(delivery.Signing),
		DefinitionDigest: definitionDigest, EnvironmentDigest: environmentDigest, Definition: definition, Environment: delivery,
		DefinitionPath: definitionPath, EnvironmentPath: environmentPath,
		CatalogDigest: stack.Digest, CatalogSnapshot: snapshot.Catalog, AppliedOverlays: append([]catalog.AppliedOverlay(nil), stack.Applied...),
		Graph: ArtifactGraph{SchemaVersion: ArtifactGraphSchema}}
	for _, target := range definition.Outputs {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "factory compilation canceled")
		}
		targetStack := stack
		if selected := targetStacks[target.Name]; selected != nil {
			targetStack = selected
		}

		for _, selection := range nativeOutputSelections(target) {
			build, err := compileOutputBuild(selection, delivery, targetStack)
			if err != nil {
				return nil, err
			}
			plan.Builds = append(plan.Builds, build)
		}
	}
	builds := make(map[string]BuildPlan, len(plan.Builds))
	for _, build := range plan.Builds {
		builds[build.Name] = build
	}
	for _, target := range definition.Outputs {
		required, err := outputRequirements(target, builds)
		if err != nil {
			return nil, err
		}
		plan.LifecycleOutputs = append(plan.LifecycleOutputs, required)
	}
	packageSpecs, targets, routes, err := derivePublication(definition, delivery, plan.Builds)
	if err != nil {
		return nil, err
	}
	plan.Targets, plan.Routes = targets, routes
	plan.Graph, err = compileArtifactGraph(*plan, tools, packageSpecs)
	if err != nil {
		return nil, err
	}
	if record != nil {
		if plan.DefinitionDigest, err = authored.CompositeDigest(record.Metadata, record.Members); err != nil {
			return nil, err
		}
		plan.SchemaVersion, plan.Composition = CompositePlanSchemaVersion, record.Clone()
	}
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return plan, nil
}
