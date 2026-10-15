// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func githubDiskBuildPlan(t *testing.T, platform, encryption string) *Plan {
	t.Helper()
	actions := githubTestPlan(t).Actions
	definition, environment, stack := vmFactoryInputs(t, platform, execution.CapabilityRequired)
	environment.GitHubARC, environment.Runtime.Runtime = "native-kvm-v1", "docker"
	environment.GitHubDispatchBranches = []string{"feature/fde"}
	for index := range environment.ExecutionClasses {
		class := &environment.ExecutionClasses[index]
		class.RunsOn = githubARCRunner(string(class.HostPlatform))
	}
	component := stack.Catalog.Components["image"]
	component.Disk.Encryption = encryption
	component.BuildCommand = nil
	component.Packer = &catalog.PackerConfig{
		ProjectPath: "os-image/packer", PackerVersion: "1.14.2", BuildName: "image.qemu.node",
		ArchitectureVariable: "architecture", AcceleratorVariable: "accelerator", MaterialManifestVariable: "material_manifest",
		OutputDirectoryVariable: "output_directory", AcquisitionArchive: "acquisitions.tar",
	}
	if encryption == "luks2" {
		component.Packer.Variables = map[string]string{"luks_passphrase": "changeit"}
	}
	stack.Catalog.Actions = actions
	stack.Catalog.Components["image"] = component
	stack, err := catalog.ApplyStack(stack.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), definition, environment, stack, "dev (unknown)")
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = "definition.yaml", "environment.yaml"
	return plan
}

func githubDiskBuildOwner(t *testing.T, plan *Plan) *LocalRunResult {
	t.Helper()
	owner := localDiskRunResult(t, plan)
	payload := testQCOW2HeaderPayload()
	held := &fakeDiskProducerResult{payload: payload, digest: pipeline.FileDigest(payload), size: int64(len(payload))}
	_, artifacts := planIndex(plan)
	raw := artifacts[plan.LifecycleOutputs[0].ArtifactID]
	_, producer, err := plan.producerInstruction(planNode(plan, raw.Producer))
	if err != nil {
		t.Fatal(err)
	}
	owner.diskCompletions = []diskProducerCompletion{{NodeID: raw.Producer, PlanDigest: owner.PlanDigest, Payload: fakeDiskPayload(held),
		Result: pipeline.ArtifactResult{Component: producer.Component, OutputType: PackageDiskImage, Qualification: pipeline.ArtifactUnqualified,
			Toolchain: producer.Toolchain, BuilderType: producer.BuilderType, EngineClass: producer.BuilderType,
			InputDigest: "sha256:" + strings.Repeat("d", 64), Path: producer.ArtifactPath, SHA256: held.digest, Size: held.size}}}
	t.Cleanup(func() { _ = owner.Close() })
	return owner
}

func TestGitHubDiskBuildOnlyGeneration(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		for _, encryption := range []string{"none", "luks2"} {
			t.Run(platform+"/"+encryption, func(t *testing.T) {
				plan := githubDiskBuildPlan(t, platform, encryption)
				bundle, err := RenderGitHub(t.Context(), plan)
				if err != nil {
					t.Fatal(err)
				}
				workflow := string(expandedGitHubWorkflow(t, bundle))
				for _, absent := range []string{"Retain inspection diagnostics", "id-token: write", "packages: write", githubReleaseEnvironment, "factory github run-node --plan .github/nvkr-run/plan.json --node package/"} {
					if strings.Contains(workflow, absent) {
						t.Fatalf("build-only workflow contains %q", absent)
					}
				}
				for _, required := range []string{"refs/heads/feature/fde", "factory github run-disk", string(TierStaticUnqualified), "Build and retain unqualified disk", githubArtifactName(plan.LifecycleOutputs[0].ArtifactID)} {
					if !strings.Contains(workflow, required) {
						t.Fatalf("build-only workflow omits %q", required)
					}
				}
				for _, node := range plan.Graph.Nodes {
					if node.Kind == NodePackage || node.Kind == NodeSBOM || node.Kind == NodePublish || signFamilyNode(node.Kind) {
						t.Fatalf("build-only graph has unsupported operation %s", node.ID)
					}
				}
			})
		}
	}
}

