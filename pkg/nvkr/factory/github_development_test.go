// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

func githubDevelopmentTestPlan(t *testing.T) *Plan {
	t.Helper()
	plan := buildOnlyPlan(t, "ubuntu-24.04")
	plan.Environment.GitHubDevelopment = true
	var err error
	plan.EnvironmentDigest, err = plan.Environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func TestGitHubDevelopmentRejectsNonHostedRunnerBeforeCatalogAccess(t *testing.T) {
	for _, runner := range []string{"arc-amd64-kvm", "arc-arm64-kvm", "self-hosted"} {
		t.Run(runner, func(t *testing.T) {
			definition, environment := fixtureInputs("development", "worker", runner)
			environment.GitHubDevelopment = true
			// A missing catalog distinguishes capability admission from later I/O.
			definition.Catalog.Base = filepath.Join(t.TempDir(), "missing-catalog")
			plan, err := CompileDocuments(t.Context(), "definition.yaml", definition,
				"environment.yaml", environment, "v0.0.0 (test)")
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if plan != nil {
				t.Fatal("unsupported development runner produced a Plan")
			}
		})
	}
}

func TestGitHubDevelopmentRenderingSeparatesPolicyAndLayout(t *testing.T) {
	for _, development := range []bool{false, true} {
		name := "production"
		if development {
			name = "development"
		}
		t.Run(name, func(t *testing.T) {
			plan := buildOnlyPlan(t, "ubuntu-24.04")
			wantPlan, wantWorkflow, wantManifest := FactoryPlanPath, GitHubWorkflowPath, GeneratedManifestPath
			wantName := "nvkr-run-" + plan.Name
			wantCondition := githubExecutionCondition
			if development {
				plan = githubDevelopmentTestPlan(t)
				wantPlan, wantWorkflow, wantManifest = GitHubDevelopmentPlanPath, GitHubDevelopmentWorkflowPath, GitHubDevelopmentManifestPath
				wantName = "nvkr-dev-" + plan.Name
				wantCondition = "github.event_name == 'workflow_dispatch' && startsWith(github.ref, 'refs/heads/')"
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if bundle.PlanPath != wantPlan || bundle.WorkflowPath != wantWorkflow || bundle.ManifestPath != wantManifest {
				t.Fatalf("unexpected bundle layout: %+v", bundle)
			}
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			if document.Name != wantName || document.Concurrency.CancelInProgress != development || len(document.On) != 1 {
				t.Fatalf("incorrect workflow identity or admission: %+v", document)
			}
			if _, ok := document.On["workflow_dispatch"]; !ok {
				t.Fatal("development requires manual dispatch")
			}
			if _, ok := document.Jobs["nvkr-lifecycle-run"]; !ok {
				t.Fatal("fixture omits finalizer")
			}
			for id, job := range document.Jobs {
				if !strings.Contains(job.If, wantCondition) {
					t.Fatalf("job %s omits exact execution policy: %s", id, job.If)
				}
				if job.Environment != "" || job.Permissions["id-token"] != "" || job.Permissions["packages"] == "write" {
					t.Fatalf("build-only job %s has publication authority: %+v", id, job)
				}
				for _, step := range job.Steps {
					if strings.Contains(step.Run, " --plan ") && !strings.Contains(step.Run, " --plan "+wantPlan) {
						t.Fatalf("job %s reads another layout: %s", id, step.Run)
					}
				}
			}
			second, err := RenderGitHub(t.Context(), plan)
			if err != nil || !bytes.Equal(second.Files[bundle.WorkflowPath], bundle.Files[bundle.WorkflowPath]) {
				t.Fatalf("rendering was not deterministic: %v", err)
			}
		})
	}
}

func TestGitHubDevelopmentRejectsWorkflowPolicyTampering(t *testing.T) {
	plan := githubDevelopmentTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	condition := "github.event_name == 'workflow_dispatch' && startsWith(github.ref, 'refs/heads/')"
	bootstrap, bootstrapMember := githubTestBootstrap(t, bundle)
	stages, _, err := githubStages(plan)
	if err != nil || len(stages) == 0 {
		t.Fatalf("fixture has no execution stages: %v", err)
	}
	executeMember, _ := githubTestSharedInvocation(t, bundle, stages[0].ID, "execute", stages[0].Nodes[0].ID)
	for _, test := range []struct {
		name, member string
		mutate       func(*testing.T, *yamlv3.Node)
		code         e.Code
	}{
		{"bootstrap guard", bundle.WorkflowPath, func(t *testing.T, root *yamlv3.Node) {
			job := githubTestYAMLField(t, root, "jobs", bootstrap)
			if githubTestYAMLField(t, job, "if").Value != "${{ "+condition+" }}" {
				t.Fatal("fixture bootstrap lacks development admission")
			}
			githubYAMLRemove(job, "if")
		}, e.CodeInvalidCatalog},
		{"finalizer guard", bundle.WorkflowPath, func(t *testing.T, root *yamlv3.Node) {
			githubTestYAMLField(t, root, "jobs", "nvkr-lifecycle-run", "if").Value = "${{ always() }}"
		}, e.CodeEngineCapability},
		{"tag admission", bundle.WorkflowPath, func(t *testing.T, root *yamlv3.Node) {
			githubTestYAMLField(t, root, "jobs", bootstrap, "if").Value = "${{ " + strings.Replace(condition, "refs/heads/", "refs/", 1) + " }}"
		}, e.CodeInvalidCatalog},
		{"pull request trigger", bundle.WorkflowPath, func(t *testing.T, root *yamlv3.Node) {
			githubYAMLSet(githubTestYAMLField(t, root, "on"), "pull_request", githubYAMLMap())
		}, e.CodeEngineCapability},
		{"cancellation", bundle.WorkflowPath, func(t *testing.T, root *yamlv3.Node) {
			githubTestYAMLField(t, root, "concurrency", "cancel-in-progress").Value = "false"
		}, e.CodeEngineCapability},
		{"production Plan substitution", executeMember, func(t *testing.T, root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "operation_execute")
			run := githubTestYAMLField(t, step, "run")
			if strings.Count(run.Value, GitHubDevelopmentPlanPath) != 1 {
				t.Fatal("fixture execution lacks its development Plan")
			}
			run.Value = strings.Replace(run.Value, GitHubDevelopmentPlanPath, FactoryPlanPath, 1)
		}, e.CodeEngineCapability},
		{"added shell command", bootstrapMember, func(t *testing.T, root *yamlv3.Node) {
			step := githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), "id", "validate-catalog")
			run := githubTestYAMLField(t, step, "run")
			if strings.Count(run.Value, "--root . --check") != 1 {
				t.Fatal("fixture bootstrap lacks its exact regeneration gate")
			}
			run.Value = strings.Replace(run.Value, "--root . --check", "--root . --check; printf unexpected", 1)
		}, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := mutateGitHubBundleYAML(t, bundle, test.member, func(root *yamlv3.Node) { test.mutate(t, root) })
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, changed), test.code)
		})
	}
}

