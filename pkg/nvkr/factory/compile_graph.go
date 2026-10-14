// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// compileArtifactGraph is the single derivation of executable scheduler nodes,
// artifacts and their exact edges from admitted inputs and resolved producers.
// It ignores the supplied Graph and never mutates the caller's Plan.
func compileArtifactGraph(plan Plan, tools map[string]catalog.ToolPin, packageSpecs []packageSpec) (ArtifactGraph, error) {
	definition, delivery := plan.Definition, plan.Environment
	runsOn := delivery.RunsOn
	if runsOn == "" {
		runsOn = DefaultRunsOn
	}
	plan.Graph = ArtifactGraph{SchemaVersion: ArtifactGraphSchema}
	artifacts := map[string]Artifact{}
	nodes := map[string]Node{}
	for _, name := range sortedKeys(tools) {
		node, artifact := compileToolOperation(name, tools[name], runsOn)
		nodes[node.ID] = node
		artifacts[artifact.ID] = artifact
	}
	builds := make(map[string]BuildPlan, len(plan.Builds))
	for _, build := range plan.Builds {
		builds[build.Name] = build
	}
	for _, output := range definition.Outputs {
		for _, target := range nativeOutputSelections(output) {
			build, ok := builds[target.Name]
			if !ok || build.Graph == nil {
				return ArtifactGraph{}, e.New(e.CodePipelineDrift, "output %q has no resolved producer graph", target.Name)
			}
			selected := make(map[string]bool, len(build.Components))
			for _, name := range build.Components {
				selected[name] = true
			}
			if err := addBuildDataflow(target, runsOn, build.Graph, selected, delivery.UtilityContainer, delivery.UtilityPlatform, delivery.Publication != nil || (target.Format == "debian-package" && definition.SBOMTool != ""), nodes, artifacts); err != nil {
				return ArtifactGraph{}, err
			}
			if target.Format == "debian-package" && definition.SBOMTool != "" {
				profile, err := debianInspectionProfile(build, delivery, tools[definition.SBOMTool])
				if err != nil {
					return ArtifactGraph{}, err
				}
				node := nodes[target.Name+"/sbom"]
				node.Container, node.Platform = profile.Image, build.Platform
				nodes[node.ID] = node
			}
			if target.Format == "disk-image" && delivery.Publication != nil {
				if err := admitDiskPublicationProducer(build); err != nil {
					return ArtifactGraph{}, err
				}
				if diskBuildProvenance(&plan) {
					continue
				}
				host, profile, err := diskPublicationInspectionProfile(build, delivery)
				if err != nil {
					return ArtifactGraph{}, err
				}
				node := nodes[target.Name+"/sbom"]
				node.Container, node.Platform = profile.Image, host
				nodes[node.ID] = node
			}
		}
	}
	if definition.SBOMTool != "" {
		if _, ok := tools[definition.SBOMTool]; !ok {
			return ArtifactGraph{}, e.New(e.CodeProvenanceMissing, "SBOM scanner is not a pinned tool")
		}
	}
	for id, node := range nodes {
		if node.Kind != NodeSBOM {
			continue
		}
		disk := plan.hasCompiledDiskTarget(node.BuildTarget, artifacts)
		if !disk && definition.SBOMTool == "" {
			continue
		}
		components := map[string]bool{}
		for _, input := range node.Inputs {
			if artifacts[input].Kind == "producer-result" {
				components[artifacts[input].Component] = true
			}
		}
		for _, artifact := range artifacts {
			if artifact.BuildTarget == node.BuildTarget && components[artifact.Component] && strings.HasPrefix(artifact.ID, "artifact/") {
				node.Inputs = append(node.Inputs, artifact.ID)
			}
		}
		if !disk {
			node.Inputs = append(node.Inputs, "tool/"+definition.SBOMTool)
		}
		node.Inputs = uniqueSorted(node.Inputs)
		nodes[id] = node
	}

	packages, err := orderPackageSpecs(packageSpecs)
	if err != nil {
		return ArtifactGraph{}, err
	}
	for _, spec := range packages {
		inputs, err := expandSelectors(spec.Inputs, artifacts)
		if err != nil {
			return ArtifactGraph{}, e.Wrap(e.CodeInvalidCatalog, err, "factory package %q", spec.Name)
		}
		if err := validatePackageInputs(spec, inputs, artifacts); err != nil {
			return ArtifactGraph{}, err
		}
		if spec.Kind == PackageImage {
			for _, inputID := range append([]string(nil), inputs...) {
				input := artifacts[inputID]
				switch nodes[input.Producer].Kind {
				case NodeBuild:
				case NodeAcquire:
					if spec.ImageIndex != nil {
						return ArtifactGraph{}, e.New(e.CodeEngineCapability, "aggregate image requires built OCI exports")
					}
					inputs = append(inputs, "result/"+input.BuildTarget+"/"+input.Component)
				default:
					return ArtifactGraph{}, e.New(e.CodeEngineCapability, "image package %q requires a build or acquired-image producer", spec.Name)
				}
			}
		}
		if spec.Kind == PackageHelm {
			for _, id := range inputs {
				input := artifacts[id]
				producer := nodes[input.Producer]
				if input.Kind == "raw" && producer.Kind != NodeAcquire {
					return ArtifactGraph{}, e.New(e.CodeInvalidCatalog, "Helm package %q requires acquired chart bytes for raw input", spec.Name)
				}
				if input.Kind == "tarball" && producer.Kind != NodeBuild {
					return ArtifactGraph{}, e.New(e.CodeInvalidCatalog, "Helm package %q requires a source build for repackaging or image binding", spec.Name)
				}
			}
			// Acquired packaging authenticates its exact producer result before
			// creating output. Transport it explicitly on every execution engine.
			if artifacts[inputs[0]].Kind == "raw" {
				input := artifacts[inputs[0]]
				inputs = append(inputs, "result/"+input.BuildTarget+"/"+input.Component)
			}
		}
		// Packaging is explicitly gated by each target's terminal observation as a
		// separate input, rather than hiding the policy edge in scheduler needs.
		inputs = append(inputs, observationInputs(inputs, artifacts)...)
		if spec.Kind == PackageDiskImage {
			inputs = append(inputs, "sbom/"+spec.Name)
			// Every contributing material subject/record remains explicit. Root
			// reconciliation does not waive its independent build-output slots.
			for id, artifact := range artifacts {
				if artifact.BuildTarget == spec.Name && (artifact.Kind == "producer-result" || strings.HasPrefix(id, "artifact/")) {
					inputs = append(inputs, id)
				}
			}
		}
		if spec.ImageIndex != nil {
			for _, child := range spec.ImageIndex.Children {
				inputs = append(inputs, child.ResultArtifactID, child.SBOMArtifactID)
			}
		}
		if spec.Kind == PackageOCI {
			inputs = append(inputs, packageProducerResults(inputs, nodes, artifacts)...)
			inputs = append(inputs, publicationSBOMInputs(inputs, nodes, artifacts)...)
		}
		inputs = uniqueSorted(inputs)
		nodeID := "package/" + spec.Name
		artifactID := "package/" + spec.Name
		nodes[nodeID] = Node{ID: nodeID, Kind: NodePackage, PackageKind: spec.Kind,
			HelmImage:    spec.HelmImage,
			ImageIndex:   spec.ImageIndex.Clone(),
			Distribution: spec.Distribution, PackageComponent: spec.Component,
			Inputs: inputs, Outputs: []string{artifactID}, Network: ir.NetworkNone, RunsOn: runsOn,
			Container: delivery.UtilityContainer, Platform: delivery.UtilityPlatform}
		artifacts[artifactID] = Artifact{ID: artifactID, Name: spec.Name, Kind: spec.Kind,
			MediaType: packageSpecMediaType(spec), Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", "packages", spec.Name)), Producer: nodeID}
	}

	routes := append([]Route(nil), plan.Routes...)
	sort.Slice(routes, func(i, j int) bool { return routes[i].Name < routes[j].Name })
	for _, route := range routes {
		inputs, err := expandSelectors([]string{route.Input}, artifacts)
		if err != nil {
			return ArtifactGraph{}, e.Wrap(e.CodeInvalidCatalog, err, "delivery route %q", route.Name)
		}
		if len(inputs) != 1 {
			return ArtifactGraph{}, e.New(e.CodeInvalidCatalog, "delivery route %q must select exactly one artifact, got %d", route.Name, len(inputs))
		}
		target := plan.Targets[route.Target]
		if err := validateRouteCompatibility(route, target, artifacts[inputs[0]]); err != nil {
			return ArtifactGraph{}, err
		}
		nodeID := "publish/" + route.Name
		outputID := "publication/" + route.Name
		nodes[nodeID] = Node{ID: nodeID, Kind: NodePublish, Target: route.Target,
			Inputs: inputs, Outputs: []string{outputID}, Network: ir.NetworkDefault, RunsOn: runsOn}
		artifacts[outputID] = Artifact{ID: outputID, Name: route.Name, Kind: "published-reference",
			Producer: nodeID, Target: route.Target,
			Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", "publications", route.Name+".txt"))}
		if err := addSigningDataflow(&plan, runsOn, route, inputs[0], outputID, nodes, artifacts); err != nil {
			return ArtifactGraph{}, err
		}
	}

	if err := bindReferenceReleaseGraph(&plan, nodes, artifacts); err != nil {
		return ArtifactGraph{}, err
	}
	plan.Graph.Nodes = mapNodes(nodes)
	plan.Graph.Artifacts = mapArtifacts(artifacts)
	if err := bindEvidenceAttestation(&plan); err != nil {
		return ArtifactGraph{}, err
	}
	return plan.Graph, nil
}

// Production reconciliation consumes the original held Packer capability.
// Generic VM command builds remain supported as local raw diagnostics only.
func admitDiskPublicationProducer(build BuildPlan) error {
	root, err := buildgraph.DiskRootName(build.ResolvedComponents)
	if err != nil {
		return err
	}
	if build.Graph == nil {
		return e.New(e.CodeEngineCapability, "disk publication has no resolved producer graph")
	}
	for _, node := range build.Graph.Nodes {
		if node.Component == root && node.Kind == ir.KindBuild && node.OutputType == PackageDiskImage {
			if node.BuilderType == ir.BuilderVMImage && node.Packer != nil {
				if node.Packer.AcquisitionArchive == "" {
					return e.New(e.CodeEngineCapability, "disk publication requires a declared producer-owned package acquisition carrier")
				}
				return nil
			}
			return e.New(e.CodeEngineCapability, "disk publication requires an original direct-Packer producer capability")
		}
	}
	return e.New(e.CodeEngineCapability, "disk publication has no supported direct-Packer producer")
}

func mapNodes(values map[string]Node) []Node {
	keys := sortedKeys(values)
	out := make([]Node, 0, len(keys))
	for _, key := range keys {
		node := values[key]
		node.Inputs = uniqueSorted(node.Inputs)
		node.Outputs = uniqueSorted(node.Outputs)
		out = append(out, node)
	}
	return out
}

func mapArtifacts(values map[string]Artifact) []Artifact {
	keys := sortedKeys(values)
	out := make([]Artifact, 0, len(keys))
	for _, key := range keys {
		out = append(out, values[key])
	}
	return out
}

func uniqueSorted(values []string) []string {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return sortedKeys(set)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

// validateCompiledGraph checks the complete compiler-owned operation set after
// authenticating the authored inputs, catalog snapshots and producer selections.
func (plan Plan) validateCompiledGraph(packages []packageSpec) error {
	for _, build := range plan.Builds {
		selection, ok := plan.selectionForBuild(build.Name)
		if !ok {
			return e.New(e.CodeUnverifiedRef, "build %q has no Definition selection", build.Name)
		}
		if selection.Catalog == nil && build.CatalogDigest != plan.CatalogDigest {
			return e.New(e.CodeUnverifiedRef, "output %q substitutes the Definition catalog", build.Name)
		}
	}
	tools, err := selectedTools(plan.Definition, plan.Environment, plan.CatalogSnapshot)
	if err != nil {
		return err
	}
	expected, err := compileArtifactGraph(plan, tools, packages)
	if err != nil {
		return err
	}
	if len(plan.Graph.Nodes) != len(expected.Nodes) || len(plan.Graph.Artifacts) != len(expected.Artifacts) {
		return e.New(e.CodePipelineDrift, "run plan operation or artifact set differs from its compiler-derived graph")
	}
	for i, want := range expected.Nodes {
		actual := plan.Graph.Nodes[i]
		// Omitted empty inputs can be nil after decoding. All nonempty edges and
		// their canonical ordering remain exact; graph validation rejects duplicates.
		if len(actual.Inputs) == 0 {
			actual.Inputs = nil
		}
		if len(want.Inputs) == 0 {
			want.Inputs = nil
		}
		if !reflect.DeepEqual(actual, want) {
			return e.New(e.CodePipelineDrift, "operation %q differs from compiler-derived operation %q", actual.ID, want.ID)
		}
	}
	for i, want := range expected.Artifacts {
		if plan.workspace != "" {
			path, err := relocatedFactoryPath(want.Path, plan.workspace)
			if err != nil {
				return err
			}
			want.Path = filepath.ToSlash(path)
		}
		if plan.Graph.Artifacts[i] != want {
			return e.New(e.CodePipelineDrift, "artifact %q differs from compiler-derived artifact %q", plan.Graph.Artifacts[i].ID, want.ID)
		}
	}
	return nil
}
