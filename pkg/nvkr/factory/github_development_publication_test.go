// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"maps"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"sigs.k8s.io/yaml"
)

func githubDevelopmentPublicationTestPlan(t *testing.T) *Plan {
	t.Helper()
	return developmentPublicationPlan(t, referenceTestPlan(t), "ghcr.io/example/nvkr/development")
}

// Recompile the complete retained selection, including output-specific catalog
// snapshots. Development changes transport and signing policy, not producers.
func developmentPublicationPlan(t *testing.T, base *Plan, prefix string) *Plan {
	t.Helper()
	environment := base.Environment.Clone()
	environment.GitHubDevelopment = true
	environment.GitHubDispatchBranches = nil
	environment.Publication.Mode = "per-output-v1"
	environment.Publication.RepositoryPrefix = prefix
	environment.Publication.CredentialRef = development.WriteCredentialRef
	environment.Publication.Anonymous = false
	environment.Publication.PlainHTTP = false
	environment.Publication.ImageTags = nil
	namespace, err := development.ParseNamespace(prefix)
	if err != nil {
		t.Fatal(err)
	}
	environment.Signing.Identity = namespace.Identity()
	environment.Signing.Issuer = development.Issuer
	environment.Signing.Mode = SigningModeCosign
	stack := &catalog.Stack{Catalog: base.CatalogSnapshot, Digest: base.CatalogDigest, Applied: base.AppliedOverlays}
	stacks := map[string]*catalog.Stack{}
	for _, output := range base.Definition.Outputs {
		if output.Catalog == nil {
			continue
		}
		for _, build := range base.Builds {
			if build.Name == nativeOutputSelections(output)[0].Name {
				stacks[output.Name] = &catalog.Stack{Catalog: build.CatalogSnapshot, Digest: build.CatalogDigest, Applied: build.AppliedOverlays}
				break
			}
		}
	}
	plan, err := compileWithTargetStacks(t.Context(), base.Definition, environment, stack, stacks,
		base.GeneratorVersion, base.DefinitionPath, base.EnvironmentPath)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestGitHubDevelopmentPublicationScopesRealStages(t *testing.T) {
	for _, test := range []struct {
		name string
		plan func(*testing.T) *Plan
		disk bool
	}{
		{"hosted", githubDevelopmentPublicationTestPlan, false},
		{"native amd64 disk", func(t *testing.T) *Plan {
			return developmentPublicationPlan(t, arcTestPlan(t, "linux/amd64"), "ghcr.io/example/nvkr/development")
		}, true},
		{"native arm64 disk", func(t *testing.T) *Plan {
			return developmentPublicationPlan(t, arcTestPlan(t, "linux/arm64"), "ghcr.io/example/nvkr/development")
		}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := test.plan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath])
			if plan.Qualification != TierPipelineAttested || document.Concurrency.CancelInProgress {
				t.Fatal("signed development lost real evidence or can cancel publication")
			}
			stages, _, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			for _, stage := range stages {
				job := document.Jobs[stage.ID]
				if !strings.Contains(job.If, "startsWith(github.ref, 'refs/heads/')") || job.Permissions["packages"] == "write" {
					t.Fatalf("stage escaped development event/token policy: %+v", job)
				}
				if stage.DiskOutput != "" {
					counts["disk"]++
					if job.Environment != githubDevelopmentARCEnvironment || job.RunsOn != githubARCRunner(stage.Nodes[0].Platform) ||
						job.Env["NVKR_CREDENTIAL"] != "" || job.Permissions["id-token"] != "" {
						t.Fatalf("disk stage escaped native credential boundary: %+v", job)
					}
				}
				for _, node := range stage.Nodes {
					counts[node.Kind]++
					secret, user := "", ""
					switch node.Kind {
					case NodePublish, NodeSign:
						secret, user = "${{ secrets.NVKR_DEV_REGISTRY_WRITE }}", "${{ vars.NVKR_DEV_REGISTRY_WRITE_USER }}"
					case NodeVerifyPublication, NodeAssembleRelease:
						secret, user = "${{ secrets.NVKR_DEV_REGISTRY_READ }}", "${{ vars.NVKR_DEV_REGISTRY_READ_USER }}"
					}
					if job.Env["NVKR_CREDENTIAL"] != secret || job.Env["NVKR_REGISTRY_USER"] != user {
						t.Fatalf("%s credential differs from its operation scope: %+v", node.Kind, job.Env)
					}
					if node.Kind == NodePublish || signFamilyNode(node.Kind) {
						if job.Environment != githubDevelopmentReleaseEnvironment || job.Permissions["packages"] != "" {
							t.Fatalf("%s has production protection/token access: %+v", node.Kind, job)
						}
						wantOIDC := ""
						if node.Kind == NodeSign || node.Kind == NodeAttestEvidence {
							wantOIDC = "write"
						}
						if job.Permissions["id-token"] != wantOIDC {
							t.Fatalf("%s OIDC permission differs: %+v", node.Kind, job.Permissions)
						}
					}
				}
			}
			for _, kind := range []string{NodePublish, NodeSign, NodeVerifyPublication, NodeAssembleRelease, NodeAttestEvidence} {
				if counts[kind] == 0 {
					t.Fatalf("development omitted real %s stages", kind)
				}
			}
			if test.disk && counts["disk"] != 1 {
				t.Fatal("development omitted its native disk group")
			}
			finalizer := document.Jobs["nvkr-lifecycle-run"]
			if finalizer.Environment != githubDevelopmentReleaseEnvironment || finalizer.Permissions["packages"] != "" || finalizer.Permissions["id-token"] != "" ||
				finalizer.Env["NVKR_CREDENTIAL"] != "${{ secrets.NVKR_DEV_REGISTRY_READ }}" || finalizer.Env["NVKR_REGISTRY_USER"] != "${{ vars.NVKR_DEV_REGISTRY_READ_USER }}" {
				t.Fatalf("finalizer is not scoped to development reads: %+v", finalizer)
			}
		})
	}
}

