// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"reflect"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// compileOutputBuild resolves one exact named output into its owned producer graph.
func compileOutputBuild(target authored.DefinitionOutput, delivery authored.Environment, targetStack *catalog.Stack) (BuildPlan, error) {
	resolved, err := buildgraph.Resolve(targetStack.Catalog, buildgraph.Selection{Components: target.Components, Platform: target.Platform, OutputFormat: target.Format})
	if err != nil {
		return BuildPlan{}, e.Wrap(e.CodeNoMatch, err, "factory build target %q", target.Name)
	}
	graph, err := compileBuildGraph(resolved, delivery)
	if err != nil {
		return BuildPlan{}, err
	}
	buildDigest, err := graph.Digest()
	if err != nil {
		return BuildPlan{}, err
	}
	selected := resolved
	selectedNames := make([]string, 0, len(selected.Components))
	for _, component := range selected.Components {
		selectedNames = append(selectedNames, component.Name)
	}
	sort.Strings(selectedNames)
	snapshot, err := catalog.ApplyStack(targetStack.Catalog)
	if err != nil {
		return BuildPlan{}, err
	}
	return BuildPlan{Name: target.Name,
		CatalogDigest: targetStack.Digest, AppliedOverlays: append([]catalog.AppliedOverlay(nil), targetStack.Applied...),
		BuildDigest: buildDigest, Platform: target.Platform, CatalogSnapshot: snapshot.Catalog,
		Components: selectedNames,
		// The SBOM node bills the SELECTED (built) components: those are exactly the
		// ones with a producer-result carrying an observed digest (ADR-13).
		ResolvedComponents: append([]catalog.NamedComponent(nil), selected.Components...),
		Graph:              graph}, nil
}

// compileBuildGraph owns the Environment transformations of a catalog-derived
// producer graph. Execution metadata remains outside Build identity, but a VM
// class selects a host-architecture-specific toolchain before the graph digest is
// computed because those toolchain bytes can affect the output. Validation uses
// this same derivation before comparing the complete graph.
func compileBuildGraph(resolved *buildgraph.Resolved, environment authored.Environment) (*ir.Graph, error) {
	vmHosts, err := selectVMHostPlatforms(resolved.Components, resolved.Platform, environment.ExecutionClasses)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "VM build cannot select an execution host")
	}
	graph, err := buildgraph.CompileForHostPlatforms(resolved, vmHosts)
	if err != nil {
		return nil, err
	}
	relocateToolchainRegistry(graph, environment.Runtime.ToolchainRegistry)
	if err := admitBuildNetwork(graph, environment.Runtime.BuildNetworkPolicy); err != nil {
		return nil, err
	}
	selected := make(map[string]bool, len(resolved.Components))
	for _, component := range resolved.Components {
		selected[component.Name] = true
	}
	if err := bindBuildExecution(graph, selected, environment.ExecutionClasses); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "build cannot satisfy execution requirements")
	}
	return graph, nil
}

func admitBuildNetwork(graph *ir.Graph, policy string) error {
	if policy == "" {
		policy = authored.BuildNetworkOfflineOnly
	}
	for _, node := range graph.Nodes {
		if node.Kind != ir.KindBuild || node.Network != ir.NetworkDefault || node.BuilderType == ir.BuilderBuildKit {
			continue
		}
		if policy != authored.BuildNetworkProducerDeclared {
			return e.New(e.CodeEngineCapability,
				"build node %q requests network access but Environment policy is %q", node.ID, policy)
		}
	}
	return nil
}

// toolchainMaterializedPrefix names operator-materialized toolchain images. The catalog pins
// them registry-agnostically (bare `nvkr-toolchain-*@sha256:…`) so the default catalog carries no
// operator's registry; the delivery's toolchainRegistry supplies WHERE the operator published
// them. A public toolchain (e.g. golang@sha256:…) never carries this prefix and is left as-is.
const toolchainMaterializedPrefix = "nvkr-toolchain-"

