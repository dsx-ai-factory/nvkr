// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	authored "github.com/NVIDIA/nke-recipes/pkg/nkd/definition"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"sigs.k8s.io/yaml"
)

func githubTestPlan(t *testing.T) *Plan {
	t.Helper()
	base, err := catalog.LoadContext(context.Background(), testCatalogProvider(t))
	if err != nil {
		t.Fatal(err)
	}
	stack, err := catalog.ApplyStack(base)
	if err != nil {
		t.Fatal(err)
	}
	definition, delivery := fixtureInputs("github-static", "worker", "ubuntu-24.04")
	delivery.Publication = &authored.Publication{RepositoryPrefix: "ghcr.io/example/nkd-tools", CredentialRef: "github:NKD_REGISTRY_AUTH"}
	plan, err := Compile(context.Background(), definition, delivery, stack, "v1.2.3 (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	plan.DefinitionPath = "examples/factory.yaml"
	plan.EnvironmentPath = "examples/environment.yaml"
	return plan
}

func TestRenderGitHubProducesStaticallyValidCompleteBundle(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	workflow := bundle.Files[GitHubWorkflowPath]
	for _, required := range [][]byte{
		[]byte("qualification: " + TierStaticUnqualified.String()),
		// The bootstrap provisions Go via the pinned setup-go action (from .go-version) rather
		// than assuming a preinstalled toolchain.
		[]byte("uses: " + plan.Actions["setup-go"].Uses()),
		[]byte("go-version-file: '.go-version'"),
		[]byte("cache: 'false'"),
		// Factory helpers own operation isolation and Plan-derived carrier selections.
		[]byte("factory github run-node --plan"),
		[]byte("factory github restore-inputs --plan"),
		[]byte("factory github pack-outputs --plan"),
		[]byte("packages: write"),
		[]byte("secrets.NKD_REGISTRY_AUTH"),
		[]byte("persist-credentials: 'false'"),
	} {
		if !bytes.Contains(workflow, required) {
			t.Fatalf("workflow omits %q\n%s", required, workflow)
		}
	}
	if err := ValidateGitHubBundle(context.Background(), plan, bundle); err != nil {
		t.Fatal(err)
	}
}

func TestValidateGitHubBundleRejectsIsolationAndTransportTampering(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	original := bundle.Files[GitHubWorkflowPath]
	tests := []struct {
		name string
		old  []byte
		new  []byte
		code e.Code
	}{
		{name: "execution-event", code: e.CodeEngineCapability, old: []byte(githubExecutionCondition), new: []byte("true")},
		{name: "job-timeout", code: e.CodeEngineCapability, old: []byte("timeout-minutes: 15"), new: []byte("timeout-minutes: 0")},
		{name: "stage-timeout", code: e.CodeEngineCapability, old: []byte("timeout-minutes: 60"), new: []byte("timeout-minutes: 59")},
		{name: "cancel-running-publication", code: e.CodeEngineCapability, old: []byte("cancel-in-progress: false"), new: []byte("cancel-in-progress: true")},
		{name: "unplanned-launch-argument", code: e.CodeEngineCapability, old: []byte("factory github run-node --plan"), new: []byte("factory github run-node --network bridge --plan")},
		{name: "node-dispatch", code: e.CodeEngineCapability, old: []byte("factory github run-node --plan"), new: []byte("factory execute-node --plan")},
		{name: "setup-go-version", old: []byte("go-version-file: '.go-version'"), new: []byte("go-version-file: 'go.mod'")},
		{name: "setup-go-cache", code: e.CodeInvalidCatalog, old: []byte("cache: 'false'"), new: []byte("cache: 'true'")},
		{name: "setup-go-cache-removed", code: e.CodeInvalidCatalog, old: []byte("cache: 'false'"), new: nil},
		{name: "output", old: []byte("if-no-files-found: error"), new: []byte("if-no-files-found: ignore")},
		{name: "checkout-credential", old: []byte("persist-credentials: 'false'"), new: []byte("persist-credentials: 'true'")},
		{name: "pull-request-trigger", code: e.CodeEngineCapability, old: []byte("workflow_dispatch: {}"), new: []byte("workflow_dispatch: {}\n  pull_request: {}")},
		{name: "push-trigger", code: e.CodeEngineCapability, old: []byte("workflow_dispatch: {}"), new: []byte("workflow_dispatch: {}\n  push: {}")},
		{name: "regeneration-gate", old: []byte("--root . --check"), new: []byte("--root . --dry-run")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := *bundle
			mutated.Files = make(map[string][]byte, len(bundle.Files))
			for name, raw := range bundle.Files {
				mutated.Files[name] = append([]byte(nil), raw...)
			}
			mutated.Files[GitHubWorkflowPath] = bytes.Replace(original, test.old, test.new, 1)
			mutated.WorkflowDigest = pipeline.FileDigest(mutated.Files[GitHubWorkflowPath])
			err := ValidateGitHubBundle(context.Background(), plan, &mutated)
			if err == nil {
				t.Fatal("tampered workflow passed static validation")
			}
			if test.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != test.code {
					t.Fatalf("want %s, got %v", test.code, err)
				}
			}
		})
	}
}

