// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

func TestGitHubUnreferencedFinalizerInterfacePreservesCommandsAndEnvironment(t *testing.T) {
	for _, test := range []struct {
		name        string
		container   bool
		development bool
		newline     bool
	}{
		{name: "host"},
		{name: "container with newline", container: true, newline: true},
		{name: "development with newline", development: true, newline: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan, _, source := authenticatedSourcePlan(t)
			// This direct projection fixture is not a complete admitted workflow.
			// Its finalizer has no summary/output observer, unlike normal rendering.
			// PinSet membership gives catalog revalidation its own credential scope.
			plan.CatalogSnapshot = &catalog.Catalog{Sources: map[string]catalog.Source{
				"fixture": {Repo: source, PinSet: &catalog.PinSet{}},
			}}
			plan.Environment.GitHubDevelopment = test.development
			actions := testActionRefs(t, plan)
			environment := githubCatalogSourceEnvironment(plan)
			if len(environment) == 0 {
				t.Fatal("fixture requires finalizer catalog credentials")
			}
			command := fmt.Sprintf("./nvkr-exec run finalize --definition %s --environment %s --plan %s --attempt \"${{ github.run_id }}-${{ github.run_attempt }}\" --workspace .nvkr/factory --out .nvkr/run.json",
				shellQuote(plan.DefinitionPath), shellQuote(plan.EnvironmentPath), shellQuote(githubLayoutFor(plan).planPath))
			var rendered bytes.Buffer
			writeCatalogValidationStep(&rendered, plan, "Finalize Run and check completeness", "finalize-catalog", command)
			var finalizer struct {
				Steps []githubStep `json:"steps"`
			}
			if err := yaml.UnmarshalStrict(append([]byte("steps:\n"), rendered.Bytes()...), &finalizer); err != nil || len(finalizer.Steps) != 1 {
				t.Fatalf("decode credentialed finalizer fixture: %v", err)
			}
			finalize := finalizer.Steps[0]
			finalize.Run = strings.TrimSuffix(finalize.Run, "\n")
			recover := githubStep{Name: "Recover planned carriers", ID: "recover-artifacts",
				Run: githubRunnerCommand(plan, "recover", "", ""), Env: map[string]string{"NVKR_JOB_RESULTS": "${{ toJSON(needs) }}"}}
			if test.newline {
				finalize.Run += "\n"
				recover.Run += "\n"
			}
			job := githubJob{Name: "Finalize fixture", RunsOn: "ubuntu-24.04", Steps: []githubStep{
				{Uses: actions.Checkout, With: map[string]string{"persist-credentials": "false"}},
				{Name: "Download NVKR runner", Uses: actions.Download, With: map[string]string{"name": "nvkr-exec-linux-amd64", "path": "."}},
				{Name: prepareHelperStepName, Run: prepareHelperRun}, recover, finalize,
			}}
			if test.container {
				job.Container = []byte(`{"image":"example.invalid/utility@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
			}
			document := githubWorkflowDocument{Jobs: map[string]githubJob{"finalizer-fixture": job}}
			raw, err := yaml.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			workflow, files, err := lowerGitHubActions(plan, raw, actions)
			if err != nil {
				t.Fatal(err)
			}
			workflow, err = shareGitHubActions(plan, workflow, files, actions)
			if err != nil {
				t.Fatal(err)
			}
			family, shell := "finalize-run", "bash -e {0}"
			if test.container {
				family, shell = family+"-container", "sh -e {0}"
			}
			member := githubActionsDirectoryFor(plan) + "shared/" + family + "/action.yml"
			calls := 0
			for _, call := range decodeGitHubWorkflow(t, workflow).Jobs["finalizer-fixture"].Steps {
				if call.Uses != "./"+strings.TrimSuffix(member, "/action.yml") {
					continue
				}
				calls++
				if len(call.Env) != 0 || call.With["nvkr-job-results"] != "${{ toJSON(needs) }}" || call.With["parameters"] != githubParametersExpression("part-1") {
					t.Fatalf("shared finalizer changes provider-result scope: %+v", call)
				}
				for key, value := range environment {
					if strings.Contains(value, "secrets.") && call.With[strings.ToLower(strings.ReplaceAll(key, "_", "-"))] != value {
						t.Fatalf("shared finalizer lost caller credential %q", key)
					}
				}
			}
			if calls != 1 {
				t.Fatalf("unreferenced finalizer must use one %s call, got %d", family, calls)
			}
			var action yamlv3.Node
			if err := yamlv3.Unmarshal(files[member], &action); err != nil || len(action.Content) != 1 {
				t.Fatalf("decode finalizer interface: %v", err)
			}
			steps := githubYAMLField(githubYAMLField(action.Content[0], "runs"), "steps")
			if steps == nil || len(steps.Content) != 2 || githubYAMLValue(steps.Content[0], "id") != "operation_recover" || githubYAMLValue(steps.Content[1], "id") != "operation_finalize" {
				t.Fatal("finalizer interface lost ordered recovery and finalization")
			}
			if githubYAMLValue(steps.Content[1], "run") != strings.TrimSuffix(finalize.Run, "\n") {
				t.Fatal("finalizer program was rewritten or moved into parameter data")
			}
			for _, step := range steps.Content {
				if githubYAMLValue(step, "shell") != shell {
					t.Fatal("finalizer interface changes the original shell")
				}
			}
			for key, value := range environment {
				if strings.HasPrefix(value, "!") && githubYAMLValue(githubYAMLField(steps.Content[1], "env"), key) != value {
					t.Fatalf("fixed credential helper %q no longer remains literal action code", key)
				}
			}
			data := files[githubActionDataPathFor(plan)]
			for _, forbidden := range []string{"secrets.", "toJSON(needs)", "./nvkr-exec", "${{"} {
				if bytes.Contains(data, []byte(forbidden)) {
					t.Fatalf("finalizer descriptor contains executable or provider context %q", forbidden)
				}
			}
			expanded, err := expandGitHubActions(plan, workflow, files)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decodeGitHubWorkflow(t, expanded), decodeGitHubWorkflow(t, raw)) {
				t.Fatal("finalizer interface changed command, environment, ID, newline, or order")
			}
		})
	}
}

// Expansion is a reversible presentation transform, not command authorization.
// Normal rendering keeps finalization inline for the closing summary; complete
// bundle validation still rejects program changes and credential substitution.
func TestGitHubFinalizerCommandAndEnvironmentAdmission(t *testing.T) {
	// Compile a small resolved snapshot with a PinSet-backed catalog credential.
	// These two admission guards do not depend on the maintained product graph.
	plan, _, source := authenticatedSourcePlan(t)
	plan = recompileGitHubStabilityPlan(t, plan, func(in *githubStabilityInputs) {
		for name, declaration := range in.catalog.Sources {
			if declaration.Repo != source {
				continue
			}
			declaration.PinSet = &catalog.PinSet{File: "versions.yaml", Members: map[string]catalog.PinSetMember{
				"member": {Source: "fixture-member", Path: "member.version"},
			}}
			in.catalog.Sources[name] = declaration
			in.catalog.Sources["fixture-member"] = catalog.Source{Repo: "https://github.com/test-owner/member", PinnedBy: name}
			return
		}
		t.Fatal("fixture has no matching catalog source")
	})
	if githubCatalogSourceEnvironment(plan)["NVKR_PIN_SOURCE_TOKEN_0"] == "" {
		t.Fatal("fixture requires admitted catalog-revalidation credentials")
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		mutate     func(*testing.T, *yamlv3.Node)
		diagnostic string
	}{
		{name: "appended finalizer program", mutate: func(t *testing.T, step *yamlv3.Node) {
			run := githubTestYAMLField(t, step, "run")
			run.Value = strings.TrimSuffix(run.Value, "\n") + "\nprintf injected\n"
		}, diagnostic: "differs from its exact admitted rendering"},
		{name: "substituted finalizer credential", mutate: func(t *testing.T, step *yamlv3.Node) {
			credential := githubTestYAMLField(t, step, "env", "NVKR_PIN_SOURCE_TOKEN_0")
			credential.Value = "${{ secrets.UNRELATED }}"
		}, diagnostic: "catalog source environment differs"},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := mutateGitHubBundleYAML(t, bundle, bundle.WorkflowPath, func(root *yamlv3.Node) {
				steps := githubTestYAMLField(t, root, "jobs", "nvkr-lifecycle-run", "steps")
				finalize := githubTestYAMLStep(t, steps, "id", "finalize-catalog")
				test.mutate(t, finalize)
			})
			err := ValidateGitHubBundle(t.Context(), plan, changed)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if !strings.Contains(err.Error(), test.diagnostic) {
				t.Fatalf("finalizer substitution reached the wrong admission guard: %v", err)
			}
		})
	}
}
