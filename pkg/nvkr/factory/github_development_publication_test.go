// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
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
		if output.Catalog == nil && len(base.CatalogCandidates) == 0 {
			continue
		}
		for _, build := range base.Builds {
			if build.Name == nativeOutputSelections(output)[0].Name {
				stacks[output.Name] = &catalog.Stack{Catalog: build.CatalogSnapshot, Digest: build.CatalogDigest, Applied: build.AppliedOverlays}
				break
			}
		}
	}
	plan, err := compileWithTargetStacksFor(t.Context(), base.Definition, environment, stack, stacks,
		base.GeneratorVersion, base.DefinitionPath, base.EnvironmentPath, nil, base.CatalogCandidates)
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
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
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
				// One release job runs several operations. Each credential stays
				// on its operation's step and never reaches job scope.
				for _, key := range []string{"NVKR_CREDENTIAL", "NVKR_REGISTRY_USER", githubRepositoryTokenEnv} {
					if _, present := job.Env[key]; present {
						t.Fatalf("stage %s binds %s at job scope", stage.ID, key)
					}
				}
				wantOIDC := ""
				for _, node := range stage.Nodes {
					counts[node.Kind]++
					secret, user := "", ""
					switch node.Kind {
					case NodePublish, NodeSign:
						secret, user = "${{ secrets.NVKR_DEV_REGISTRY_WRITE }}", "${{ vars.NVKR_DEV_REGISTRY_WRITE_USER }}"
					case NodeVerifyPublication, NodeAssembleRelease:
						secret, user = "${{ secrets.NVKR_DEV_REGISTRY_READ }}", "${{ vars.NVKR_DEV_REGISTRY_READ_USER }}"
					}
					repositoryToken := ""
					if node.Kind == NodePublish {
						repositoryToken = "${{ github.token }}"
					}
					if stage.DiskOutput == "" {
						step := githubNodeStep(t, &job, node.ID)
						if step.Env["NVKR_CREDENTIAL"] != secret || step.Env["NVKR_REGISTRY_USER"] != user {
							t.Fatalf("%s credential differs from its operation scope: %+v", node.Kind, step.Env)
						}
						if step.Env[githubRepositoryTokenEnv] != repositoryToken {
							t.Fatalf("%s repository metadata credential differs from its publication scope", node.Kind)
						}
					}
					if node.Kind == NodePublish || signFamilyNode(node.Kind) {
						if job.Environment != githubDevelopmentReleaseEnvironment || job.Permissions["packages"] != "" {
							t.Fatalf("%s has production protection/token access: %+v", node.Kind, job)
						}
					} else if job.Environment == githubDevelopmentReleaseEnvironment {
						t.Fatalf("%s shares a job with release authority", node.Kind)
					}
					if node.Kind == NodeSign || node.Kind == NodeAttestEvidence {
						wantOIDC = "write"
					}
				}
				if job.Permissions["id-token"] != wantOIDC {
					t.Fatalf("stage %s OIDC permission differs from its operations: %+v", stage.ID, job.Permissions)
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
				finalizer.Env["NVKR_CREDENTIAL"] != "${{ secrets.NVKR_DEV_REGISTRY_READ }}" || finalizer.Env["NVKR_REGISTRY_USER"] != "${{ vars.NVKR_DEV_REGISTRY_READ_USER }}" ||
				finalizer.Env[githubRepositoryTokenEnv] != "" {
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
	selectedNode := map[string]string{}
	for _, node := range plan.Graph.Nodes {
		selectedNode[node.Kind] = node.ID
	}
	// A release job runs several operations, so credential tampering targets the
	// execution step of one operation, not the job. Such a step is outside the
	// closed shared interface and reaches validation through the lower boundary.
	stepTampered := false
	stepEnv := func(kind string, change func(map[string]string)) func(*githubJob) {
		return func(job *githubJob) {
			step := githubNodeStep(t, job, selectedNode[kind])
			if step.Env == nil {
				step.Env = map[string]string{}
			}
			change(step.Env)
			stepTampered = true
		}
	}
	for _, test := range []struct {
		name, kind string
		change     func(*githubJob)
	}{
		{"writer on reader", NodeVerifyPublication, stepEnv(NodeVerifyPublication, func(env map[string]string) {
			env["NVKR_CREDENTIAL"] = "${{ secrets.NVKR_DEV_REGISTRY_WRITE }}"
		})},
		{"production token", NodePublish, stepEnv(NodePublish, func(env map[string]string) { env["NVKR_CREDENTIAL"] = "${{ secrets.GITHUB_TOKEN }}" })},
		{"missing repository token", NodePublish, stepEnv(NodePublish, func(env map[string]string) { delete(env, githubRepositoryTokenEnv) })},
		{"package token for repository", NodePublish, stepEnv(NodePublish, func(env map[string]string) {
			env[githubRepositoryTokenEnv] = "${{ secrets.NVKR_DEV_REGISTRY_WRITE }}"
		})},
		{"repository token on signing", NodeSign, stepEnv(NodeSign, func(env map[string]string) { env[githubRepositoryTokenEnv] = "${{ github.token }}" })},
		{"repository token on producer", NodeBuild, stepEnv(NodeBuild, func(env map[string]string) { env[githubRepositoryTokenEnv] = "${{ github.token }}" })},
		{"actor username", NodeSign, stepEnv(NodeSign, func(env map[string]string) { env["NVKR_REGISTRY_USER"] = "${{ github.actor }}" })},
		{"writer at job scope", NodePublish, func(job *githubJob) {
			job.Env = map[string]string{"NVKR_CREDENTIAL": "${{ secrets.NVKR_DEV_REGISTRY_WRITE }}"}
		}},
		{"source write scope", NodeSign, func(job *githubJob) { job.Permissions["contents"] = "write" }},
		{"OIDC on producer", NodeBuild, func(job *githubJob) {
			job.Permissions = map[string]string{"contents": "read", "id-token": "write"}
		}},
		{"OIDC dropped from signer", NodeSign, func(job *githubJob) { delete(job.Permissions, "id-token") }},
		{"production protection", NodePublish, func(job *githubJob) { job.Environment = githubReleaseEnvironment }},
		{"registry on SBOM attestation", NodeAttestEvidence, stepEnv(NodeAttestEvidence, func(env map[string]string) {
			env["NVKR_CREDENTIAL"] = "${{ secrets.NVKR_DEV_REGISTRY_WRITE }}"
		})},
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
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			id := selected[test.kind]
			if test.kind == "" {
				id = "nvkr-lifecycle-run"
			}
			job := document.Jobs[id]
			stepTampered = false
			test.change(&job)
			document.Jobs[id] = job
			raw, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			changed := (*GeneratedBundle)(nil)
			if stepTampered {
				changed = unsharedGitHubTestBundle(t, plan, bundle, raw)
			} else {
				changed = relowerGitHubTestBundle(t, plan, bundle, raw)
			}
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), e.CodeEngineCapability)
		})
	}
}

