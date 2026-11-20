// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
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
		result := pipeline.ArtifactResult{Component: artifact.Component, SHA256: digest, Size: size}
		if artifact.Component == tool.Component {
			_, producer, err := plan.producerInstruction(planNode(plan, artifact.Producer))
			if err != nil {
				t.Fatal(err)
			}
			result.BuilderType, result.EngineClass, result.OutputType = producer.BuilderType, producer.BuilderType, producer.OutputType
			result.Path, result.TransportPath = producer.ArtifactPath, "/synthetic/runtime"
		}
		write(artifact.Path, result)
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

func writeRuntimeConsumerFixture(t *testing.T, plan *Plan, artifacts map[string]Artifact) {
	writeRuntimeConsumerDigests(t, plan, artifacts, pipeline.FileDigest([]byte("layout")), pipeline.FileDigest([]byte("layout")))
}

func writeRuntimeConsumerDigests(t *testing.T, plan *Plan, artifacts map[string]Artifact, layout, layer string) {
	t.Helper()
	identity, err := plan.IdentityDigest()
	if err != nil {
		t.Fatal(err)
	}
	for _, consumer := range plan.Graph.Nodes {
		if consumer.Kind != NodeBuild || len(consumer.InternalTools) == 0 {
			continue
		}
		_, instruction, err := plan.producerInstruction(consumer)
		if err != nil {
			t.Fatal(err)
		}
		if instruction.Runtime == nil {
			continue
		}
		var tool InternalToolPlan
		for _, candidate := range plan.InternalTools {
			if candidate.Name == instruction.Runtime.Tool {
				tool = candidate
			}
		}
		produced, err := readProducerResult(t.Context(), artifacts[tool.ResultArtifactID].Path)
		if err != nil {
			t.Fatal(err)
		}
		_, producer, err := plan.producerInstruction(planNode(plan, artifacts[tool.ResultArtifactID].Producer))
		if err != nil {
			t.Fatal(err)
		}
		terminal, err := canonical.Digest(canonical.KindTerminalVerification, map[string]any{
			"buildDigest": tool.BuildDigest, "layoutDigest": layout,
			"nodes": []any{map[string]any{"id": producer.ID, "kind": producer.Kind, "status": "ok", "duration": int64(0)}},
			"artifacts": []any{map[string]any{"nodeId": producer.ID, "result": produced, "liveRoot": produced.TransportPath,
				"livePath": filepath.Join(produced.TransportPath, "blobs", "sha256", strings.TrimPrefix(produced.SHA256, "sha256:"))}},
		})
		if err != nil {
			t.Fatal(err)
		}
		observation, err := digestFactoryPath(t.Context(), artifacts[tool.ObservationArtifactID].Path)
		if err != nil {
			t.Fatal(err)
		}
		inventory, err := digestFactoryPath(t.Context(), artifacts[tool.SBOMArtifactID].Path)
		if err != nil {
			t.Fatal(err)
		}
		binding := &pipeline.RuntimeUseBinding{PlanDigest: identity, Subject: identity + "/" + identity + "/" + consumer.ID, Purpose: "packer-build", Runtime: *instruction.Runtime,
			Production: pipeline.ProducedRuntimeBinding{BuildDigest: tool.BuildDigest, Platform: tool.Platform, Component: tool.Component, Digest: produced.SHA256, Size: produced.Size, LayoutDigest: layout, LayerDigest: layer, TerminalDigest: terminal}, Observation: observation, SBOM: inventory}
		for _, id := range consumer.Outputs {
			if artifacts[id].Kind != "producer-result" {
				continue
			}
			raw, err := canonical.Marshal(pipeline.ArtifactResult{RuntimeUse: binding})
			if err != nil {
				t.Fatal(err)
			}
			if err := writeFactoryFile(t.Context(), artifacts[id].Path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestRuntimeConsistencyRequiresEveryIndependentProduction(t *testing.T) {
	plan := runtimeConsumersPlanFixture(t, []string{"first", "second", "third", "fourth"})
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
			t.Chdir(t.TempDir())
			_, artifacts := planIndex(plan)
			for _, tool := range plan.InternalTools {
				writeRuntimeConsistencyFixture(t, plan, artifacts, tool, pipeline.FileDigest([]byte("manifest")), pipeline.FileDigest([]byte("layout")), 100)
			}
			writeRuntimeConsumerFixture(t, plan, artifacts)
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
	plan.Actions = githubTestPlan(t).Actions
	plan.Environment.Publication = &definition.Publication{Mode: "per-output-v1"}
	jobs := make(map[string]string)
	for _, node := range plan.Graph.Nodes {
		jobs[node.ID] = githubJobID(node.ID)
	}
	var rendered bytes.Buffer
	if err := writeGitHubFinalizerRestoreSteps(&rendered, plan, jobs); err != nil {
		t.Fatal(err)
	}
	steps := decodeGitHubSteps(t, rendered.Bytes())
	names := strings.Split(strings.Trim(steps[0].With["pattern"], "{}"), ",")
	for _, tool := range plan.InternalTools {
		if slices.Contains(names, githubArtifactName(tool.ArtifactID)) {
			t.Fatal("finalizer downloads runtime payload")
		}
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
	_, artifacts := planIndex(plan)
	consumers := 0
	for _, consumer := range plan.Graph.Nodes {
		if consumer.Kind != NodeBuild || len(consumer.InternalTools) == 0 {
			continue
		}
		for _, id := range consumer.Outputs {
			if artifacts[id].Kind == "producer-result" {
				consumers++
				if !finalizerArtifact(plan, artifacts[id]) || !slices.Contains(names, githubArtifactName(id)) {
					t.Fatalf("consumer result %s omitted", id)
				}
			}
		}
	}
	if consumers != 4 {
		t.Fatalf("transport fixture has %d consumers, want four", consumers)
	}
}

func TestRuntimeConsumerEvidenceRejectsSubstitution(t *testing.T) {
	plan := runtimeConsumersPlanFixture(t, []string{"first", "second"})
	for _, field := range []string{"observation", "sbom", "manifest", "layout", "layer", "terminal", "size", "consumer", "plan", "runtime"} {
		t.Run(field, func(t *testing.T) {
			t.Chdir(t.TempDir())
			_, artifacts := planIndex(plan)
			for _, tool := range plan.InternalTools {
				writeRuntimeConsistencyFixture(t, plan, artifacts, tool, pipeline.FileDigest([]byte("manifest")), pipeline.FileDigest([]byte("layout")), 100)
			}
			writeRuntimeConsumerFixture(t, plan, artifacts)
			var path string
			var result pipeline.ArtifactResult
			for _, node := range plan.Graph.Nodes {
				if node.Kind != NodeBuild || len(node.InternalTools) == 0 {
					continue
				}
				_, instruction, err := plan.producerInstruction(node)
				if err != nil {
					t.Fatal(err)
				}
				if instruction.Runtime == nil {
					continue
				}
				for _, id := range node.Outputs {
					if artifacts[id].Kind == "producer-result" {
						path = artifacts[id].Path
					}
				}
				result, err = readProducerResult(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				break
			}
			if result.RuntimeUse == nil {
				t.Fatal("fixture omits runtime consumer")
			}
			use, other := result.RuntimeUse, pipeline.FileDigest([]byte("unrelated evidence"))
			switch field {
			case "observation":
				use.Observation = other
			case "sbom":
				use.SBOM = other
			case "manifest":
				use.Production.Digest = other
			case "layout":
				use.Production.LayoutDigest = other
			case "layer":
				use.Production.LayerDigest = other
			case "terminal":
				use.Production.TerminalDigest = other
			case "size":
				use.Production.Size++
			case "consumer":
				parts := strings.SplitN(use.Subject, "/", 3)
				parts[2] = "unrelated/" + filepath.Base(parts[2])
				use.Subject = strings.Join(parts, "/")
			case "plan":
				use.PlanDigest = other
			case "runtime":
				use.Runtime.Tool = "unrelated"
			}
			raw, err := canonical.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeFactoryFile(t.Context(), path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			err = validateRuntimeConsistency(t.Context(), plan, artifacts, func(path string) (string, error) { return path, nil })
			assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestRuntimeSBOMRequiresOwnedScratchBeforeIO(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scratch *runtimeScratch
		code    e.Code
	}{
		{"missing", nil, e.CodeEngineCapability}, {"closed", &runtimeScratch{}, e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := executeSBOMWithRuntime(t.Context(), &Plan{}, Node{}, nil, nil, &pipeline.ProducedRuntime{}, tc.scratch)
			assertFactoryErrorCode(t, err, tc.code)
		})
	}
}