func TestGitHubDevelopmentRejectsBundlePathSubstitution(t *testing.T) {
	plan := githubDevelopmentTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"workflow", "manifest", "plan", "production layout", "engine"} {
		t.Run(name, func(t *testing.T) {
			changed := *bundle
			changed.Files = maps.Clone(bundle.Files)
			switch name {
			case "workflow":
				changed.WorkflowPath = ".github/workflows/other.yml"
			case "manifest":
				changed.ManifestPath = GeneratedManifestPath
			case "plan":
				changed.PlanPath = FactoryPlanPath
			case "production layout":
				changed.WorkflowPath, changed.ManifestPath, changed.PlanPath = GitHubWorkflowPath, GeneratedManifestPath, FactoryPlanPath
			case "engine":
				changed.Engine = EngineGitLab
			}
			if changed.WorkflowPath != bundle.WorkflowPath {
				delete(changed.Files, bundle.WorkflowPath)
				changed.Files[changed.WorkflowPath] = bundle.Files[bundle.WorkflowPath]
			}
			if changed.PlanPath != bundle.PlanPath {
				delete(changed.Files, bundle.PlanPath)
				changed.Files[changed.PlanPath] = bundle.Files[bundle.PlanPath]
			}
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), e.CodePipelineDrift)
		})
	}
}

