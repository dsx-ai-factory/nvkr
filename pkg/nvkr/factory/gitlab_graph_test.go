// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yaml "go.yaml.in/yaml/v3"
)

// syntheticGitlabCondition is the hand-authored node shape for one projection
// case: a Plan node kind plus its direct raw-graph parents.
type syntheticGitlabCondition struct {
	kind    string
	parents []string
}

// syntheticGitlabProjection projects hand-authored shapes through the exact
// owner projectGitLabGraph uses, so scheduling rules are tested without
// recompiling a complete Plan.
func syntheticGitlabProjection(t *testing.T, shapes map[string]syntheticGitlabCondition) ([]gitlabOperation, error) {
	t.Helper()
	nodes := make(map[string]Node, len(shapes))
	jobs := make(map[string]string, len(shapes))
	full := make(map[string][]string, len(shapes))
	for id, shape := range shapes {
		nodes[id] = Node{ID: id, Kind: shape.kind}
		jobs[id] = gitlabNodeJob(id)
	}
	for id, shape := range shapes {
		for _, parent := range shape.parents {
			full[jobs[id]] = append(full[jobs[id]], jobs[parent])
		}
		full[jobs[id]] = uniqueSorted(full[jobs[id]])
	}
	return gitlabExecutionOperations(t.Context(), nodes, jobs, full)
}

func gitlabProjectedOperation(t *testing.T, operations []gitlabOperation, nodeID string) gitlabOperation {
	t.Helper()
	for _, operation := range operations {
		if operation.Node.ID == nodeID {
			return operation
		}
	}
	t.Fatalf("projection lost operation %q", nodeID)
	return gitlabOperation{}
}

