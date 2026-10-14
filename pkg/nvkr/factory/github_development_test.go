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
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
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
			document := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath])
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
				if development || !strings.HasPrefix(id, "build-runner-") {
					if !strings.Contains(job.If, wantCondition) {
						t.Fatalf("job %s omits exact execution policy: %s", id, job.If)
					}
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
	for _, test := range []struct {
		name, before, after string
		code                e.Code
	}{
		{"bootstrap guard", "    if: ${{ " + condition + " }}\n", "", e.CodeInvalidCatalog},
		{"finalizer guard", "always() && " + condition, "always()", e.CodeEngineCapability},
		{"tag admission", "startsWith(github.ref, 'refs/heads/')", "startsWith(github.ref, 'refs/')", e.CodeInvalidCatalog},
		{"pull request trigger", "workflow_dispatch: {}", "workflow_dispatch: {}\n  pull_request: {}", e.CodeEngineCapability},
		{"cancellation", "cancel-in-progress: true", "cancel-in-progress: false", e.CodeEngineCapability},
		{"production Plan substitution", "factory github run-node --plan " + GitHubDevelopmentPlanPath, "factory github run-node --plan " + FactoryPlanPath, e.CodeEngineCapability},
		{"added shell command", "--root . --check", "--root . --check; printf unexpected", e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := *bundle
			changed.Files = maps.Clone(bundle.Files)
			raw := bytes.Replace(bundle.Files[bundle.WorkflowPath], []byte(test.before), []byte(test.after), 1)
			if bytes.Equal(raw, bundle.Files[bundle.WorkflowPath]) {
				t.Fatal("mutation did not change fixture")
			}
			changed.Files[bundle.WorkflowPath] = raw
			changed.WorkflowDigest = pipeline.FileDigest(raw)
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), test.code)
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