func TestGitHubDevelopmentPublicationRejectsScopeTampering(t *testing.T) {
	plan := githubDevelopmentPublicationTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	_, jobs, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	selected := map[string]string{}
	for _, node := range plan.Graph.Nodes {
		selected[node.Kind] = jobs[node.ID]
	}
	for _, test := range []struct {
		name, kind string
		change     func(*githubJob)
	}{
		{"writer on reader", NodeVerifyPublication, func(job *githubJob) { job.Env["NVKR_CREDENTIAL"] = "${{ secrets.NVKR_DEV_REGISTRY_WRITE }}" }},
		{"production token", NodePublish, func(job *githubJob) { job.Env["NVKR_CREDENTIAL"] = "${{ secrets.GITHUB_TOKEN }}" }},
		{"actor username", NodeSign, func(job *githubJob) { job.Env["NVKR_REGISTRY_USER"] = "${{ github.actor }}" }},
		{"production write scope", NodeSign, func(job *githubJob) { job.Permissions["packages"] = "write" }},
		{"OIDC on reader", NodeAssembleRelease, func(job *githubJob) { job.Permissions["id-token"] = "write" }},
		{"production protection", NodePublish, func(job *githubJob) { job.Environment = githubReleaseEnvironment }},
		{"registry on SBOM attestation", NodeAttestEvidence, func(job *githubJob) {
			job.Env = map[string]string{"NVKR_CREDENTIAL": "${{ secrets.NVKR_DEV_REGISTRY_WRITE }}"}
		}},
		{"short retention", NodePublish, func(job *githubJob) {
			for i := range job.Steps {
				if job.Steps[i].With["retention-days"] == "7" {
					job.Steps[i].With["retention-days"] = "1"
					return
				}
			}
			t.Fatal("fixture omits publication carrier upload")
		}},
		{"finalizer writer", "", func(job *githubJob) { job.Env["NVKR_CREDENTIAL"] = "${{ secrets.NVKR_DEV_REGISTRY_WRITE }}" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath])
			id := selected[test.kind]
			if test.kind == "" {
				id = "nvkr-lifecycle-run"
			}
			job := document.Jobs[id]
			test.change(&job)
			document.Jobs[id] = job
			raw, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			raw = append([]byte("# qualification: "+plan.Qualification.String()+"\n# planDigest: "+bundle.PlanDigest+"\n"), raw...)
			changed := *bundle
			changed.Files = maps.Clone(bundle.Files)
			changed.Files[bundle.WorkflowPath], changed.WorkflowDigest = raw, pipeline.FileDigest(raw)
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), e.CodeEngineCapability)
		})
	}
}

func TestMaintainedM2DevelopmentPreservesCompleteGraphAndPrivateToolchainReads(t *testing.T) {
	base, err := LoadPlanContext(t.Context(), "../../../"+FactoryPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	plan := developmentPublicationPlan(t, base, "ghcr.io/dsx-ai-factory/nvkr/development")
	if len(plan.LifecycleOutputs) != len(base.LifecycleOutputs) || len(plan.Graph.Nodes) != len(base.Graph.Nodes) {
		t.Fatal("development changed the maintained selected output or operation closure")
	}
	fdePlatforms := map[string]bool{}
	for i, build := range plan.Builds {
		if build.Name != base.Builds[i].Name || build.BuildDigest != base.Builds[i].BuildDigest {
			t.Fatalf("development changed producer identity for %s", build.Name)
		}
		for _, instruction := range build.Graph.Nodes {
			if instruction.Disk != nil && instruction.Disk.Encryption == "luks2" {
				fdePlatforms[build.Platform] = true
			}
		}
	}
	if !fdePlatforms["linux/amd64"] || !fdePlatforms["linux/arm64"] || len(fdePlatforms) != 2 {
		t.Fatal("development omitted the maintained native public-bootstrap FDE producers")
	}
	// The production qualification handoff needs every original output record.
	// Development still authenticates the complete release with six controls;
	// do not download unused handoff records into its finalizer.
	controls := 0
	for _, artifact := range plan.Graph.Artifacts {
		if !finalizerArtifact(plan, artifact) {
			continue
		}
		controls++
		switch artifact.ID {
		case "metadata/release", "publication/release", "signature/release", "predicate/release", "verification/release", "tool/" + plan.Definition.CosignTool:
		default:
			t.Fatalf("development finalizer restores unused qualification record %s", artifact.ID)
		}
	}
	if controls != 6 {
		t.Fatalf("development finalizer lost its authenticated release controls: %d", controls)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	document := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath])
	disks, fetches := 0, 0
	for _, job := range document.Jobs {
		if job.Permissions["packages"] == "write" {
			t.Fatal("development gained workflow-token package writes")
		}
		for _, step := range job.Steps {
			if step.Env[githubImageToken] == "" {
				continue
			}
			if job.Permissions["packages"] != "read" || job.Permissions["id-token"] != "" ||
				step.Env[githubImageToken] != "${{ github.token }}" || job.Env["NVKR_CREDENTIAL"] != "" {
				t.Fatal("native toolchain preparation lost its independent read-only scope")
			}
			if job.Environment == githubDevelopmentARCEnvironment {
				disks++
			} else {
				fetches++
			}
		}
	}
	if disks != 4 || fetches == 0 {
		t.Fatalf("development omitted private native preparation: %d disks / %d fetches", disks, fetches)
	}
	t.Logf("complete M2 development workflow: %d bytes", len(bundle.Files[bundle.WorkflowPath]))
}