// githubNodeStep returns the execution step of one Plan operation in its job.
func githubNodeStep(t testing.TB, job *githubJob, nodeID string) *githubStep {
	t.Helper()
	for index := range job.Steps {
		if job.Steps[index].ID == githubJobID(nodeID) {
			return &job.Steps[index]
		}
	}
	t.Fatalf("job %q has no execution step for %s", job.Name, nodeID)
	return nil
}

func TestGitHubPublicationRepositoryIdentityCoversOutputAndReleaseRoots(t *testing.T) {
	for _, test := range []struct {
		name string
		plan func(*testing.T) *Plan
	}{
		{"production", referenceTestPlan},
		{"development", githubDevelopmentPublicationTestPlan},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := test.plan(t)
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			_, nodeJobs, err := githubStages(plan)
			if err != nil {
				t.Fatal(err)
			}
			// The repository token is bound to each publication's own step.
			publishers := map[string]string{}
			for _, node := range plan.Graph.Nodes {
				if node.Kind == NodePublish {
					publishers[githubJobID(node.ID)] = nodeJobs[node.ID]
				}
			}
			if len(publishers) != len(plan.LifecycleOutputs)+1 {
				t.Fatal("fixture must cover every output and the aggregate release publisher")
			}
			for jobID, job := range document.Jobs {
				if _, present := job.Env[githubRepositoryTokenEnv]; present {
					t.Fatalf("job %s binds the repository metadata credential at job scope", jobID)
				}
				for _, step := range job.Steps {
					token, present := step.Env[githubRepositoryTokenEnv]
					publisher := publishers[step.ID] == jobID && step.ID != ""
					if present != publisher || present && token != "${{ github.token }}" {
						t.Fatalf("job %s step %q received the wrong repository metadata credential scope", jobID, step.Name)
					}
					if present && job.Permissions["contents"] != "read" {
						t.Fatalf("publisher %s lost read-only repository metadata access", jobID)
					}
				}
			}
		})
	}
}

