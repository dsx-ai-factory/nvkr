// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"reflect"
	"slices"

	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// ImageIndexPlan freezes requirements before execution. Observed native manifests
// belong to delivery.ImageIndexBinding and cannot fill an absent child here.
type ImageIndexPlan struct {
	RequiredPlatforms []string              `json:"requiredPlatforms"`
	Children          []ImageIndexChildPlan `json:"children"`
}

type ImageIndexChildPlan struct {
	Platform         string `json:"platform"`
	BuildTarget      string `json:"buildTarget"`
	BuildDigest      string `json:"buildDigest"`
	Component        string `json:"component"`
	ArtifactID       string `json:"artifactId"`
	ResultArtifactID string `json:"resultArtifactId"`
	SBOMArtifactID   string `json:"sbomArtifactId"`
}

func (index *ImageIndexPlan) Clone() *ImageIndexPlan {
	if index == nil {
		return nil
	}
	return &ImageIndexPlan{RequiredPlatforms: slices.Clone(index.RequiredPlatforms), Children: slices.Clone(index.Children)}
}

func (index *ImageIndexPlan) buildDigest() (string, error) {
	if index == nil {
		return "", e.New(e.CodeUnverifiedRef, "image index requirements are absent")
	}
	children := make([]ir.ImageIndexBuildChild, len(index.Children))
	for i, child := range index.Children {
		if !safeGraphID(child.BuildTarget) || child.ArtifactID != "artifact/"+child.BuildTarget+"/"+child.Component || child.ResultArtifactID != "result/"+child.BuildTarget+"/"+child.Component || child.SBOMArtifactID != "sbom/"+child.BuildTarget {
			return "", e.New(e.CodeUnverifiedRef, "image index child has inexact producer or evidence references")
		}
		children[i] = ir.ImageIndexBuildChild{Platform: child.Platform, BuildDigest: child.BuildDigest, Component: child.Component}
	}
	return ir.ImageIndexBuildDigest(index.RequiredPlatforms, children)
}

// nativeOutputSelections expands only compiler-owned producer uses. A slash
// cannot occur in a public output name, so each (output,platform) is injective and
// cannot collide with a separately named public output. Native names stay stable.
func nativeOutputSelections(output authored.DefinitionOutput) []authored.DefinitionOutput {
	if len(output.Platforms) == 0 {
		return []authored.DefinitionOutput{output}
	}
	selections := make([]authored.DefinitionOutput, 0, len(output.Platforms))
	for _, platform := range output.Platforms {
		native := output
		native.Name = output.Name + "/" + platform
		native.Platform = platform
		native.Platforms = nil
		selections = append(selections, native)
	}
	return selections
}

func (plan Plan) selectionForBuild(name string) (authored.DefinitionOutput, bool) {
	for _, output := range plan.Definition.Outputs {
		for _, selection := range nativeOutputSelections(output) {
			if selection.Name == name {
				return selection, true
			}
		}
	}
	return authored.DefinitionOutput{}, false
}

func outputRequirements(output authored.DefinitionOutput, builds map[string]BuildPlan) (LifecycleOutputPlan, error) {
	required := LifecycleOutputPlan{Name: output.Name, Kind: output.Format, Components: slices.Clone(output.Components)}
	selections := nativeOutputSelections(output)
	if len(output.Platforms) > 0 {
		required.ImageIndex = &ImageIndexPlan{RequiredPlatforms: slices.Clone(output.Platforms)}
	}
	for _, selection := range selections {
		build, ok := builds[selection.Name]
		if !ok || build.Platform != selection.Platform || !reflect.DeepEqual(build.Components, selection.Components) {
			// Disk graphs include transitive material components; their selected closure is
			// checked by the native lifecycle validator and catalog recompilation.
			if !ok || output.Format != authored.OutputDiskImage || build.Platform != selection.Platform {
				return LifecycleOutputPlan{}, e.New(e.CodeUnverifiedRef, "output %q lacks its exact native producer selection %q", output.Name, selection.Name)
			}
		}
		artifactID, err := lifecycleArtifactID(output, build.ResolvedComponents)
		if err != nil {
			return LifecycleOutputPlan{}, err
		}
		required.ArtifactID = artifactID
		if required.ImageIndex == nil {
			required.BuildDigest = build.BuildDigest
			continue
		}
		component := selection.Components[0]
		required.ImageIndex.Children = append(required.ImageIndex.Children, ImageIndexChildPlan{
			Platform: selection.Platform, BuildTarget: selection.Name, BuildDigest: build.BuildDigest, Component: component,
			ArtifactID: "artifact/" + selection.Name + "/" + component, ResultArtifactID: "result/" + selection.Name + "/" + component, SBOMArtifactID: "sbom/" + selection.Name,
		})
	}
	if required.ImageIndex != nil {
		digest, err := required.ImageIndex.buildDigest()
		if err != nil {
			return LifecycleOutputPlan{}, err
		}
		required.BuildDigest = digest
	}
	return required, nil
}

// ValidateFor binds a frozen vector to its public output. Empty optional digest
// and components allow compiler-owned package operations to validate their shape.
func (index *ImageIndexPlan) ValidateFor(outputName, buildDigest string, components []string) error {
	digest, err := index.buildDigest()
	if err != nil {
		return err
	}
	if !canonicalName.MatchString(outputName) || (buildDigest != "" && digest != buildDigest) {
		return e.New(e.CodeUnverifiedRef, "image index requirements differ from output identity")
	}
	for _, child := range index.Children {
		if child.BuildTarget != outputName+"/"+child.Platform || (components != nil && (len(components) != 1 || components[0] != child.Component)) {
			return e.New(e.CodeUnverifiedRef, "image index child belongs to another output or component")
		}
	}
	return nil
}