// relocateToolchainRegistry rewrites build and vendoring operator-materialized image refs into
// the delivery-supplied registry, preserving the @sha256 digest (mirroring airgap.Relocate, so
// buildDigest — which strips the prefix — is unchanged). When no registry is configured the
// operator toolchains are left registry-agnostic (they will not pull at runtime until an
// operator names their registry); public toolchains are always left untouched.
func relocateToolchainRegistry(graph *ir.Graph, registry string) {
	registry = strings.TrimSuffix(strings.TrimSpace(registry), "/")
	if registry == "" {
		return
	}
	for i := range graph.Nodes {
		node := &graph.Nodes[i]
		switch node.Kind {
		case ir.KindBuild:
			node.Toolchain = relocatedToolchainReference(node.Toolchain, registry)
		case ir.KindFetch:
			node.VendorImage = relocatedToolchainReference(node.VendorImage, registry)
		}
	}
}

func relocatedToolchainReference(reference, registry string) string {
	name, _, _ := strings.Cut(reference, "@")
	// Only bare operator toolchains await relocation; public and qualified refs stay intact.
	if strings.Contains(name, "/") || !strings.HasPrefix(name, toolchainMaterializedPrefix) {
		return reference
	}
	return registry + "/" + reference
}

func (plan Plan) validatePlanBuilds() error {
	previous := ""
	for _, applied := range plan.AppliedOverlays {
		if !canonicalName.MatchString(applied.Name) || !canonical.IsDigest(applied.OverlayDigest) ||
			!canonical.IsDigest(applied.InputDigest) || !canonical.IsDigest(applied.OutputDigest) {
			return e.New(e.CodeUnpinnedSource, "factory plan has an invalid applied-overlay binding")
		}
		if previous != "" && applied.InputDigest != previous {
			return e.New(e.CodeUnverifiedRef, "factory plan overlay %q is not chained to its predecessor", applied.Name)
		}
		previous = applied.OutputDigest
	}
	if previous != "" && previous != plan.CatalogDigest {
		return e.New(e.CodeUnverifiedRef, "factory plan overlay chain does not end at catalogDigest")
	}
	if len(plan.Builds) == 0 {
		return e.New(e.CodeInvalidCatalog, "factory plan has no builds")
	}
	seen := map[string]bool{}
	resolvedSources := map[string]bool{}
	for _, buildPlan := range plan.Builds {
		if !safeGraphID(buildPlan.Name) || seen[buildPlan.Name] || buildPlan.Graph == nil {
			return e.New(e.CodeInvalidCatalog, "factory plan build target %q is invalid or repeated", buildPlan.Name)
		}
		seen[buildPlan.Name] = true
		if !canonical.IsDigest(buildPlan.CatalogDigest) {
			return e.New(e.CodeUnpinnedSource, "factory build target %q has no exact catalog digest", buildPlan.Name)
		}
		targetCatalog := ""
		for _, applied := range buildPlan.AppliedOverlays {
			if !canonical.IsDigest(applied.InputDigest) || !canonical.IsDigest(applied.OutputDigest) ||
				!canonical.IsDigest(applied.OverlayDigest) || !canonicalName.MatchString(applied.Name) ||
				(targetCatalog != "" && applied.InputDigest != targetCatalog) {
				return e.New(e.CodeUnverifiedRef, "factory build target %q has an invalid catalog overlay chain", buildPlan.Name)
			}
			targetCatalog = applied.OutputDigest
		}
		if targetCatalog != "" && targetCatalog != buildPlan.CatalogDigest {
			return e.New(e.CodeUnverifiedRef, "factory build target %q catalog overlay chain ends at the wrong digest", buildPlan.Name)
		}
		if err := buildPlan.Graph.Validate(); err != nil {
			return err
		}
		digest, err := buildPlan.Graph.Digest()
		if err != nil {
			return err
		}
		if digest != buildPlan.BuildDigest || buildPlan.Platform != buildPlan.Graph.Platform {
			return e.New(e.CodeUnverifiedRef, "factory build target %q graph identity does not match its binding", buildPlan.Name)
		}
		if len(buildPlan.Components) == 0 {
			return e.New(e.CodeInvalidCatalog, "factory build target %q selects no components", buildPlan.Name)
		}
		if !sort.StringsAreSorted(buildPlan.Components) || hasAdjacentDuplicate(buildPlan.Components) {
			return e.New(e.CodeInvalidCatalog, "factory build target %q components are not canonical", buildPlan.Name)
		}

		selection, ok := plan.selectionForBuild(buildPlan.Name)
		if !ok || selection.Platform != buildPlan.Platform {
			return e.New(e.CodeUnverifiedRef, "native build %q graph identity is not selected by its Definition", buildPlan.Name)
		}
		outputFormat := selection.Format
		if err := validateBuildCatalog(buildPlan, outputFormat, plan.Environment); err != nil {
			return err
		}
		for _, node := range buildPlan.Graph.Nodes {
			if node.Kind == ir.KindFetch && node.Repo != "" {
				resolvedSources[node.Repo] = true
			}
		}
	}
	// Catalog loading re-derives every declared pin set from its pinned Git tree,
	// including acquired-only outputs whose executable graph has no fetch node.
	// Admit the same exact repositories used by catalog revalidation credentials.
	for source := range plan.catalogPinSetRepositories() {
		resolvedSources[source] = true
	}
	for source := range plan.Environment.SourceAccess {
		if !resolvedSources[source] {
			return e.New(e.CodeInvalidCatalog,
				"execution settings sourceAccess %q does not match a resolved fetch or catalog pin-set source", source)
		}
	}
	return nil
}

