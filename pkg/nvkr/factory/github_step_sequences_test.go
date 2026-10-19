// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

func githubStepSequenceFixture(t *testing.T, plan *Plan, actions githubActionRefs, runner string) *yamlv3.Node {
	t.Helper()
	preflight, err := githubPlatformCheck(plan, runner)
	if err != nil {
		t.Fatal(err)
	}
	shared := "./" + githubActionsDirectoryFor(plan) + "shared/"
	return githubYAMLMap("runs-on", githubYAMLString(runner), "steps", &yamlv3.Node{Kind: yamlv3.SequenceNode, Tag: "!!seq", Content: []*yamlv3.Node{
		githubYAMLMap("name", githubYAMLString(githubPlatformCheckName), "run", githubYAMLString(preflight)),
		githubYAMLMap("name", githubYAMLString("Check out workflow source"), "uses", githubYAMLString(actions.Checkout),
			"with", githubYAMLMap("persist-credentials", githubYAMLString("false"))),
		githubYAMLMap("name", githubYAMLString("Prepare NVKR runner and job parameters"), "id", githubYAMLString(githubContextStepID),
			"uses", githubYAMLString(shared+"prepare-stage"), "with", githubYAMLMap("runner-artifact", githubYAMLString("nvkr-exec-"+shortStableName(runner)))),
		githubYAMLMap("name", githubYAMLString("execute node part-1"), "uses", githubYAMLString(shared+"execute-node"),
			"with", githubYAMLMap("parameters", githubYAMLString(githubParametersExpression("part-1")))),
	}})
}

func TestGitHubStepSequencesPreserveExpansionAndJobParameters(t *testing.T) {
	plan := &Plan{}
	plan.Environment.GitHubRunners = map[string]string{"linux/amd64": "ubuntu-24.04"}
	actions := testActionRefs(t, githubTestPlan(t))
	raw := githubActionInterfaceFixture(actions, []githubInterfaceFixture{
		{name: "first", nodes: 1, carriers: 1}, {name: "second", nodes: 1, carriers: 1},
		{name: "grouped-first", nodes: 2, carriers: 2}, {name: "grouped-second", nodes: 2, carriers: 2},
	})
	var preflight bytes.Buffer
	if err := writeGitHubPlatformCheck(&preflight, plan, "ubuntu-24.04"); err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("    steps:\n"), append([]byte("    steps:\n"), preflight.Bytes()...))
	raw = bytes.ReplaceAll(raw, []byte("nvkr-exec-linux-amd64"), []byte("nvkr-exec-ubuntu-24-04"))
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	jobs := githubYAMLField(document.Content[0], "jobs")
	for index := 1; index < len(jobs.Content); index += 2 {
		job := jobs.Content[index]
		// Shared steps retain independently authored job policy and credentials.
		githubYAMLSet(job, "if", githubYAMLString(fmt.Sprintf("${{ needs.upstream_%d.result == 'success' }}", index)))
		githubYAMLSet(job, "permissions", githubYAMLMap("packages", githubYAMLString("read")))
		githubYAMLSet(job, "env", githubYAMLMap("NVKR_CREDENTIAL", githubYAMLString(fmt.Sprintf("${{ secrets.REGISTRY_%d }}", index))))
	}
	raw, err := encodeGitHubYAML(&document)
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
	for _, anchor := range []string{"steps-ubuntu-24-04-node", "steps-ubuntu-24-04-two-nodes"} {
		if !bytes.Contains(workflow, []byte("steps: &"+anchor)) || !bytes.Contains(workflow, []byte("steps: *"+anchor)) {
			t.Fatalf("complete sequence %q was not shared", anchor)
		}
	}
	for range 2 {
		expanded, err := expandGitHubActions(plan, workflow, files)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decodeGitHubWorkflow(t, expanded), decodeGitHubWorkflow(t, raw)) {
			t.Fatal("sequence expansion changed per-job parameters, credentials, policy or operation order")
		}
	}
	assertGitHubSequenceAliasGraph(t, workflow)
	assertGitHubCompositeReadableYAML(t, "workflow", workflow, plan)
	if err := yamlv3.Unmarshal(workflow, &document); err != nil {
		t.Fatal(err)
	}
	if err := anchorGitHubPreflight(plan, githubYAMLField(document.Content[0], "jobs"), actions); err != nil {
		t.Fatal(err)
	}
	repeated, err := encodeGitHubYAML(&document)
	if err != nil || !bytes.Equal(repeated, workflow) {
		t.Fatalf("repeated alias lowering changed workflow bytes: %v", err)
	}
}

