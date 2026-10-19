// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func referenceTestPlan(t *testing.T) *Plan {
	t.Helper()
	original := signedTestPlan(t, cosignSigningProfile(), ghcrTarget())
	env := original.Environment
	env.Publication.Mode = "per-output-v1"
	env.Publication.CredentialRef = "github:GITHUB_TOKEN"
	second := original.Definition.Outputs[0]
	second.Name = "worker-two"
	original.Definition.Outputs = append(original.Definition.Outputs, second)
	stack, err := catalog.ApplyStack(original.CatalogSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(t.Context(), original.Definition, env, stack, original.GeneratorVersion)
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath, plan.EnvironmentPath = original.DefinitionPath, original.EnvironmentPath
	return plan
}

func TestReferenceReleaseGraphHasNoAggregatePayloadEdges(t *testing.T) {
	plan := referenceTestPlan(t)
	nodes, artifacts := planIndex(plan)
	aggregate := nodes["package/release"]
	if aggregate.Kind != NodeAssembleRelease {
		t.Fatal("no reference assembly")
	}
	for _, id := range aggregate.Inputs {
		switch artifacts[id].Kind {
		case ArtifactPublicationMetadata, ArtifactSigningRecord, ArtifactProvenancePredicate, ArtifactVerificationRecord, "published-reference", "pipeline-tool":
		default:
			t.Fatalf("aggregate consumes %s (%s)", id, artifacts[id].Kind)
		}
	}
	count := 0
	for _, a := range artifacts {
		if finalizerArtifact(plan, a) {
			count++
		}
	}
	if count != 6+3*len(plan.LifecycleOutputs) {
		t.Fatalf("finalizer downloads %d artifacts", count)
	}
	for _, output := range plan.LifecycleOutputs {
		n := nodes["package/"+outputPublicationPackage(output.Name)]
		selected := selectedPublicationOutputs(plan, n)
		if len(selected) != 1 || selected[0].Name != output.Name {
			t.Fatal("publication shard does not select exact output")
		}
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range stages {
		for _, node := range stage.Nodes {
			if node.PackageKind == PackageOCI && len(stage.Nodes) != 1 {
				t.Fatal("publication payloads regrouped")
			}
		}
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateGitHubBundle(t.Context(), plan, bundle); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bundle.Files[GitHubWorkflowPath]), "refs/heads/feat/") {
		t.Fatal("branch exception")
	}
	finalizer := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle)).Jobs["nvkr-lifecycle-run"]
	if finalizer.Environment != githubReleaseEnvironment || finalizer.Permissions["packages"] != "read" || finalizer.Permissions["id-token"] != "" {
		t.Fatal("finalizer must use protected release credentials with read-only registry access")
	}
	for _, kind := range []string{NodeAssembleRelease, NodeVerifyPublication} {
		p := githubSigningPermissions(kind, SigningModeCosign)
		if p["packages"] != "read" || p["id-token"] != "" {
			t.Fatal("metadata reader can publish or mint identity")
		}
	}
}

func TestReferenceFinalizerRequiresAllProviderJobs(t *testing.T) {
	for _, status := range []string{"restored:success", "job-failed", ""} {
		t.Run(status, func(t *testing.T) {
			plan := referenceTestPlan(t)
			workspace := filepath.Join(t.TempDir(), "factory")
			for _, a := range plan.Graph.Artifacts {
				if finalizerArtifact(plan, a) {
					if err := writeFactoryFile(t.Context(), HostedRestoreOutcomePath(workspace, a.ID), []byte("restored:success\n"), 0644); err != nil {
						t.Fatal(err)
					}
				}
			}
			if status != "" {
				if err := writeFactoryFile(t.Context(), HostedRestoreOutcomePath(workspace, "all-provider-jobs"), []byte(status+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			err := ValidateHostedRestores(t.Context(), workspace, plan)
			if status == "restored:success" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
			}
		})
	}
}

func TestReferencePublicationRejectsUnscopedCredentials(t *testing.T) {
	for _, credential := range []string{"", "github:GITHUB_TOKEN", "github:REGISTRY_AUTH"} {
		t.Run(credential, func(t *testing.T) {
			plan := referenceTestPlan(t)
			env := plan.Environment.Clone()
			env.Publication.CredentialRef = credential
			env.Publication.Anonymous = credential == ""
			if env.Publication.Anonymous {
				env.Publication.RepositoryPrefix = "registry.example/nvkr-tools"
			}
			stack, err := catalog.ApplyStack(plan.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			plan, err = Compile(t.Context(), plan.Definition, env, stack, plan.GeneratorVersion)
			if err != nil {
				t.Fatal(err)
			}
			plan.DefinitionPath, plan.EnvironmentPath = "definitions/definition.yaml", "definitions/environment.yaml"
			bundle, err := RenderGitHub(t.Context(), plan)
			if credential == "github:REGISTRY_AUTH" {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				if bundle != nil {
					t.Fatal("unsupported credential produced a partial bundle")
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReferenceFinalizerPreservesContextError(t *testing.T) {
	plan := referenceTestPlan(t)
	for _, mode := range []string{"cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			var ctx context.Context
			var cancel context.CancelFunc
			if mode == "deadline" {
				ctx, cancel = context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
			} else {
				ctx, cancel = context.WithCancel(t.Context())
				cancel()
			}
			defer cancel()
			err := ValidateHostedRestores(ctx, filepath.Join(t.TempDir(), "factory"), plan)
			assertFactoryErrorCode(t, err, e.CodeTimeout)
			if !errors.Is(err, ctx.Err()) {
				t.Fatalf("lost interruption cause: %v", err)
			}
		})
	}
}
