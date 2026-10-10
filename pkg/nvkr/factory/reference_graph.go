// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

const NodeAssembleRelease = "assemble-release"
const ArtifactPublicationMetadata = "publication-metadata"

func PerOutputPublication(plan *Plan) bool {
	return plan != nil && plan.Environment.Publication != nil && plan.Environment.Publication.Mode == "per-output-v1"
}

func outputPublicationRoute(name string) string   { return "output-" + name }
func outputPublicationPackage(name string) string { return "publication-" + name }

func deriveOutputPublication(def definition.Definition, env definition.Environment, packages []packageSpec, targets map[string]Target, routes []Route) ([]packageSpec, map[string]Target, []Route, error) {
	if env.Publication.Mode != "per-output-v1" {
		return packages, targets, routes, nil
	}
	for _, output := range def.Outputs {
		if strings.HasPrefix(output.Name, "publication-") {
			return nil, nil, nil, e.New(e.CodeInvalidCatalog, "output name collides with generated publication namespace")
		}
		route := outputPublicationRoute(output.Name)
		name := outputPublicationPackage(output.Name)
		packages = append(packages, packageSpec{Name: name, Kind: PackageOCI, Inputs: []string{"package/" + output.Name}})
		target := targets["release"]
		target.ImageTags = nil
		if tag := env.Publication.ImageTags[output.Name]; tag != "" {
			target.ImageTags = map[string]string{output.Name: tag}
		}
		target.Ref = env.Publication.RepositoryPrefix + "/releases-" + output.Name
		targets[route] = target
		routes = append(routes, Route{Name: route, Input: "package/" + name, Target: route})
	}
	target := targets["release"]
	target.ImageTags = nil // The metadata-only root cannot publish payload aliases.
	targets["release"] = target
	return packages, targets, routes, nil
}

// Replaces the aggregate payload edges with authenticated output-local metadata.
// The complete Plan, including every producer and verification operation, remains.
func bindReferenceReleaseGraph(plan *Plan, nodes map[string]Node, artifacts map[string]Artifact) error {
	if !PerOutputPublication(plan) {
		return nil
	}
	inputs := []string{"tool/" + plan.Definition.CosignTool}
	for _, route := range plan.Routes {
		verify := nodes[NodeVerifyPublication+"/"+route.Name]
		if verify.ID == "" {
			return e.New(e.CodePipelineDrift, "per-output publication requires verification")
		}
		id := "metadata/" + route.Name
		verify.Outputs = uniqueSorted(append(verify.Outputs, id))
		nodes[verify.ID] = verify
		artifacts[id] = Artifact{ID: id, Name: route.Name, Kind: ArtifactPublicationMetadata, Producer: verify.ID, Target: route.Target,
			Path: filepath.ToSlash(filepath.Join(".nvkr", "factory", "metadata", route.Name))}
		if route.Name != "release" {
			inputs = append(inputs, id, "publication/"+route.Name, "signature/"+route.Name, "predicate/"+route.Name, "verification/"+route.Name)
		}
	}
	n := nodes["package/release"]
	n.Kind = NodeAssembleRelease
	n.Network = ir.NetworkDefault
	n.Target = "release"
	n.Container = ""
	n.Platform = ""
	n.Inputs = uniqueSorted(inputs)
	nodes[n.ID] = n
	return nil
}

func selectedPublicationOutputs(plan *Plan, node Node) []LifecycleOutputPlan {
	if PerOutputPublication(plan) {
		for _, output := range plan.LifecycleOutputs {
			if node.ID == "package/"+outputPublicationPackage(output.Name) {
				return []LifecycleOutputPlan{output}
			}
		}
	}
	return plan.LifecycleOutputs
}

// Only authenticated release metadata and its cryptographic tools/records cross
// the final aggregate boundary. Provider job outcomes still cover the whole DAG.
func finalizerArtifact(plan *Plan, a Artifact) bool {
	if !PerOutputPublication(plan) {
		_, raw := diskPublicationRequirement(plan, a)
		return !raw
	}
	switch a.ID {
	case "metadata/release", "publication/release", "signature/release", "predicate/release", "verification/release", "tool/" + plan.Definition.CosignTool:
		return true
	}
	return false
}