func TestGitHubStepSequencesRequireApprovedIdenticalSteps(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	for _, test := range []struct {
		name   string
		change func(*yamlv3.Node)
		both   bool
	}{
		{name: "different checkout pin", change: func(s *yamlv3.Node) { githubYAMLSet(s.Content[1], "uses", githubYAMLString("actions/checkout@other")) }},
		{name: "checkout credential", both: true, change: func(s *yamlv3.Node) {
			githubYAMLSet(githubYAMLField(s.Content[1], "with"), "token", githubYAMLString("${{ secrets.SOURCE }}"))
		}},
		{name: "source credential", both: true, change: func(s *yamlv3.Node) {
			githubYAMLSet(githubYAMLField(s.Content[3], "with"), "nvkr-source-token", githubYAMLString("${{ secrets.SOURCE }}"))
		}},
		{name: "source operation", both: true, change: func(s *yamlv3.Node) {
			githubYAMLSet(s.Content[3], "uses", githubYAMLString("./"+githubSharedDirectory+"execute-source"))
		}},
		{name: "different helper", change: func(s *yamlv3.Node) {
			githubYAMLSet(githubYAMLField(s.Content[2], "with"), "runner-artifact", githubYAMLString("other-helper"))
		}},
		{name: "injected job selector", change: func(s *yamlv3.Node) {
			githubYAMLSet(githubYAMLField(s.Content[2], "with"), "action", githubYAMLString("another-job"))
		}},
		{name: "redundant job selector", both: true, change: func(s *yamlv3.Node) {
			githubYAMLSet(githubYAMLField(s.Content[2], "with"), "action", githubYAMLString("${{ github.job }}"))
		}},
		{name: "different operation selector", change: func(s *yamlv3.Node) {
			githubYAMLSet(githubYAMLField(s.Content[3], "with"), "parameters", githubYAMLString(githubParametersExpression("part-2")))
		}},
		{name: "operation label omits part", both: true, change: func(s *yamlv3.Node) {
			githubYAMLSet(s.Content[3], "name", githubYAMLString("execute node"))
		}},
		{name: "same name different bytes", change: func(s *yamlv3.Node) { githubYAMLField(s.Content[0], "run").Value += "\n" }},
		{name: "step id", both: true, change: func(s *yamlv3.Node) { githubYAMLSet(s.Content[3], "id", githubYAMLString("execute")) }},
		{name: "condition", both: true, change: func(s *yamlv3.Node) { githubYAMLSet(s.Content[3], "if", githubYAMLString("${{ always() }}")) }},
		{name: "timeout", both: true, change: func(s *yamlv3.Node) { githubYAMLSet(s.Content[3], "timeout-minutes", githubYAMLString("5")) }},
		{name: "continue on error", both: true, change: func(s *yamlv3.Node) { githubYAMLSet(s.Content[3], "continue-on-error", githubYAMLString("true")) }},
		{name: "authored setup", both: true, change: func(s *yamlv3.Node) {
			s.Content = append(s.Content, githubYAMLMap("name", githubYAMLString("Configure build environment"), "run", githubYAMLString("true")))
		}},
		{name: "step order", both: true, change: func(s *yamlv3.Node) { s.Content[0], s.Content[1] = s.Content[1], s.Content[0] }},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := &Plan{}
			plan.Environment.GitHubRunners = map[string]string{"linux/amd64": "ubuntu-24.04"}
			first, second := githubStepSequenceFixture(t, plan, actions, "ubuntu-24.04"), githubStepSequenceFixture(t, plan, actions, "ubuntu-24.04")
			if test.both {
				test.change(githubYAMLField(first, "steps"))
			}
			test.change(githubYAMLField(second, "steps"))
			jobs := githubYAMLMap("first", first, "second", second)
			if err := anchorGitHubPreflight(plan, jobs, actions); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"first", "second"} {
				steps := githubYAMLField(githubYAMLField(jobs, name), "steps")
				if steps.Kind == yamlv3.AliasNode || steps.Anchor != "" {
					t.Fatal("unapproved or nonidentical complete sequence was shared")
				}
			}
		})
	}
}

