// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

func TestWorkflowLoweringPreservesExpandedGraph(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	raw := []byte("'on': {workflow_dispatch: {}}\njobs:\n  first:\n    steps:\n      - uses: " + actions.Checkout + "\n        with: {persist-credentials: 'false'}\n  second:\n    steps:\n      - uses: " + actions.Checkout + "\n        with: {persist-credentials: 'false'}\n")
	workflow, files, err := lowerGitHubActions(&Plan{}, raw, actions)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(workflow, []byte("*checkout-workflow-source")) {
		t.Fatal("repeated checkout does not use its descriptive preflight alias")
	}
	expected, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		t.Fatal(err)
	}
	expanded, err := expandGitHubActions(t.Context(), &Plan{}, workflow, files)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := yaml.YAMLToJSONStrict(expanded)
	if err != nil || !bytes.Equal(expected, actual) {
		t.Fatalf("graph changed: %v", err)
	}
	var tree yamlv3.Node
	if err := yamlv3.Unmarshal(workflow, &tree); err != nil {
		t.Fatal(err)
	}
	definitions, uses := map[string]bool{}, map[string]bool{}
	var visit func(*yamlv3.Node)
	visit = func(node *yamlv3.Node) {
		if node.Anchor != "" {
			definitions[node.Anchor] = true
		}
		if node.Kind == yamlv3.AliasNode {
			uses[node.Value] = true
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	visit(&tree)
	if len(uses) == 0 {
		t.Fatal("repeated steps not aliased")
	}
	for name := range definitions {
		if !uses[name] {
			t.Fatalf("unused anchor %s", name)
		}
	}
	second, _, err := lowerGitHubActions(&Plan{}, raw, actions)
	if err != nil || !bytes.Equal(workflow, second) {
		t.Fatalf("nondeterministic action lowering: %v", err)
	}
}

func TestWorkflowLoweringPreservesScalarTypesAndAuthority(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	for _, value := range []string{"yes", "No", "ON", "off", "y", "N", "true", "null", "01", "1e2", "2026-10-13", "# literal", "colon: value", "first\nsecond", "${{ secrets.NKE_SOURCE_TOKEN }}"} {
		t.Run(value, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`'on': {workflow_dispatch: {}}
permissions: {contents: read}
jobs:
  fetch:
    name: 'Fetch exact source'
    runs-on: ubuntu-24.04
    timeout-minutes: 60
    permissions: {contents: read, packages: read}
    if: ${{ github.ref == 'refs/heads/main' }}
    steps:
      - uses: %s
      - name: 'Fetch exact source'
        env: {NVKR_SOURCE_TOKEN: '${{ secrets.NKE_SOURCE_TOKEN }}', LITERAL: %s}
        run: |
          set -eu
          ./nvkr-exec factory github run-node --plan .github/nvkr-run/plan.json --node fetch/source
  sign:
    name: 'Sign exact publication'
    needs: [fetch]
    runs-on: ubuntu-24.04
    timeout-minutes: 60
    permissions: {contents: read, packages: write, id-token: write}
    if: ${{ github.ref == 'refs/heads/main' }}
    steps:
      - uses: %s
      - name: 'Sign exact publication'
        run: ./nvkr-exec factory github run-node --plan .github/nvkr-run/plan.json --node sign/output
`, actions.Checkout, yamlSingle(value), actions.Checkout))
			workflow, files, err := lowerGitHubActions(&Plan{}, raw, actions)
			if err != nil {
				t.Fatal(err)
			}
			assertGitHubActionExtraction(t, workflow, files)
			before, err := yaml.YAMLToJSONStrict(raw)
			if err != nil {
				t.Fatal(err)
			}
			expanded, err := expandGitHubActions(t.Context(), &Plan{}, workflow, files)
			if err != nil {
				t.Fatal(err)
			}
			after, err := yaml.YAMLToJSONStrict(expanded)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("action lowering changed credentials, dependency, branch policy, permissions or script bytes: %v", err)
			}
		})
	}
}

