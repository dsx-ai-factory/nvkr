// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func TestInternalToolUseCompletionJoinsExactDescriptions(t *testing.T) {
	_, plan := internalToolPlanFixture(t)
	t.Chdir(t.TempDir())
	nodes, artifacts := planIndex(plan)
	node := nodes["node-image/sbom"]
	tool := plan.InternalTools[0]
	directory := artifacts["sbom/node-image"].Path
	identity, err := plan.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	write := func(path string, value any) {
		t.Helper()
		raw, err := canonical.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFactoryFile(t.Context(), path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// These are description fixtures for the completion join. Separate producer,
	// terminal and SBOM validators authenticate their contents in finalization.
	write(artifacts[tool.ObservationArtifactID].Path, struct{}{})
	write(filepath.Join(artifacts[tool.SBOMArtifactID].Path, "index.json"), struct{}{})
	observation, err := digestFactoryPath(t.Context(), artifacts[tool.ObservationArtifactID].Path)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := digestFactoryPath(t.Context(), artifacts[tool.SBOMArtifactID].Path)
	if err != nil {
		t.Fatal(err)
	}
	helperDigest := "sha256:" + strings.Repeat("a", 64)
	subjectDigest := "sha256:" + strings.Repeat("b", 64)
	write(artifacts[tool.ResultArtifactID].Path, pipeline.ArtifactResult{Component: tool.Component, SHA256: helperDigest, Size: 11})
	for _, output := range plan.LifecycleOutputs {
		if output.Name != node.BuildTarget {
			continue
		}
		root := artifacts[output.ArtifactID]
		write(artifacts["result/"+output.Name+"/"+root.Component].Path, pipeline.ArtifactResult{Component: root.Component, SHA256: subjectDigest, Size: 22})
		for _, component := range output.Components {
			bom := &sbom.BOM{}
			bom.Metadata.Props = append(bom.Metadata.Props, sbom.Property{Name: "nvkr:diskInspectionHelperDigest", Value: helperDigest})
			write(filepath.Join(directory, component+".cdx.json"), bom)
		}
	}
	_, appliance, _ := strings.Cut(plan.Environment.DiskInspectionProfiles[node.Platform].Image, "@")
	record := internalToolUseRecord{SchemaVersion: "nvkr.dev/internal-tool-use/v1", PlanDigest: identity,
		Consumer: node.ID, Purpose: "disk-inspection", Tool: tool.Name, SubjectDigest: subjectDigest, SubjectSize: 22,
		Appliance: appliance, Observation: observation, SBOM: evidence,
		Binding: pipeline.ProducedExecutableBinding{BuildDigest: tool.BuildDigest, Platform: tool.Platform, Component: tool.Component,
			Digest: helperDigest, Size: 11, TerminalDigest: "sha256:" + strings.Repeat("c", 64)}}
	local := func(path string) (string, error) { return path, nil }
	assertFactoryErrorCode(t, validateInternalToolUseEvidence(t.Context(), plan, node, directory, artifacts, local), e.CodeProvenanceMissing)
	for _, tc := range []struct {
		name   string
		change func(*internalToolUseRecord)
	}{
		{"exact", func(*internalToolUseRecord) {}},
		{"foreign Plan", func(r *internalToolUseRecord) { r.PlanDigest = helperDigest }},
		{"foreign consumer", func(r *internalToolUseRecord) { r.Consumer = "sibling/sbom" }},
		{"wrong architecture", func(r *internalToolUseRecord) { r.Binding.Platform = "linux/arm64" }},
		{"changed helper", func(r *internalToolUseRecord) { r.Binding.Digest = subjectDigest }},
		{"changed subject", func(r *internalToolUseRecord) { r.SubjectDigest = helperDigest }},
		{"changed appliance", func(r *internalToolUseRecord) { r.Appliance = helperDigest }},
		{"changed evidence", func(r *internalToolUseRecord) { r.SBOM = helperDigest }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := record
			tc.change(&changed)
			write(filepath.Join(directory, internalToolUseFile), changed)
			err := validateInternalToolUseEvidence(t.Context(), plan, node, directory, artifacts, local)
			if tc.name == "exact" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
	node.InternalTools = nil
	assertFactoryErrorCode(t, validateInternalToolUseEvidence(t.Context(), plan, node, directory, artifacts, local), e.CodeVerifyFailed)
}