// catalogPinSetRepositories is shared by source-access admission and the scoped
// GitHub catalog revalidation steps. Ordinary unselected catalog sources are not
// credential consumers. Plan validation binds these snapshots before admission.
func (plan *Plan) catalogPinSetRepositories() map[string]bool {
	repositories := map[string]bool{}
	collect := func(snapshot *catalog.Catalog) {
		if snapshot == nil {
			return
		}
		for _, source := range snapshot.Sources {
			if source.PinSet != nil {
				repositories[source.Repo] = true
			}
		}
	}
	collect(plan.CatalogSnapshot)
	for _, build := range plan.Builds {
		collect(build.CatalogSnapshot)
	}
	return repositories
}

// validateBuildCatalog binds the complete executable graph to its catalog and
// Environment. Logical identity alone excludes transport and execution metadata.
func validateBuildCatalog(build BuildPlan, outputFormat string, environment authored.Environment) error {
	if build.CatalogSnapshot == nil {
		return e.New(e.CodeUnverifiedRef, "output %q has no catalog snapshot", build.Name)
	}
	digest, err := build.CatalogSnapshot.SnapshotDigest()
	if err != nil {
		return err
	}
	if digest != build.CatalogDigest {
		return e.New(e.CodeUnverifiedRef, "output %q catalog snapshot differs", build.Name)
	}
	selection := buildgraph.Selection{Components: build.Components, Platform: build.Platform, OutputFormat: outputFormat}
	selected, err := buildgraph.Resolve(build.CatalogSnapshot, selection)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(selected.Components, build.ResolvedComponents) {
		return e.New(e.CodeUnverifiedRef, "output %q resolved producers differ from catalog", build.Name)
	}
	graph, err := compileBuildGraph(selected, environment)
	if err != nil {
		return err
	}
	graphDigest, err := graph.Digest()
	if err != nil {
		return err
	}
	if graphDigest != build.BuildDigest {
		return e.New(e.CodeUnverifiedRef, "output %q graph differs from catalog producers", build.Name)
	}
	expected, err := graph.CanonicalJSON()
	if err != nil {
		return err
	}
	actual, err := build.Graph.CanonicalJSON()
	if err != nil {
		return err
	}
	if !bytes.Equal(actual, expected) {
		return e.New(e.CodePipelineDrift, "output %q executable graph differs from its catalog and environment", build.Name)
	}
	return nil
}
