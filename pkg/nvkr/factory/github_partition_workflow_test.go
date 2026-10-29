// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

func TestGitHubPartitionWorkflowPreservesJobIdentityAndFailurePolicy(t *testing.T) {
	plan := githubTestPlan(t)
	condition := githubDispatchCondition(plan)
	raw := []byte("permissions: {contents: read}\njobs:\n  source:\n    runs-on: ubuntu-24.04\n  build:\n    needs: [source]\n    if: ${{ " + condition + " }}\n    runs-on: ubuntu-24.04\n    timeout-minutes: 30\n    steps:\n      - run: echo build\n        env:\n          TOKEN: ${{ secrets.BUILD_TOKEN }}\n          PUBLISH: ${{ secrets['SECRETS'] }}\n  inspect:\n    needs: [source]\n    if: ${{ always() && " + condition + " }}\n    runs-on: ubuntu-24.04\n    timeout-minutes: 30\n    steps:\n      - run: echo inspect\n")
	model, err := newExecutionGraph(t.Context(), map[string]executionOperation{
		"source": {}, "build": {Needs: []string{"source"}}, "inspect": {Needs: []string{"source"}, Condition: executionAfterCompletion},
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := renderGitHubPartition(t.Context(), plan, raw, githubExecutionPartition{Jobs: []string{"build", "inspect"}}, model, nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc yamlv3.Node
	if err := yamlv3.Unmarshal(child, &doc); err != nil {
		t.Fatal(err)
	}
	root := doc.Content[0]
	jobs := githubYAMLField(root, "jobs")
	if len(jobs.Content) != 6 || githubYAMLField(jobs, "build") == nil || githubYAMLField(jobs, "inspect") == nil {
		t.Fatal("partition changed original job IDs")
	}
	build := githubYAMLField(jobs, "build")
	if !strings.Contains(githubYAMLValue(build, "if"), "!cancelled()") { //nolint:misspell // GitHub expression function spelling.
		t.Fatal("ordinary job became immune to workflow cancellation")
	}
	if githubYAMLField(build, "needs") != nil || !strings.Contains(githubYAMLValue(build, "if"), "fromJSON(inputs.dependencies)['source'].result == 'success'") {
		t.Fatal("ordinary job lost prerequisite success admission")
	}
	if strings.Contains(githubYAMLValue(githubYAMLField(jobs, "inspect"), "if"), "fromJSON") {
		t.Fatal("always job incorrectly requires prerequisite success")
	}
	report := githubYAMLField(jobs, githubPartitionReporter)
	if githubYAMLValue(githubYAMLField(report, "outputs"), "results") != "${{ toJSON(needs) }}" {
		t.Fatal("report does not preserve original outcomes")
	}
	call := githubYAMLField(githubYAMLField(root, "on"), "workflow_call")
	// The caller forwards exactly these declarations.
	if declared := githubYAMLField(call, "secrets"); len(declared.Content) != 4 ||
		githubYAMLField(declared, "BUILD_TOKEN") == nil || githubYAMLField(declared, "SECRETS") == nil {
		t.Fatal("worker omitted its explicit credential contract")
	}
	if githubYAMLField(root, "concurrency") != nil {
		t.Fatal("worker must not compete with entry workflow concurrency")
	}
	for _, ids := range [][]string{{"missing"}, {"build", "build"}, {"source", "build"}, {githubPartitionReporter}} {
		_, err := renderGitHubPartition(t.Context(), plan, raw, githubExecutionPartition{Jobs: ids}, model, nil)
		assertFactoryErrorCode(t, err, e.CodePipelineDrift)
	}
}

func TestGitHubPartitionExports(t *testing.T) {
	plan := githubTestPlan(t)
	raw := []byte("permissions: {contents: read}\njobs:\n  build:\n    if: ${{ " + githubDispatchCondition(plan) + " }}\n    runs-on: ubuntu-24.04\n")
	model, err := newExecutionGraph(t.Context(), map[string]executionOperation{"build": {}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		exports []githubPartitionExport
		want    []string
	}{
		{"valid", []githubPartitionExport{{Name: "x", Members: []string{"build"}, Statuses: true}, {Name: "y", Members: []string{"build"}}}, []string{"x", "x-statuses", "y"}},
		{"foreign member", []githubPartitionExport{{Name: "x", Members: []string{"other"}}}, nil},
		{"reserved results", []githubPartitionExport{{Name: "results", Members: []string{"build"}}}, nil},
		{"reserved statuses", []githubPartitionExport{{Name: "statuses", Members: []string{"build"}}}, nil},
		{"duplicate", []githubPartitionExport{{Name: "x", Members: []string{"build"}}, {Name: "x", Members: []string{"build"}}}, nil},
		{"derived after explicit", []githubPartitionExport{{Name: "x-statuses", Members: []string{"build"}}, {Name: "x", Members: []string{"build"}, Statuses: true}}, nil},
		{"explicit after derived", []githubPartitionExport{{Name: "x", Members: []string{"build"}, Statuses: true}, {Name: "x-statuses", Members: []string{"build"}}}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			child, err := renderGitHubPartition(t.Context(), plan, raw, githubExecutionPartition{Jobs: []string{"build"}}, model, test.exports)
			if test.want == nil {
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var doc yamlv3.Node
			if err := yamlv3.Unmarshal(child, &doc); err != nil {
				t.Fatal(err)
			}
			outputs := githubYAMLField(githubYAMLField(githubYAMLField(doc.Content[0], "on"), "workflow_call"), "outputs")
			for _, name := range append([]string{"results", "statuses"}, test.want...) {
				if githubYAMLField(outputs, name) == nil {
					t.Fatalf("missing output %q", name)
				}
			}
			if len(outputs.Content) != 2*(2+len(test.want)) {
				t.Fatalf("unexpected output count %d", len(outputs.Content)/2)
			}
		})
	}
}

func TestGitHubPartitionRefusesUnprojectedDependencyExpressions(t *testing.T) {
	plan := githubTestPlan(t)
	for _, expression := range []string{
		"needs.source.result", "needs['source'].result", "NEEDS.source.result", "toJSON( needs )",
	} {
		t.Run(expression, func(t *testing.T) {
			job := githubYAMLMap("if", githubYAMLString("${{ "+githubDispatchCondition(plan)+" }}"),
				"env", githubYAMLMap("INPUT", githubYAMLString("${{ "+expression+" }}")))
			_, err := projectGitHubPartitionJob(plan, "consumer", job, []string{"source"}, nil, executionAfterSuccess)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
		})
	}
}

func TestGitHubPartitionSecretReferences(t *testing.T) {
	for _, test := range []struct {
		value  string
		want   string
		refuse bool
	}{
		{"${{ secrets.BUILD_TOKEN }}", "BUILD_TOKEN", false},
		{"${{ Secrets.build_token }}", "BUILD_TOKEN", false},
		{"${{ secrets['BUILD_TOKEN'] }}", "BUILD_TOKEN", false},
		{"${{ secrets . BUILD_TOKEN }}", "BUILD_TOKEN", false},
		{"${{ secrets.GITHUB_TOKEN }}", "", false},
		{"${{ secrets[inputs.name] }}", "", true},
		{"${{ toJSON(secrets) }}", "", true},
		{"printf '%s\\n' secrets", "", false},
		{"echo secrets.CONFIG ${{ github.sha }}", "", false},
		{"${{ format('}}', secrets.LATE) }}", "LATE", false},
		{"${{ format('}}', toJSON(secrets)) }}", "", true},
		{"${{ secrets.OPEN", "", true},
		{"${{ format('uses secrets: {0}', github.sha) }}", "", false},
		{"${{ format('secrets.QUOTED {0}', secrets.REAL) }}", "REAL", false},
		{"${{ format('it''s {0}', toJSON(secrets)) }}", "", true},
		{"${{ vars.secrets }}", "", false},
		{"${{ fromJSON('{\"secrets\":\"synthetic-value\"}').secrets }}", "", false},
		{"${{ vars.secrets && secrets.REAL }}", "REAL", false},
		{"${{ vars.secrets && toJSON(secrets) }}", "", true},
		{"${{ secrets.SECRETS }}", "SECRETS", false},
		{"${{ secrets['SECRETS'] }}", "SECRETS", false},
		{"${{ Secrets [ 'secrets' ] }}", "SECRETS", false},
		{"${{ secrets . Secrets }}", "SECRETS", false},
		{"${{ vars.secrets.SECRETS && secrets.REAL }}", "REAL", false},
		{"${{ secrets['it''s'] }}", "", true},
		{"${{ secrets[' }}", "", true},
		{"${{ secrets }}", "", true},
		{"${{ my-secrets.VALUE }}", "", false},
	} {
		t.Run(test.value, func(t *testing.T) {
			names, err := githubPartitionSecretNames(githubYAMLString(test.value))
			if test.refuse {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				return
			}
			if err != nil || strings.Join(names, ",") != test.want {
				t.Fatalf("secret names=%v error=%v", names, err)
			}
		})
	}
}

func TestGitHubPartitionSecretReferencesInBareConditions(t *testing.T) {
	workflow := func(jobIf, stepIf, envIf string) *yamlv3.Node {
		return githubYAMLMap("jobs", githubYAMLMap("build", githubYAMLMap(
			"if", githubYAMLString(jobIf),
			"env", githubYAMLMap("if", githubYAMLString(envIf)),
			"steps", &yamlv3.Node{Kind: yamlv3.SequenceNode, Content: []*yamlv3.Node{
				githubYAMLMap("if", githubYAMLString(stepIf), "run", githubYAMLString("echo secrets.LITERAL")),
			}})))
	}
	names, err := githubPartitionSecretNames(workflow("secrets.JOB != ''", "secrets.STEP != ''", "secrets.DATA"))
	if err != nil || strings.Join(names, ",") != "JOB,STEP" {
		t.Fatalf("secret names=%v error=%v", names, err)
	}
	if _, err := githubPartitionSecretNames(workflow("true", "true", "secrets")); err != nil {
		t.Fatalf("env.if data refused: %v", err)
	}
	for _, refused := range [][2]string{{"toJSON(secrets) != ''", "true"}, {"true", "toJSON(secrets) != ''"}} {
		_, err = githubPartitionSecretNames(workflow(refused[0], refused[1], "data"))
		assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	}
}

func TestGitHubPartitionPreservesFailureAncestryThroughRecovery(t *testing.T) {
	plan := githubTestPlan(t)
	ordinary := "${{ " + githubDispatchCondition(plan) + " }}"
	always := "${{ always() && " + githubDispatchCondition(plan) + " }}"
	for _, test := range []struct{ name, condition, want string }{
		{"ordinary recovery", ordinary, "recovery"},
		{"always recovery", always, "recovery,source"},
	} {
		t.Run(test.name, func(t *testing.T) {
			jobs := githubYAMLMap(
				"source", githubYAMLMap("if", githubYAMLString(ordinary)),
				"recovery", githubYAMLMap("if", githubYAMLString(test.condition), "needs", githubNeedsSequence([]string{"source"})),
				"consumer", githubYAMLMap("if", githubYAMLString(ordinary), "needs", githubNeedsSequence([]string{"recovery"})))
			recoveryCondition := executionAfterSuccess
			if test.condition == always {
				recoveryCondition = executionAfterCompletion
			}
			model, err := newExecutionGraph(t.Context(), map[string]executionOperation{
				"source": {}, "recovery": {Needs: []string{"source"}, Condition: recoveryCondition}, "consumer": {Needs: []string{"recovery"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			parents, err := model.prerequisiteResults(t.Context(), "consumer")
			if err != nil || strings.Join(parents, ",") != test.want {
				t.Fatalf("prerequisites=%v error=%v", parents, err)
			}
			for _, owners := range []map[string]string{nil, {"source": "source-worker", "recovery": "recovery-worker"}} {
				job, err := projectGitHubPartitionJob(plan, "consumer", githubYAMLField(jobs, "consumer"), parents, owners, executionAfterSuccess)
				if err != nil {
					t.Fatal(err)
				}
				if test.condition == always && !strings.Contains(githubYAMLValue(job, "if"), "['source'].result == 'success'") {
					t.Fatal("successful recovery hid failed ancestor from ordinary consumer")
				}
			}
		})
	}
}
