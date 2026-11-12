// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"reflect"
	"slices"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// InternalToolPlan is a compiler-derived required use, never a delivery slot or
// execution capability. Consumer receipts authenticate the referenced results.
type InternalToolPlan struct {
	Name                  string `json:"name"`
	Platform              string `json:"platform"`
	Format                string `json:"format"`
	BuildTarget           string `json:"buildTarget"`
	BuildDigest           string `json:"buildDigest"`
	Component             string `json:"component"`
	ArtifactID            string `json:"artifactId"`
	ResultArtifactID      string `json:"resultArtifactId"`
	ObservationArtifactID string `json:"observationArtifactId"`
	SBOMArtifactID        string `json:"sbomArtifactId"`
}

func internalToolSelection(tool authored.InternalTool) authored.DefinitionOutput {
	// Authored names contain no slash. Aggregate delivery build names have the
	// distinct shape output/linux/arch, so neither can alias internal/name.
	return authored.DefinitionOutput{Name: "internal/" + tool.Name, Platform: tool.Platform,
		Catalog: tool.Catalog, Components: slices.Clone(tool.Components), Format: tool.Format}
}

// Evidence tools are Plan inputs, independent of the produced artifact's Build
// identity. Internal tools run on their native consumer host, which may differ
// from the Environment's ordinary utility platform.
func (plan Plan) sbomToolForBuild(target string) string {
	for _, tool := range plan.Definition.InternalTools {
		if internalToolSelection(tool).Name == target && tool.SBOMTool != "" {
			return tool.SBOMTool
		}
	}
	return plan.Definition.SBOMTool
}

func requiredInternalTools(definition authored.Definition, builds []BuildPlan) ([]InternalToolPlan, error) {
	var requirements []InternalToolPlan
	for _, tool := range definition.InternalTools {
		selection := internalToolSelection(tool)
		var selected *BuildPlan
		for i := range builds {
			if builds[i].Name == selection.Name {
				if selected != nil {
					return nil, e.New(e.CodePipelineDrift, "internal tool %q has repeated builds", tool.Name)
				}
				selected = &builds[i]
			}
		}
		if selected == nil || selected.Platform != tool.Platform || !slices.Equal(selected.Components, tool.Components) {
			return nil, e.New(e.CodePipelineDrift, "internal tool %q lacks its exact selected producer closure", tool.Name)
		}
		if err := pipeline.ValidateProducedExecutableProducer(selected.Graph, selected.BuildDigest, tool.Result); err != nil {
			return nil, err
		}
		requirements = append(requirements, InternalToolPlan{Name: tool.Name, Platform: tool.Platform, Format: tool.Format,
			BuildTarget: selection.Name, BuildDigest: selected.BuildDigest, Component: tool.Result,
			ArtifactID:            "artifact/" + selection.Name + "/" + tool.Result,
			ResultArtifactID:      "result/" + selection.Name + "/" + tool.Result,
			ObservationArtifactID: "observation/" + selection.Name, SBOMArtifactID: "sbom/" + selection.Name})
	}
	return requirements, nil
}

func (plan Plan) validateInternalToolPlans() error {
	expected, err := requiredInternalTools(plan.Definition, plan.Builds)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(plan.InternalTools, expected) {
		return e.New(e.CodePipelineDrift, "internal tool requirements differ from their exact Definition producer uses")
	}
	return nil
}

func (tool InternalToolPlan) inputs() []string {
	return []string{tool.ArtifactID, tool.ResultArtifactID, tool.ObservationArtifactID, tool.SBOMArtifactID}
}

// internalToolInput separates a declared tool dependency from the consumer's
// own SBOM subjects. Foreign build results are not implicitly tool inputs.
func (plan *Plan) internalToolInput(node Node, artifact Artifact) bool {
	if artifact.BuildTarget == node.BuildTarget || !slices.Contains(node.Inputs, artifact.ID) {
		return false
	}
	for _, tool := range plan.InternalTools {
		if slices.Contains(node.InternalTools, tool.Name) && artifact.BuildTarget == tool.BuildTarget && slices.Contains(tool.inputs(), artifact.ID) {
			return true
		}
	}
	return false
}

func bindInternalToolUses(plan Plan, nodes map[string]Node, artifacts map[string]Artifact) error {
	tools := make(map[string]InternalToolPlan, len(plan.InternalTools))
	for _, tool := range plan.InternalTools {
		tools[tool.Name] = tool
	}
	used := map[string]bool{}
	for _, id := range sortedKeys(nodes) {
		node := nodes[id]
		if node.Kind != NodeSBOM {
			continue
		}
		inspection := plan.hasCompiledDiskTarget(node.BuildTarget, artifacts) && !diskBuildProvenance(&plan) ||
			plan.debianPublicationTarget(node.BuildTarget) && plan.Definition.SBOMTool != ""
		if !inspection {
			continue
		}
		profile := plan.Environment.DiskInspectionProfiles[node.Platform]
		if profile.HelperTool == "" {
			continue
		}
		tool, ok := tools[profile.HelperTool]
		if !ok || tool.Format != authored.OutputExecutable || tool.Platform != node.Platform {
			return e.New(e.CodeEngineCapability, "inspection node %q lacks its exact native executable helper selection", id)
		}
		for _, input := range tool.inputs() {
			if _, ok := artifacts[input]; !ok {
				return e.New(e.CodePipelineDrift, "internal helper %q lacks producer or evidence input %q", tool.Name, input)
			}
		}
		node.Inputs = uniqueSorted(append(node.Inputs, tool.inputs()...))
		node.InternalTools = []string{tool.Name}
		nodes[id] = node
		used[tool.Name] = true
	}
	for _, tool := range plan.InternalTools {
		if !used[tool.Name] {
			return e.New(e.CodeInvalidCatalog, "internal tool %q has no selected consumer", tool.Name)
		}
	}
	return nil
}

// Current execution support keeps production, evidence and inspection in one
// native hosted owner. Other consumers and cross-job capability restoration
// remain closed; ordinary fetch and scanner acquisition may precede that group.
func admitInternalToolExecution(plan *Plan) error {
	for _, tool := range plan.InternalTools {
		if tool.Format != authored.OutputExecutable || !githubDiskInspectionRequired(plan) {
			return e.New(e.CodeEngineCapability, "internal tool execution requires an original native inspection owner")
		}
		if _, err := internalToolDiskOutput(plan, tool); err != nil {
			return err
		}
	}
	return nil
}
