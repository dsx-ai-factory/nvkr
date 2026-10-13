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

func TestWorkflowCompactionPreservesExpandedGraph(t *testing.T) {
	raw := []byte("'on': {workflow_dispatch: {}}\njobs:\n  first:\n    steps:\n      - uses: actions/checkout@" + testCosignArchiveDigest[7:] + "\n        with: {persist-credentials: 'false'}\n  second:\n    steps:\n      - uses: actions/checkout@" + testCosignArchiveDigest[7:] + "\n        with: {persist-credentials: 'false'}\n")
	compact, err := compactGitHubWorkflow(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(compact, []byte("*n")) || len(compact) >= len(raw) {
		t.Fatal("repeated steps not compacted")
	}
	expected, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := yaml.YAMLToJSONStrict(compact)
	if err != nil || !bytes.Equal(expected, actual) {
		t.Fatalf("graph changed: %v", err)
	}
	var tree yamlv3.Node
	if err := yamlv3.Unmarshal(compact, &tree); err != nil {
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
	for name := range definitions {
		if !uses[name] {
			t.Fatalf("unused anchor %s", name)
		}
	}
	second, err := compactGitHubWorkflow(raw)
	if err != nil || !bytes.Equal(compact, second) {
		t.Fatalf("nondeterministic compaction: %v", err)
	}
}

func TestWorkflowCompactionPreservesScalarTypesAndAuthority(t *testing.T) {
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
      - name: 'Sign exact publication'
        run: ./nvkr-exec factory github run-node --plan .github/nvkr-run/plan.json --node sign/output
`, yamlSingle(value)))
			compact, err := compactGitHubWorkflow(raw)
			if err != nil {
				t.Fatal(err)
			}
			before, err := yaml.YAMLToJSONStrict(raw)
			if err != nil {
				t.Fatal(err)
			}
			after, err := yaml.YAMLToJSONStrict(compact)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("compaction changed credentials, dependency, branch policy, permissions or script bytes: %v", err)
			}
		})
	}
}

func TestWorkflowCompactionPreservesExistingAliases(t *testing.T) {
	raw := []byte(`'on': {workflow_dispatch: {}}
env: {EXACT_SOURCE: &n1 '${{ secrets.EXACT_SOURCE }}'}
jobs:
  first:
    name: 'Repeated source fetch'
    runs-on: ubuntu-24.04
    steps:
      - env: {EXACT_SOURCE: *n1}
        run: 'fetch exact source bytes'
  second:
    name: 'Repeated source fetch'
    runs-on: ubuntu-24.04
    steps:
      - env: {EXACT_SOURCE: *n1}
        run: 'fetch exact source bytes'
`)
	expected, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		raw, err = compactGitHubWorkflow(raw)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := yaml.YAMLToJSONStrict(raw)
		if err != nil || !bytes.Equal(expected, actual) {
			t.Fatalf("recompaction changed an existing alias binding: %v", err)
		}
	}
}

func TestWorkflowCompactionRejectsRecursiveAliases(t *testing.T) {
	_, err := compactGitHubWorkflow([]byte("jobs: &recursive {first: *recursive}\n"))
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
	if plan.DefinitionPath != "definitions/nvke-m2/definition.yaml" || plan.EnvironmentPath != "definitions/nvke-m2/environment-github.yaml" {
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
	document := decodeGitHubWorkflow(t, raw)
	t.Logf("maintained workflow: %d bytes, %d explicit jobs", len(raw), len(document.Jobs))
}
