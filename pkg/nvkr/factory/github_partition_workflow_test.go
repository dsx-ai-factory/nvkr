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
	raw := []byte("permissions: {contents: read}\njobs:\n  source:\n    runs-on: ubuntu-24.04\n  build:\n    needs: [source]\n    if: ${{ " + condition + " }}\n    runs-on: ubuntu-24.04\n    timeout-minutes: 30\n    steps:\n      - run: echo build\n        env:\n          TOKEN: ${{ secrets.BUILD_TOKEN }}\n  inspect:\n    needs: [source]\n    if: ${{ always() && " + condition + " }}\n    runs-on: ubuntu-24.04\n    timeout-minutes: 30\n    steps:\n      - run: echo inspect\n")
	model, err := newExecutionGraph(t.Context(), map[string]executionOperation{
		"source": {}, "build": {Needs: []string{"source"}}, "inspect": {Needs: []string{"source"}, Condition: executionAfterCompletion},
	})
	if err != nil {
		t.Fatal(err)
	}
	child, err := renderGitHubPartition(t.Context(), plan, raw, githubExecutionPartition{Jobs: []string{"build", "inspect"}}, model)
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
	if githubYAMLField(githubYAMLField(call, "secrets"), "BUILD_TOKEN") == nil {
		t.Fatal("worker omitted its explicit credential contract")
	}
	if githubYAMLField(root, "concurrency") != nil {
		t.Fatal("worker must not compete with entry workflow concurrency")
	}
	for _, ids := range [][]string{{"missing"}, {"build", "build"}, {"source", "build"}, {githubPartitionReporter}} {
		_, err := renderGitHubPartition(t.Context(), plan, raw, githubExecutionPartition{Jobs: ids}, model)
		assertFactoryErrorCode(t, err, e.CodePipelineDrift)
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
	names, err := githubPartitionSecretNames(githubYAMLMap("if", githubYAMLString("secrets.GATE != ''"),
		"run", githubYAMLString("echo secrets.LITERAL")))
	if err != nil || strings.Join(names, ",") != "GATE" {
		t.Fatalf("secret names=%v error=%v", names, err)
	}
	_, err = githubPartitionSecretNames(githubYAMLMap("if", githubYAMLString("toJSON(secrets) != ''")))
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
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