func TestDevelopmentPreservesProducerGraphAndPrivateToolchainReads(t *testing.T) {
	// A native disk and its vendor fetch cover both preparation sites. The
	// full maintained product adds no distinct development credential boundary.
	base := recompileGitHubStabilityPlan(t, privateImageTestPlan(t, "github:GITHUB_TOKEN"), func(inputs *githubStabilityInputs) {
		inputs.environment.Publication.Mode = "per-output-v1"
		inputs.environment.Publication.RepositoryPrefix = "ghcr.io/dsx-ai-factory/nvkr/m2-candidate"
		inputs.environment.ToolchainAccess.PrivateRoots = []string{"ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains"}
		root := inputs.catalog.Components["image"]
		for arch, image := range root.ToolchainImages {
			root.ToolchainImages[arch] = "ghcr.io/dsx-ai-factory/nvkr/m2-candidate/toolchains/packer-" + arch + image[strings.Index(image, "@sha256:"):]
		}
		inputs.catalog.Components["image"] = root
	})
	plan := developmentPublicationPlan(t, base, "ghcr.io/dsx-ai-factory/nvkr/development")
	if len(plan.LifecycleOutputs) != len(base.LifecycleOutputs) || len(plan.Graph.Nodes) != len(base.Graph.Nodes) {
		t.Fatal("development changed the selected output or operation closure")
	}
	for i, build := range plan.Builds {
		if build.Name != base.Builds[i].Name || build.BuildDigest != base.Builds[i].BuildDigest {
			t.Fatalf("development changed producer identity for %s", build.Name)
		}
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
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	disks, fetches, preflights := 0, 0, 0
	for _, job := range document.Jobs {
		if job.Permissions["packages"] == "write" {
			t.Fatal("development gained workflow-token package writes")
		}
		for _, step := range job.Steps {
			if step.Env[githubImageToken] == "" {
				continue
			}
			if job.Permissions["packages"] != "" || job.Permissions["id-token"] != "" ||
				step.Env[githubImageToken] != "${{ secrets.NVKR_REGISTRY_READ }}" || job.Env["NVKR_CREDENTIAL"] != "" {
				t.Fatal("native toolchain preparation lost its independent read-only scope")
			}
			if strings.Contains(step.Run, "factory github preflight-images ") {
				preflights++
				continue
			}
			if !strings.Contains(step.Run, "factory github prepare-images ") {
				t.Fatal("development reader escaped preflight and per-job preparation")
			}
			if job.Environment == githubDevelopmentARCEnvironment {
				disks++
			} else {
				fetches++
			}
		}
	}
	if disks != 1 || fetches == 0 || preflights != 2 {
		t.Fatalf("development omitted private native preparation or placement preflight: %d disks / %d fetches / %d preflights", disks, fetches, preflights)
	}
}