func TestGitHubStepSequencesSupportNativeRoutesAndLayouts(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	for _, layout := range []struct {
		name        string
		development bool
		bundle      string
	}{
		{name: "default"},
		{name: "development", development: true},
		{name: "named", bundle: "nvke-nightly-arm64"},
	} {
		for _, runner := range []string{"ubuntu-24.04", "ubuntu-24.04-arm"} {
			t.Run(fmt.Sprintf("%s/%s", runner, layout.name), func(t *testing.T) {
				plan := &Plan{}
				plan.Environment.GitHubDevelopment = layout.development
				plan.Environment.GitHubBundle = layout.bundle
				plan.Environment.GitHubRunners = map[string]string{"linux/amd64": "ubuntu-24.04", "linux/arm64": "ubuntu-24.04-arm"}
				jobs := githubYAMLMap("first", githubStepSequenceFixture(t, plan, actions, runner), "second", githubStepSequenceFixture(t, plan, actions, runner))
				before, err := encodeGitHubYAML(jobs)
				if err != nil {
					t.Fatal(err)
				}
				if err := anchorGitHubPreflight(plan, jobs, actions); err != nil {
					t.Fatal(err)
				}
				after, err := encodeGitHubYAML(jobs)
				if err != nil {
					t.Fatal(err)
				}
				steps := githubYAMLField(githubYAMLField(jobs, "second"), "steps")
				if steps.Kind != yamlv3.AliasNode || steps.Value != "steps-"+shortStableName(runner)+"-node" {
					t.Fatal("native route did not share its complete sequence")
				}
				var want, got any
				if err := yamlv3.Unmarshal(before, &want); err != nil {
					t.Fatal(err)
				}
				if err := yamlv3.Unmarshal(after, &got); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(want, got) {
					t.Fatal("sequence aliases changed the native route or selected workflow layout")
				}
				assertGitHubSequenceAliasGraph(t, after)
				assertGitHubCompositeReadableYAML(t, "workflow", after, plan)
			})
		}
	}
}

func TestGitHubStepSequencesRejectBrokenAliases(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		t.Run(fmt.Sprintf("cycle=%v", cycle), func(t *testing.T) {
			alias := &yamlv3.Node{Kind: yamlv3.AliasNode, Value: "broken"}
			if cycle {
				alias.Alias = alias
			}
			jobs := githubYAMLMap("job", githubYAMLMap("steps", alias))
			err := anchorGitHubPreflight(&Plan{}, jobs, githubActionRefs{})
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
		})
	}
}

func assertGitHubSequenceAliasGraph(t *testing.T, raw []byte) {
	t.Helper()
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	anchors, uses := map[string]*yamlv3.Node{}, map[string]int{}
	var visit func(*yamlv3.Node)
	visit = func(node *yamlv3.Node) {
		if node.Anchor != "" {
			if anchors[node.Anchor] != nil {
				t.Fatalf("duplicate anchor %q", node.Anchor)
			}
			anchors[node.Anchor] = node
		}
		if node.Kind == yamlv3.AliasNode {
			if node.Alias == nil || anchors[node.Value] != node.Alias {
				t.Fatalf("forward or dangling alias %q", node.Value)
			}
			uses[node.Value]++
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	visit(&document)
	for name := range anchors {
		if uses[name] == 0 {
			t.Fatalf("unused anchor %q", name)
		}
		if strings.HasPrefix(name, "steps-") && !strings.Contains(name, "node") {
			t.Fatalf("non-semantic sequence anchor %q", name)
		}
	}
}
