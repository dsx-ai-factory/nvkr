// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"reflect"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func lifecycleArtifactID(output authored.DefinitionOutput, components []catalog.NamedComponent) (string, error) {
	if output.Format == authored.OutputDebianPackage {
		if len(components) != 1 || components[0].OutputType != authored.OutputDebianPackage {
			return "", e.New(e.CodeInvalidCatalog, "Debian output requires one archive producer")
		}
		return "artifact/" + output.Name + "/" + components[0].Name, nil
	}
	if output.Format != authored.OutputDiskImage {
		return "package/" + output.Name, nil
	}
	root, err := buildgraph.DiskRootName(components)
	if err != nil {
		return "", e.Wrap(e.CodeInvalidCatalog, err,
			"disk-image output %q cannot derive its root producer", output.Name)
	}
	return "artifact/" + output.Name + "/" + root, nil
}

func (plan Plan) validateLifecyclePlans() error {
	if len(plan.LifecycleOutputs) == 0 {
		return nil
	}
	if plan.DefinitionDigest == "" || plan.EnvironmentDigest == "" || len(plan.LifecycleOutputs) == 0 {
		return e.New(e.CodeUnverifiedRef, "lifecycle plan bindings require exact Definition and Environment identities")
	}
	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	builds := make(map[string]BuildPlan, len(plan.Builds))
	for _, build := range plan.Builds {
		builds[build.Name] = build
	}
	nodes := make(map[string]Node, len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		nodes[node.ID] = node
	}
	previous := ""
	for i, output := range plan.LifecycleOutputs {
		required, err := outputRequirements(plan.Definition.Outputs[i], builds)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(output, required) {
			return e.New(e.CodeUnverifiedRef, "lifecycle output %q differs from its exact Definition requirements", output.Name)
		}
		artifact, ok := artifacts[output.ArtifactID]
		if output.ImageIndex != nil {
			if !ok || artifact.Kind != PackageImage || artifact.Name != output.Name || artifact.MediaType != "application/vnd.oci.image.index.v1+json" || output.Name <= previous {
				return e.New(e.CodeUnverifiedRef, "image index output %q lacks exact aggregate artifact", output.Name)
			}
			previous = output.Name
			continue // Requirements above were rederived; graph equality checks every child edge.
		}
		build, buildOK := builds[output.Name]
		producer := nodes[artifact.Producer]
		var actualComponents []string
		actualBuild := ""
		if output.Kind == authored.OutputDiskImage || output.Kind == authored.OutputDebianPackage {
			// Plan.Validate has already re-resolved and recompiled this BuildPlan
			// from the held Definition and catalog snapshot. Use that authenticated
			// selected closure rather than reconstructing only the root's direct
			// inputs from the artifact graph; material graphs are transitive.
			actualComponents = make([]string, 0, len(build.ResolvedComponents))
			for _, component := range build.ResolvedComponents {
				actualComponents = append(actualComponents, component.Name)
			}
			actualBuild = artifact.BuildTarget
		} else {
			for _, inputID := range producer.Inputs {
				input := artifacts[inputID]
				if input.Kind == "producer-result" || input.BuildTarget == "" {
					continue
				}
				if input.Component != "" {
					actualComponents = append(actualComponents, input.Component)
				}
				if actualBuild == "" {
					actualBuild = input.BuildTarget
				} else if actualBuild != input.BuildTarget {
					actualBuild = "!mixed"
				}
			}
		}
		actualComponents = uniqueSorted(actualComponents)
		nameMatches := artifact.Name == output.Name ||
			((output.Kind == authored.OutputDiskImage || output.Kind == authored.OutputDebianPackage) && artifact.Name == artifact.Component)
		if !canonicalName.MatchString(output.Name) || output.Name <= previous || !ok || artifact.Kind != lifecyclePackageKind(output.Kind) ||
			!nameMatches || !buildOK || (output.Kind != "helm-oci" && len(output.Components) == 0) || !sort.StringsAreSorted(output.Components) ||
			build.BuildDigest != output.BuildDigest || build.Name != actualBuild || !equalFactoryStrings(actualComponents, output.Components) {
			return e.New(e.CodeUnverifiedRef, "lifecycle output %q is malformed or not bound to its graph build", output.Name)
		}
		selection := plan.Definition.Outputs[i]
		if (selection.Format != authored.OutputDebianPackage && build.Platform != selection.Platform) || !equalFactoryStrings(build.Components, selection.Components) {
			return e.New(e.CodeUnverifiedRef, "output %q producer selection differs from its definition", output.Name)
		}
		previous = output.Name
	}
	return nil
}

func lifecyclePackageKind(kind string) string {
	switch kind {
	case "executable":
		return PackageBinaryBundle
	case "apt-repository":
		return PackageAPT
	case "helm-oci":
		return PackageHelm
	case "oci":
		return PackageImage
	case authored.OutputDiskImage, authored.OutputDebianPackage:
		return kind
	default:
		return ""
	}
}