func TestGitLabConditionalProjection(t *testing.T) {
	t.Run("ordinary reduced chain", func(t *testing.T) {
		operations, err := syntheticGitlabProjection(t, map[string]syntheticGitlabCondition{
			"p": {kind: NodeBuild},
			"q": {kind: NodeBuild, parents: []string{"p"}},
			"c": {kind: NodePackage, parents: []string{"q", "p"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		consumer := gitlabProjectedOperation(t, operations, "c")
		if want := []string{gitlabNodeJob("q")}; !slices.Equal(consumer.Needs, want) {
			t.Fatalf("transitive reduction was not preserved: needs=%v", consumer.Needs)
		}
		for _, id := range []string{"p", "q", "c"} {
			if operation := gitlabProjectedOperation(t, operations, id); operation.Condition != executionAfterSuccess {
				t.Fatalf("ordinary operation %q projected condition %d", id, operation.Condition)
			}
		}
	})

	t.Run("ancestor success through completion", func(t *testing.T) {
		operations, err := syntheticGitlabProjection(t, map[string]syntheticGitlabCondition{
			"p": {kind: NodeBuild},
			"v": {kind: NodeVerify, parents: []string{"p"}},
			"c": {kind: NodePackage, parents: []string{"v"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		verification := gitlabProjectedOperation(t, operations, "v")
		if verification.Condition != executionAfterCompletion {
			t.Fatal("verification node lost its completion condition")
		}
		if want := []string{gitlabNodeJob("p")}; !slices.Equal(verification.Needs, want) {
			t.Fatalf("verification needs=%v", verification.Needs)
		}
		consumer := gitlabProjectedOperation(t, operations, "c")
		if consumer.Condition != executionAfterSuccess {
			t.Fatal("ordinary consumer lost its success condition")
		}
		if want := []string{gitlabNodeJob("p"), gitlabNodeJob("v")}; !slices.Equal(consumer.Needs, want) {
			t.Fatalf("ordinary consumer dropped an ancestor success requirement: needs=%v", consumer.Needs)
		}
	})

	t.Run("consecutive completion nodes", func(t *testing.T) {
		operations, err := syntheticGitlabProjection(t, map[string]syntheticGitlabCondition{
			"p":  {kind: NodeBuild},
			"v1": {kind: NodeVerify, parents: []string{"p"}},
			"v2": {kind: NodeVerify, parents: []string{"v1"}},
			"c":  {kind: NodePackage, parents: []string{"v2"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"v1", "v2"} {
			if operation := gitlabProjectedOperation(t, operations, id); operation.Condition != executionAfterCompletion {
				t.Fatalf("completion operation %q projected condition %d", id, operation.Condition)
			}
		}
		if want := []string{gitlabNodeJob("v1")}; !slices.Equal(gitlabProjectedOperation(t, operations, "v2").Needs, want) {
			t.Fatal("completion operation did not keep its direct completion prerequisite")
		}
		consumer := gitlabProjectedOperation(t, operations, "c")
		if want := []string{gitlabNodeJob("p"), gitlabNodeJob("v1"), gitlabNodeJob("v2")}; !slices.Equal(consumer.Needs, want) {
			t.Fatalf("ordinary consumer lost the consecutive completion chain: needs=%v", consumer.Needs)
		}
	})

	t.Run("rendered needs and when match the typed projection", func(t *testing.T) {
		_, plan := gitlabFixture(t, 1)
		projection, err := projectGitLabGraph(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := RenderGitLab(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		var document map[string]any
		if err := yaml.Unmarshal(bundle.Files[GitLabWorkflowPath], &document); err != nil {
			t.Fatal(err)
		}
		consumers := 0
		for _, operation := range projection.Operations {
			job, ok := document[operation.Job].(map[string]any)
			if !ok {
				t.Fatalf("rendered workflow lost operation job %q", operation.Job)
			}
			want := "on_success"
			if operation.Condition == executionAfterCompletion {
				want = "always"
			}
			if job["when"] != want {
				t.Fatalf("operation %q rendered when %v for condition %d", operation.Node.ID, job["when"], operation.Condition)
			}
			entries, ok := job["needs"].([]any)
			if !ok || len(entries) == 0 {
				t.Fatalf("operation %q rendered without bootstrap needs", operation.Node.ID)
			}
			bootstrap, ok := entries[0].(map[string]any)
			platform, err := gitlabNodePlatform(plan, operation.Node)
			if err != nil {
				t.Fatal(err)
			}
			if !ok || bootstrap["job"] != gitlabBootstrapJob(platform) || bootstrap["artifacts"] != true {
				t.Fatalf("operation %q lost its bootstrap transport entry", operation.Node.ID)
			}
			scheduled := map[string]bool{}
			for _, entry := range entries[1:] {
				need, ok := entry.(map[string]any)
				if !ok || need["artifacts"] != false {
					t.Fatalf("operation %q rendered a non-scheduling need entry", operation.Node.ID)
				}
				scheduled[need["job"].(string)] = true
			}
			if len(scheduled) != len(operation.Needs) {
				t.Fatalf("operation %q rendered %d needs for %d projected", operation.Node.ID, len(scheduled), len(operation.Needs))
			}
			for _, parent := range operation.Needs {
				if !scheduled[parent] {
					t.Fatalf("operation %q dropped projected need %q", operation.Node.ID, parent)
				}
			}
			// Complete original membership: in this fixture every original input
			// producer remains a rendered prerequisite even when the direct edge
			// was transitively reduced through the completion-scheduled verify.
			for _, input := range operation.Inputs {
				if !slices.Contains(operation.Needs, gitlabNodeJob(input.Producer)) {
					t.Fatalf("operation %q lost original input producer %q from needs %v", operation.Node.ID, input.Producer, operation.Needs)
				}
			}
			if len(operation.Inputs) != len(operation.Node.Inputs) || len(operation.Outputs) != len(operation.Node.Outputs) {
				t.Fatalf("operation %q dropped complete original membership", operation.Node.ID)
			}
			if operation.Node.Kind == NodeVerify {
				continue
			}
			for _, input := range operation.Inputs {
				if input.Kind == string(TierTerminalObservation) {
					consumers++
				}
			}
		}
		if consumers == 0 {
			t.Fatal("fixture exercised no completion-gated consumer")
		}
		consumer := gitlabProjectedOperation(t, projection.Operations, "package/other")
		verification := gitlabNodeJob("other/terminal-observe")
		producer := gitlabNodeJob("other/build/kubectl")
		if !slices.Contains(consumer.Needs, verification) || !slices.Contains(consumer.Needs, producer) {
			t.Fatalf("ordinary package consumer lost completion or ancestor success: needs=%v", consumer.Needs)
		}
		if gitlabProjectedOperation(t, projection.Operations, "other/terminal-observe").Condition != executionAfterCompletion {
			t.Fatal("terminal observation lost its completion condition")
		}
	})

	t.Run("deterministic projection", func(t *testing.T) {
		_, plan := gitlabFixture(t, 1)
		first, err := RenderGitLab(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		second, err := RenderGitLab(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatal("conditional projection is not deterministic")
		}
	})

	t.Run("coded unknown and cyclic rejection", func(t *testing.T) {
		for _, test := range []struct {
			name   string
			shapes map[string]syntheticGitlabCondition
		}{
			{"unknown dependency", map[string]syntheticGitlabCondition{
				"p": {kind: NodeBuild},
				"c": {kind: NodePackage, parents: []string{"ghost"}},
			}},
			{"cycle", map[string]syntheticGitlabCondition{
				"a": {kind: NodeBuild, parents: []string{"b"}},
				"b": {kind: NodeBuild, parents: []string{"a"}},
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				_, err := syntheticGitlabProjection(t, test.shapes)
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			})
		}
	})

	t.Run("needs bound after condition expansion", func(t *testing.T) {
		base, err := catalog.LoadContext(t.Context(), testCatalogProvider(t))
		if err != nil {
			t.Fatal(err)
		}
		template := base.Components["kubectl"]
		const producers = 49
		components := make([]string, 0, producers)
		for i := range producers {
			name := fmt.Sprintf("bound-%02d", i)
			component := template
			component.ArtifactPath = fmt.Sprintf("_output/local/bin/linux/{{.Arch}}/tool-%02d", i)
			base.Components[name] = component
			components = append(components, name)
		}
		slices.Sort(components)
		stack, err := catalog.ApplyStack(base)
		if err != nil {
			t.Fatal(err)
		}
		definition, environment := fixtureInputs("gitlab-condition-bound", "cli", "")
		definition.Outputs = []authored.DefinitionOutput{
			{Name: "cli", Platform: "linux/amd64", Format: authored.OutputExecutable, Components: components},
		}
		environment.RunsOn = ""
		environment.GitLab = &authored.GitLab{APIURL: "https://gitlab.example.com/api/v4", ProjectID: 123, APIReadCredentialRef: "gitlab:NVKR_API_READ", APIReadCredentialKind: "private-token", Runners: map[string]authored.GitLabRunner{"linux/amd64": {Tags: []string{"runner: native"}, BootstrapImage: "registry.example.com/go@sha256:" + strings.Repeat("a", 64)}}}
		plan, err := compileDocumentsWithLoader(t.Context(), "config/definition.json", definition, "config/environment.json", environment, "v0.0.0 (test)",
			func(context.Context, authored.Catalog) (*catalog.Stack, error) { return stack, nil })
		if err != nil {
			t.Fatal(err)
		}
		projection, err := projectGitLabGraph(t.Context(), plan)
		if err != nil {
			t.Fatal(err)
		}
		verification := gitlabProjectedOperation(t, projection.Operations, "cli/terminal-observe")
		if len(verification.Needs) != producers {
			t.Fatalf("verification projected %d needs, want %d", len(verification.Needs), producers)
		}
		bundle, err := RenderGitLab(t.Context(), plan)
		if bundle != nil {
			t.Fatal("needs-bound refusal produced a bundle")
		}
		assertFactoryErrorCode(t, err, e.CodeEngineCapability)
		if err == nil || !strings.Contains(err.Error(), "needs bound") {
			t.Fatalf("refusal did not name the native needs bound: %v", err)
		}
	})
}