func TestGitHubDevelopmentBundleWritesAndChecksStayIndependent(t *testing.T) {
	production, err := RenderGitHub(t.Context(), buildOnlyPlan(t, "ubuntu-24.04"))
	if err != nil {
		t.Fatal(err)
	}
	development, err := RenderGitHub(t.Context(), githubDevelopmentTestPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		first, second *GeneratedBundle
	}{
		{"production then development", production, development},
		{"development then production", development, production},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			for _, bundle := range []*GeneratedBundle{test.first, test.second, test.first, test.second} {
				if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
					t.Fatal(err)
				}
			}
			for _, bundle := range []*GeneratedBundle{production, development} {
				if err := CheckGeneratedBundle(t.Context(), root, bundle); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, development.PlanPath), []byte("changed development Plan"), 0o644); err != nil {
				t.Fatal(err)
			}
			assertFactoryErrorCode(t, CheckGeneratedBundle(t.Context(), root, development), e.CodePipelineDrift)
			if err := CheckGeneratedBundle(t.Context(), root, production); err != nil {
				t.Fatalf("development drift affected production: %v", err)
			}
		})
	}
}

func TestGitHubDevelopmentUtilityBindsItsOwnPlanBeforeLaunch(t *testing.T) {
	for _, state := range []string{"matching", "missing", "substituted"} {
		t.Run(state, func(t *testing.T) {
			plan := githubDevelopmentTestPlan(t)
			var selected Node
			for _, node := range plan.Graph.Nodes {
				if node.Kind == NodePackage && node.Container != "" {
					selected = node
					break
				}
			}
			if selected.ID == "" {
				t.Fatal("fixture has no utility container")
			}
			root := t.TempDir()
			t.Chdir(root)
			t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
			runtimePath := filepath.Join(root, plan.Environment.RuntimeBinary())
			if err := os.WriteFile(runtimePath, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" >> runtime-calls\nexit 0\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			writePlan := func(path string, value *Plan) {
				t.Helper()
				raw, err := canonical.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, raw, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			// Even identical bytes in the production layout cannot substitute for
			// a missing or changed development Plan.
			writePlan(FactoryPlanPath, plan)
			switch state {
			case "matching":
				writePlan(GitHubDevelopmentPlanPath, plan)
			case "substituted":
				changed := *plan
				changed.GeneratorVersion = "changed (test)"
				writePlan(GitHubDevelopmentPlanPath, &changed)
			}
			err := RunGitHubNode(t.Context(), plan, selected.ID, GitHubStepOptions{})
			if state != "matching" {
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				if _, err := os.Stat("runtime-calls"); !os.IsNotExist(err) {
					t.Fatal("unbound Plan reached utility runtime")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile("runtime-calls")
			if err != nil {
				t.Fatal(err)
			}
			digest, err := plan.Digest()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(raw), "--plan\n"+GitHubDevelopmentPlanPath+"\n") ||
				!strings.Contains(string(raw), "--plan-digest\n"+digest+"\n") || strings.Contains(string(raw), FactoryPlanPath) {
				t.Fatalf("utility launch lost exact development Plan: %s", raw)
			}
		})
	}
}

func TestGitHubDevelopmentRejectsNamedBundleBeforeCatalogAccess(t *testing.T) {
	for _, name := range []string{"project", "project-a"} {
		t.Run(name, func(t *testing.T) {
			definition, environment := fixtureInputs("development", "worker", "ubuntu-24.04")
			environment.GitHubDevelopment = true
			environment.GitHubBundle = name
			definition.Catalog.Base = filepath.Join(t.TempDir(), "missing-catalog")
			plan, err := CompileDocuments(t.Context(), "definition.yaml", definition, "environment.yaml", environment, "v0.0.0 (test)")
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if plan != nil {
				t.Fatal("unsupported mode combination produced a Plan")
			}
			rendered := githubDevelopmentTestPlan(t)
			rendered.Environment.GitHubBundle = name
			bundle, err := RenderGitHub(t.Context(), rendered)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if bundle != nil {
				t.Fatal("unsupported mode combination emitted generated bytes")
			}
		})
	}
}