func TestGitHubDiskBuildOnlyHeldExportAndHostedObservation(t *testing.T) {
	for _, encryption := range []string{"none", "luks2"} {
		t.Run(encryption, func(t *testing.T) {
			plan := githubDiskBuildPlan(t, "linux/amd64", encryption)
			owner := githubDiskBuildOwner(t, plan)
			t.Chdir(t.TempDir())
			expected := plan.LifecycleOutputs[0]
			_, artifacts := planIndex(plan)
			if err := executeHeldDiskObservation(t.Context(), plan, planNode(plan, expected.Name+"/terminal-observe"), artifacts, owner); err != nil {
				t.Fatal(err)
			}
			if err := completeGitHubDiskBuild(t.Context(), plan, expected, owner); err != nil {
				t.Fatal(err)
			}
			if err := owner.Close(); err != nil {
				t.Fatal(err)
			}
			observed, err := ObserveHostedDiskBuild(t.Context(), plan, expected, artifacts)
			if err != nil || observed.Encryption != encryption || observed.Digest != pipeline.FileDigest(testQCOW2HeaderPayload()) || observed.PlanDigest != owner.PlanDigest {
				t.Fatalf("restored observation = %+v, %v", observed, err)
			}
			if err := ValidateHostedOutcomes(t.Context(), ".nvkr/factory", plan); err != nil {
				t.Fatal(err)
			}
			for _, artifact := range artifacts {
				if artifact.Kind == PackageDiskImage || artifact.Kind == "producer-result" {
					info, err := os.Stat(artifact.Path)
					if err != nil || info.Mode().Perm() != 0o444 {
						t.Fatalf("export not immutable: %v, %v", info, err)
					}
				}
			}
		})
	}
}

func TestGitHubDiskBuildOnlyRejectsSubstitution(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *Plan, map[string]Artifact)
		want   e.Code
	}{
		{"changed payload", func(t *testing.T, p *Plan, a map[string]Artifact) {
			t.Helper()
			path := a[p.LifecycleOutputs[0].ArtifactID].Path
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, bytes.Repeat([]byte{1}, 512), 0o644); err != nil {
				t.Fatal(err)
			}
		}, e.CodeVerifyFailed},
		{"wrong result owner", func(t *testing.T, p *Plan, a map[string]Artifact) {
			t.Helper()
			raw := a[p.LifecycleOutputs[0].ArtifactID]
			id := diskPublicationProducerResultID(p.LifecycleOutputs[0], raw)
			record := a[id]
			record.Component = "sibling"
			a[id] = record
		}, e.CodePipelineDrift},
		{"missing result", func(_ *testing.T, p *Plan, a map[string]Artifact) {
			delete(a, diskPublicationProducerResultID(p.LifecycleOutputs[0], a[p.LifecycleOutputs[0].ArtifactID]))
		}, e.CodePipelineDrift},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := githubDiskBuildPlan(t, "linux/amd64", "luks2")
			owner := githubDiskBuildOwner(t, plan)
			t.Chdir(t.TempDir())
			expected := plan.LifecycleOutputs[0]
			if err := completeGitHubDiskBuild(t.Context(), plan, expected, owner); err != nil {
				t.Fatal(err)
			}
			_, artifacts := planIndex(plan)
			test.mutate(t, plan, artifacts)
			_, err := ObserveHostedDiskBuild(t.Context(), plan, expected, artifacts)
			assertFactoryErrorCode(t, err, test.want)
		})
	}
}