func TestGitHubYAMLEncodingPreservesConstructedStringTypes(t *testing.T) {
	for _, value := range []string{"y", "Y", "yes", "Yes", "YES", "n", "N", "no", "No", "NO", "on", "On", "ON", "off", "Off", "OFF", "true", "null", "01", "1e2"} {
		t.Run(value, func(t *testing.T) {
			raw, err := encodeGitHubYAML(githubYAMLMap("literal", githubYAMLString(value)))
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := yaml.UnmarshalStrict(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded["literal"] != value {
				t.Fatalf("constructed string %q changed YAML scalar type: %#v", value, decoded["literal"])
			}
		})
	}
}

func TestWorkflowLoweringPreservesExistingAliases(t *testing.T) {
	actions := testActionRefs(t, githubTestPlan(t))
	raw := []byte(strings.ReplaceAll(`'on': {workflow_dispatch: {}}
env: {EXACT_SOURCE: &n1 '${{ secrets.EXACT_SOURCE }}'}
jobs:
  first:
    name: 'Repeated source fetch'
    runs-on: ubuntu-24.04
    steps:
      - uses: CHECKOUT
      - env: {EXACT_SOURCE: *n1}
        run: 'fetch exact source bytes'
  second:
    name: 'Repeated source fetch'
    runs-on: ubuntu-24.04
    steps:
      - uses: CHECKOUT
      - env: {EXACT_SOURCE: *n1}
        run: 'fetch exact source bytes'
`, "CHECKOUT", actions.Checkout))
	expected, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		workflow, files, err := lowerGitHubActions(&Plan{}, raw, actions)
		if err != nil {
			t.Fatal(err)
		}
		assertGitHubActionExtraction(t, workflow, files)
		raw, err = expandGitHubActions(t.Context(), &Plan{}, workflow, files)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := yaml.YAMLToJSONStrict(raw)
		if err != nil || !bytes.Equal(expected, actual) {
			t.Fatalf("repeated action lowering changed an existing alias binding: %v", err)
		}
	}
}

func assertGitHubActionExtraction(t *testing.T, workflow []byte, files map[string][]byte) {
	t.Helper()
	actions, calls := 0, 0
	for name := range files {
		if strings.HasPrefix(name, githubActionsDirectory+"jobs/") {
			actions++
		}
	}
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(workflow, &document); err != nil {
		t.Fatal(err)
	}
	jobs := githubYAMLField(document.Content[0], "jobs")
	for index := 1; index < len(jobs.Content); index += 2 {
		for _, step := range githubYAMLField(jobs.Content[index], "steps").Content {
			if strings.HasPrefix(githubYAMLValue(step, "uses"), "./"+githubActionsDirectory+"jobs/") {
				calls++
			}
		}
	}
	if actions == 0 || calls == 0 {
		t.Fatalf("fixture bypassed action extraction: %d actions, %d local calls", actions, calls)
	}
}

func TestWorkflowLoweringRejectsRecursiveAliases(t *testing.T) {
	_, _, err := lowerGitHubActions(&Plan{}, []byte("jobs: &recursive {first: *recursive}\n"), testActionRefs(t, githubTestPlan(t)))
	assertFactoryErrorCode(t, err, e.CodePipelineDrift)
}

func TestGitHubWorkflowSizeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   []byte
		valid bool
	}{
		{"under", bytes.Repeat([]byte("x"), maxGitHubWorkflowBytes-1), true},
		{"exact", bytes.Repeat([]byte("x"), maxGitHubWorkflowBytes), true},
		{"over", bytes.Repeat([]byte("x"), maxGitHubWorkflowBytes+1), false},
		{"UTF8 bytes", []byte(strings.Repeat("é", maxGitHubWorkflowBytes/2) + "x"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGitHubWorkflowSize(tc.raw)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			}
		})
	}
}

func TestOversizedWorkflowCannotWriteGeneratedBundle(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	raw := bytes.Repeat([]byte("x"), maxGitHubWorkflowBytes+1)
	bundle.Files[GitHubWorkflowPath] = raw
	bundle.WorkflowDigest = pipeline.FileDigest(raw)
	out := filepath.Join(t.TempDir(), "must-not-exist")
	assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), out, bundle), e.CodeEngineCapability)
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("oversized workflow wrote output: %v", err)
	}
}

func TestMaintainedWorkflowFitsGitHubFileLimit(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../"+FactoryPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	if plan.DefinitionPath != "inputs/definitions/nvke/definition.yaml" || plan.EnvironmentPath != "inputs/environments/github-release.yaml" {
		t.Fatal("committed workflow must select the maintained combined M2 inputs")
	}
	raw, err := os.ReadFile("../../../" + GitHubWorkflowPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGitHubWorkflowSize(raw); err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, bundle.Files[GitHubWorkflowPath]) {
		t.Fatal("committed workflow differs from its complete regenerated Plan")
	}
	t.Logf("maintained workflow: %d bytes, %d explicit jobs", len(raw), len(githubSerializedJobOrder(t, raw)))
}

// Render the maintained complete Plan as well as checking its committed bytes:
// a newly required field on hundreds of steps can overflow provider admission
// even when the previous generated workflow still fits.
func TestMaintainedM2RenderingFitsGitHubFileLimit(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../"+FactoryPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	raw := bundle.Files[bundle.WorkflowPath]
	if err := validateGitHubWorkflowSize(raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) > maxGitHubWorkflowBytes*95/100 {
		t.Fatalf("maintained workflow has less than 5%% headroom (%d of %d bytes); compact semantic calls before expanding membership", len(raw), maxGitHubWorkflowBytes)
	}
	t.Logf("maintained M2 rendering: %d bytes (%d bytes below GitHub limit)", len(raw), maxGitHubWorkflowBytes-len(raw))
}
