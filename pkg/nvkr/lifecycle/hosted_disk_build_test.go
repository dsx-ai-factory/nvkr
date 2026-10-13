// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestFinalizeGeneratedDiskBuildOnly(t *testing.T) {
	for _, test := range []struct {
		name string
		want e.Code
	}{
		{"complete", ""},
		{"changed disk", e.CodeVerifyFailed},
		{"changed producer", e.CodeVerifyFailed},
		{"failed provider", e.CodeProvenanceMissing},
		{"missing terminal observation", e.CodeProvenanceMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan := hostedDiskBuildFinalizerFixture(t)
			artifacts := map[string]factory.Artifact{}
			for _, artifact := range plan.Graph.Artifacts {
				artifacts[artifact.ID] = artifact
			}
			expected := plan.LifecycleOutputs[0]
			payloadArtifact := artifacts[expected.ArtifactID]
			payload := make([]byte, 512)
			copy(payload, []byte{'Q', 'F', 'I', 0xfb})
			binary.BigEndian.PutUint32(payload[4:8], 3)
			binary.BigEndian.PutUint32(payload[20:24], 9)
			binary.BigEndian.PutUint64(payload[24:32], 1<<20)
			binary.BigEndian.PutUint32(payload[100:104], 104)
			if err := os.MkdirAll(filepath.Dir(payloadArtifact.Path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(payloadArtifact.Path, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			record := artifacts["result/"+expected.Name+"/"+payloadArtifact.Component]
			result := fixtureProducerResult(t, plan, record, payloadArtifact.Path)
			result.InputDigest = "sha256:" + strings.Repeat("d", 64)
			result.EngineClass = ir.BuilderVMImage
			writeCanonicalTestFile(t, record.Path, result)
			var observationPath string
			for _, node := range plan.Graph.Nodes {
				if node.Kind != factory.NodeVerify {
					continue
				}
				var inputs []map[string]string
				for _, id := range node.Inputs {
					artifact := artifacts[id]
					raw, err := os.ReadFile(artifact.Path)
					if err != nil {
						t.Fatal(err)
					}
					inputs = append(inputs, map[string]string{"artifactId": id, "kind": artifact.Kind, "digest": pipeline.FileDigest(raw)})
				}
				observationPath = artifacts[node.Outputs[0]].Path
				writeCanonicalTestFile(t, observationPath, map[string]any{
					"schemaVersion": "nvkr.dev/factory-observation/v2", "qualification": factory.TierStaticUnqualified,
					"buildTarget": expected.Name, "buildDigest": expected.BuildDigest, "inputs": inputs,
				})
			}
			writeHostedRestoreOutcomes(t, ".nvkr/factory", plan)
			switch test.name {
			case "changed disk":
				payload[511] = 1
				if err := os.WriteFile(payloadArtifact.Path, payload, 0o600); err != nil {
					t.Fatal(err)
				}
			case "changed producer":
				result.Toolchain = "example.test/other@sha256:" + strings.Repeat("a", 64)
				writeCanonicalTestFile(t, record.Path, result)
			case "failed provider":
				if err := os.WriteFile(factory.HostedRestoreOutcomePath(".nvkr/factory", expected.ArtifactID), []byte("job-failed\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing terminal observation":
				if err := os.Remove(observationPath); err != nil {
					t.Fatal(err)
				}
			}
			run, err := FinalizeGenerated(t.Context(), "definition.json", "environment.json", "plan.json", "test", "fde-attempt", ".nvkr/factory", "run.json")
			if test.want != "" {
				assertCode(t, err, test.want)
				if run == nil || run.Status != RunFailed || run.Lane != LaneUnqualified {
					t.Fatalf("invalid failed Run: %+v", run)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != RunComplete || run.Lane != LaneUnqualified || len(run.Observed) != 1 || run.Observed[0].Disk == nil ||
				run.Observed[0].Disk.Encryption != "luks2" || run.Observed[0].Disk.EvidenceMode != "" || run.Observed[0].Digest != pipeline.FileDigest(payload) {
				t.Fatalf("disk build Run overclaims or loses exact bytes: %+v", run)
			}
			if !canonical.IsDigest(run.ContentSubject) {
				t.Fatal("complete disk Run has no frozen content subject")
			}
			_, err = FinalizeGenerated(t.Context(), "definition.json", "environment.json", "plan.json", "test", "fde-attempt", ".nvkr/factory", "run.json")
			assertCode(t, err, e.CodeImmutableViolation)
		})
	}
}

func hostedDiskBuildFinalizerFixture(t *testing.T) *factory.Plan {
	t.Helper()
	prepared, _ := diskLifecycleFixtureWithProducer(t, "fde-attempt", true)
	definition, environment := prepared.plan.Definition.Clone(), prepared.plan.Environment.Clone()
	environment.Publication, environment.Signing = nil, nil
	environment.GitHubARC, environment.Runtime.Runtime = "native-kvm-v1", "docker"
	environment.ExecutionClasses[0].RunsOn = "arc-amd64-kvm"
	component := prepared.plan.CatalogSnapshot.Components["nvke-node-non-fde"]
	component.Disk.Encryption = "luks2"
	component.Packer.AcquisitionArchive = "acquisitions.tar"
	component.Packer.Variables = map[string]string{"luks_passphrase": "changeit"}
	writeCanonicalTestFile(t, "catalog/registry.yaml", map[string]any{
		"apiVersion": "nvkr.dev/catalog/v3", "kind": "SourceBuildRegistry", "metadata": map[string]string{"name": "fde-test"},
		"sources": map[string]any{"image": map[string]any{"repo": component.Repo, "constraint": "1.0.x", "toolchainImagesByArch": component.ToolchainImages}},
		"components": map[string]any{"nvke-node-non-fde": map[string]any{
			"source": "image", "platforms": component.Platforms, "fromSource": true, "builderType": component.BuilderType,
			"outputType": component.OutputType, "disk": component.Disk, "buildRequirements": component.BuildRequirements,
			"packer": component.Packer, "artifactPath": component.ArtifactPath,
		}},
	})
	writeCanonicalTestFile(t, "catalog/pins.lock.yaml", map[string]any{
		"apiVersion": "nvkr.dev/lock/v1", "kind": "PinLock", "pins": map[string]any{"image": map[string]any{
			"tag": component.Tag, "commit": component.Commit, "treeHash": component.TreeHash, "sourceDateEpoch": component.SourceDate,
		}},
	})
	writeCanonicalTestFile(t, "definition.json", definition)
	writeCanonicalTestFile(t, "environment.json", environment)
	plan, err := CompileFiles(t.Context(), "definition.json", "environment.json", "test")
	if err != nil {
		t.Fatal(err)
	}
	writeCanonicalTestFile(t, "plan.json", plan)
	return plan
}