func TestGitHubDiskBuildOnlyRejectsSiblingArtifactMapSubstitution(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload bool
		record  bool
	}{
		{name: "payload", payload: true},
		{name: "producer result", record: true},
		{name: "payload and producer result", payload: true, record: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := githubDiskBuildPlan(t, "linux/amd64", "luks2")
			definition := original.Definition.Clone()
			sibling := definition.Outputs[0]
			sibling.Name = "sibling-image"
			definition.Outputs = append(definition.Outputs, sibling)
			stack, err := catalog.ApplyStack(original.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := Compile(t.Context(), definition, original.Environment, stack, original.GeneratorVersion)
			if err != nil {
				t.Fatal(err)
			}
			expected, other := plan.LifecycleOutputs[0], plan.LifecycleOutputs[1]
			if expected.BuildDigest != other.BuildDigest || expected.ArtifactID == other.ArtifactID {
				t.Fatal("fixture requires distinct named outputs with equal logical build identities")
			}
			owner := githubDiskBuildOwner(t, plan)
			t.Chdir(t.TempDir())
			if err := completeGitHubDiskBuild(t.Context(), plan, expected, owner); err != nil {
				t.Fatal(err)
			}
			_, artifacts := planIndex(plan)
			for _, substitution := range []struct {
				enabled bool
				target  string
				sibling string
			}{
				{test.payload, expected.ArtifactID, other.ArtifactID},
				{test.record, diskPublicationProducerResultID(expected, artifacts[expected.ArtifactID]),
					diskPublicationProducerResultID(other, artifacts[other.ArtifactID])},
			} {
				if substitution.enabled {
					actual := artifacts[substitution.sibling]
					actual.Path = artifacts[substitution.target].Path
					artifacts[substitution.target] = actual
				}
			}
			_, err = ObserveHostedDiskBuild(t.Context(), plan, expected, artifacts)
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
		})
	}
}

func TestGitHubDiskBuildExportDoesNotReplaceOrCommitFailedBytes(t *testing.T) {
	for _, mode := range []string{"repeat", "canceled", "invalid payload"} {
		t.Run(mode, func(t *testing.T) {
			plan := githubDiskBuildPlan(t, "linux/amd64", "luks2")
			owner := githubDiskBuildOwner(t, plan)
			t.Chdir(t.TempDir())
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			expected := plan.LifecycleOutputs[0]
			want := e.CodeVerifyFailed
			switch mode {
			case "repeat":
				if err := completeGitHubDiskBuild(ctx, plan, expected, owner); err != nil {
					t.Fatal(err)
				}
				want = e.CodeImmutableViolation
			case "canceled":
				cancel()
				want = e.CodeTimeout
			case "invalid payload":
				fakeDiskPayloadState(owner.diskCompletions[0].Payload).payload[0] = 0
			}
			assertFactoryErrorCode(t, completeGitHubDiskBuild(ctx, plan, expected, owner), want)
			_, artifacts := planIndex(plan)
			path := artifacts[expected.ArtifactID].Path
			if mode == "repeat" {
				raw, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(raw, testQCOW2HeaderPayload()) {
					t.Fatalf("replaced export: %v", err)
				}
			} else if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("failed export visible: %v", err)
			}
			matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".nvkr-download-*"))
			if err != nil || len(matches) != 0 {
				t.Fatalf("temporary export survived: %v, %v", matches, err)
			}
		})
	}
}

func TestGitHubDiskBuildOnlyPrivateImageBoundary(t *testing.T) {
	plan := githubDiskBuildPlan(t, "linux/amd64", "luks2")
	for _, test := range []struct {
		prefix string
		want   bool
	}{
		{"ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer", true},
		{"ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/toolchains/packer", true},
		{"ghcr.io/dsx-ai-factory/nke-recipes/m2-candidate/other/packer", false},
		{"ghcr.io/other/nke-recipes/m2-candidate/toolchains/packer", false},
	} {
		t.Run(test.prefix, func(t *testing.T) {
			if got := githubPrivateToolchain(plan, test.prefix+"@sha256:"+strings.Repeat("a", 64)); got != test.want {
				t.Fatalf("private preparation = %t, want %t", got, test.want)
			}
		})
	}
}
