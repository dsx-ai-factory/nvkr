// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/buildgraph"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// Synthetic transported records test completion checks, never native production
// or execution authority. The real source-pinned assembly is separately evidenced.
func writeRuntimeConsistencyFixture(t *testing.T, plan *Plan, artifacts map[string]Artifact, tool InternalToolPlan, subject, layout string, size int64) {
	t.Helper()
	write := func(path string, value any) {
		t.Helper()
		raw, err := canonical.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := writeFactoryFile(t.Context(), path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var build BuildPlan
	for _, candidate := range plan.Builds {
		if candidate.Name == tool.BuildTarget {
			build = candidate
		}
	}
	hashes := map[string]string{}
	for _, artifact := range artifacts {
		if artifact.BuildTarget != tool.BuildTarget || artifact.Kind != "producer-result" {
			continue
		}
		digest := pipeline.FileDigest([]byte(artifact.Component))
		if artifact.Component == tool.Component {
			digest = subject
		}
		hashes[artifact.Component] = digest
		write(artifact.Path, pipeline.ArtifactResult{Component: artifact.Component, SHA256: digest, Size: size})
	}
	boms, index, err := sbom.PerArtifact(&buildgraph.Resolved{Platform: build.Platform, Components: build.ResolvedComponents}, build.BuildDigest, hashes)
	if err != nil {
		t.Fatal(err)
	}
	bom := boms[tool.Component]
	digest := pipeline.FileDigest([]byte("fixture report"))
	bom.Metadata.Props = append(bom.Metadata.Props,
		sbom.Property{Name: "nvkr:dependencyInspection", Value: sbom.MkosiInstalledMethod},
		sbom.Property{Name: "nvkr:runtimeLayerDigest", Value: layout},
		sbom.Property{Name: "nvkr:runtimeRecipeDigest", Value: digest},
		sbom.Property{Name: "nvkr:mkosi:runtime:reportDigest", Value: digest})
	slices.SortFunc(bom.Metadata.Props, func(a, b sbom.Property) int { return strings.Compare(a.Name, b.Name) })
	const purl = "pkg:deb/ubuntu/base-files@1?arch=amd64&distro=noble"
	bom.Components = []sbom.Component{{Type: "library", Name: "base-files", Version: "1", PURL: purl, BOMRef: purl,
		Properties: []sbom.Property{
			{Name: "nvkr:mkosi:runtime:archiveDigest", Value: digest},
			{Name: "nvkr:mkosi:runtime:archiveSize", Value: "42"},
			{Name: "nvkr:mkosi:runtime:reportDigest", Value: digest},
			{Name: "nvkr:mkosi:runtime:repositoryDigest", Value: digest},
			{Name: "nvkr:mkosi:runtime:snapshot", Value: "20261109T000000Z"},
		}}}
	bom.Metadata.Component.BOMRef = "artifact:" + strings.TrimPrefix(subject, "sha256:")
	bom.Dependencies = []sbom.Dependency{{Ref: bom.Metadata.Component.BOMRef, DependsOn: []string{purl}}}
	for i := range index.Entries {
		entry := &index.Entries[i]
		entry.SBOMDigest, err = boms[entry.Artifact].Digest()
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join(artifacts[tool.SBOMArtifactID].Path, entry.Artifact+".cdx.json"), boms[entry.Artifact])
	}
	write(filepath.Join(artifacts[tool.SBOMArtifactID].Path, "index.json"), index)
	node := planNode(plan, artifacts[tool.ObservationArtifactID].Producer)
	observation := factoryObservation{SchemaVersion: "nvkr.dev/factory-observation/v2", BuildDigest: tool.BuildDigest,
		BuildTarget: tool.BuildTarget, Qualification: TierStaticUnqualified}
	for _, id := range node.Inputs {
		artifact := artifacts[id]
		digest := pipeline.FileDigest([]byte("fixture input " + artifact.Component))
		if id == tool.ArtifactID {
			digest = layout
		}
		if artifact.Kind == "producer-result" {
			digest, err = digestFactoryPath(t.Context(), artifact.Path)
			if err != nil {
				t.Fatal(err)
			}
		}
		observation.Inputs = append(observation.Inputs, factoryObservedInput{ArtifactID: id, Kind: artifact.Kind, Digest: digest})
	}
	write(artifacts[tool.ObservationArtifactID].Path, observation)
}

func TestRuntimeConsistencyRequiresEveryIndependentProduction(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, *Plan, map[string]Artifact, InternalToolPlan)
		code   e.Code
	}{
		{name: "four equal productions"},
		{name: "different manifest", change: func(t *testing.T, p *Plan, a map[string]Artifact, tool InternalToolPlan) {
			writeRuntimeConsistencyFixture(t, p, a, tool, pipeline.FileDigest([]byte("different")), pipeline.FileDigest([]byte("layout")), 100)
		}, code: e.CodeVerifyFailed},
		{name: "different layout", change: func(t *testing.T, p *Plan, a map[string]Artifact, tool InternalToolPlan) {
			writeRuntimeConsistencyFixture(t, p, a, tool, pipeline.FileDigest([]byte("manifest")), pipeline.FileDigest([]byte("different")), 100)
		}, code: e.CodeVerifyFailed},
		{name: "different size", change: func(t *testing.T, p *Plan, a map[string]Artifact, tool InternalToolPlan) {
			writeRuntimeConsistencyFixture(t, p, a, tool, pipeline.FileDigest([]byte("manifest")), pipeline.FileDigest([]byte("layout")), 101)
		}, code: e.CodeVerifyFailed},
		{name: "missing independent inventory", change: func(t *testing.T, _ *Plan, a map[string]Artifact, tool InternalToolPlan) {
			artifact := a[tool.SBOMArtifactID]
			artifact.Path += "-absent"
			a[tool.SBOMArtifactID] = artifact
		}, code: e.CodeProvenanceMissing},
		{name: "changed result after observation", change: func(t *testing.T, _ *Plan, a map[string]Artifact, tool InternalToolPlan) {
			result, err := readProducerResult(t.Context(), a[tool.ResultArtifactID].Path)
			if err != nil {
				t.Fatal(err)
			}
			result.Size++
			raw, err := canonical.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeFactoryFile(t.Context(), a[tool.ResultArtifactID].Path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := runtimeConsumersPlanFixture(t, []string{"first", "second", "third", "fourth"})
			t.Chdir(t.TempDir())
			_, artifacts := planIndex(plan)
			for _, tool := range plan.InternalTools {
				writeRuntimeConsistencyFixture(t, plan, artifacts, tool, pipeline.FileDigest([]byte("manifest")), pipeline.FileDigest([]byte("layout")), 100)
			}
			if tc.change != nil {
				tc.change(t, plan, artifacts, plan.InternalTools[3])
			}
			err := validateRuntimeConsistency(t.Context(), plan, artifacts, func(path string) (string, error) { return path, nil })
			if tc.code != "" {
				assertFactoryErrorCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRuntimeConsistencyTransportsEvidenceWithoutPayloadOrAuthority(t *testing.T) {
	plan := runtimeConsumersPlanFixture(t, []string{"first", "second", "third", "fourth"})
	plan.Environment.Publication = &definition.Publication{Mode: "per-output-v1"}
	for _, tool := range plan.InternalTools {
		for _, a := range plan.Graph.Artifacts {
			if a.BuildTarget != tool.BuildTarget {
				continue
			}
			want := a.ID == tool.ObservationArtifactID || a.ID == tool.SBOMArtifactID || a.Kind == "producer-result"
			if got := finalizerArtifact(plan, a); got != want {
				t.Fatalf("transport %s = %v, want %v", a.ID, got, want)
			}
		}
	}
}