// The helper arrives from download-artifact WITHOUT its execute bit, and every input
// Restore step is itself a `./nkd-exec` invocation. The rendered job must therefore carry
// a dedicated preparation step directly after the helper download, before any helper use.
func TestRenderGitHubPreparesHelperBeforeFirstUse(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	var document githubWorkflowDocument
	if err := yaml.UnmarshalStrict(bundle.Files[GitHubWorkflowPath], &document); err != nil {
		t.Fatal(err)
	}
	restoring := 0
	for jobID, job := range document.Jobs {
		if strings.HasPrefix(jobID, "build-runner-") {
			continue
		}
		if job.Steps[1].Uses != plan.Actions["download-artifact"].Uses() || job.Steps[1].With["name"] == "" {
			t.Fatalf("job %q does not download the helper as its second step", jobID)
		}
		if job.Steps[2].Name != prepareHelperStepName || strings.TrimSpace(job.Steps[2].Run) != prepareHelperRun {
			t.Fatalf("job %q does not prepare the helper immediately after downloading it: %+v", jobID, job.Steps[2])
		}
		for index, step := range job.Steps[:2] {
			if strings.Contains(step.Run, "./nkd-exec") {
				t.Fatalf("job %q invokes the helper at step %d, before it is executable", jobID, index)
			}
		}
		if strings.Contains(joinRuns(job.Steps[3:]), prepareHelperRun) {
			t.Fatalf("job %q chmods the helper a second time; preparation must happen exactly once", jobID)
		}
		if strings.Contains(joinRuns(job.Steps), "factory github restore-inputs") {
			restoring++
		}
	}
	if restoring == 0 {
		t.Fatal("test plan has no input-bearing job, so it cannot exercise the Restore ordering")
	}
}

// The pre-fix rendering — chmod folded into the Execute step, after the Restore steps —
// must be rejected by the validator rather than silently accepted.
func TestValidateGitHubBundleRejectsLateHelperPreparation(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	original := bundle.Files[GitHubWorkflowPath]
	legacy := bytes.ReplaceAll(original,
		[]byte("      - name: "+prepareHelperStepName+"\n        run: "+prepareHelperRun+"\n"), nil)
	legacy = bytes.ReplaceAll(legacy, []byte("        run: |\n          ./nkd-exec"),
		[]byte("        run: |\n          "+prepareHelperRun+"\n          ./nkd-exec"))
	if bytes.Equal(legacy, original) {
		t.Fatal("legacy workflow reconstruction changed nothing")
	}
	mutated := *bundle
	mutated.Files = make(map[string][]byte, len(bundle.Files))
	for name, raw := range bundle.Files {
		mutated.Files[name] = append([]byte(nil), raw...)
	}
	mutated.Files[GitHubWorkflowPath] = legacy
	mutated.WorkflowDigest = pipeline.FileDigest(legacy)
	err = ValidateGitHubBundle(context.Background(), plan, &mutated)
	if err == nil {
		t.Fatal("workflow that chmods the helper after using it passed static validation")
	}
	if !strings.Contains(err.Error(), prepareHelperStepName) {
		t.Fatalf("rejection does not name the missing preparation step: %v", err)
	}
}

func testActionRefs(t *testing.T, plan *Plan) githubActionRefs {
	t.Helper()
	refs, err := resolveGitHubActions(plan.Actions)
	if err != nil {
		t.Fatal(err)
	}
	return refs
}

func TestGitHubExplicitDispatchBranchPolicy(t *testing.T) {
	for _, platform := range []string{"hosted", "linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			var plan *Plan
			if platform == "hosted" {
				plan = githubTestPlan(t)
			} else {
				plan = arcTestPlan(t, platform)
			}

			oldDigest := plan.EnvironmentDigest
			plan.Environment.GitHubDispatchBranches = []string{"feat/release"}
			digest, err := plan.Environment.Digest()
			if err != nil {
				t.Fatal(err)
			}
			plan.EnvironmentDigest = digest
			if digest == oldDigest {
				t.Fatal("branch authority missing from Environment identity")
			}
			want := "github.event_name == 'workflow_dispatch' && (github.ref == format('refs/heads/{0}', github.event.repository.default_branch) || github.ref == 'refs/heads/feat/release')"
			if got := githubDispatchCondition(plan); got != want {
				t.Fatalf("unexpected branch guard: %s", got)
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			document := decodeGitHubWorkflow(t, bundle.Files[GitHubWorkflowPath])
			for name, job := range document.Jobs {
				if strings.HasPrefix(name, "build-runner-") {
					continue
				}
				if !strings.Contains(job.If, want) {
					t.Fatalf("job %s lost exact dispatch policy: %s", name, job.If)
				}
			}
			changed := *bundle
			changed.Files = map[string][]byte{}
			for name, raw := range bundle.Files {
				changed.Files[name] = raw
			}
			changed.Files[GitHubWorkflowPath] = bytes.ReplaceAll(bundle.Files[GitHubWorkflowPath], []byte("refs/heads/feat/release"), []byte("refs/heads/other"))
			changed.WorkflowDigest = pipeline.FileDigest(changed.Files[GitHubWorkflowPath])
			assertFactoryErrorCode(t, ValidateGitHubBundle(t.Context(), plan, &changed), e.CodeEngineCapability)
			assertFactoryErrorCode(t, AdmitLocal(t.Context(), plan, LocalRunOptions{}), e.CodeEngineCapability)
		})
	}
}
