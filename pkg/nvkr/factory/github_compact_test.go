// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
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
	expanded, err := expandGitHubActions(&Plan{}, workflow, files)
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
	for _, value := range []string{"yes", "No", "ON", "off", "y", "N", "true", "null", "01", "1e2", "2026-10-13", "# literal", "colon: value", "first\nsecond", "${{ secrets.TEST_SOURCE_READ_TOKEN }}"} {
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
        env: {NVKR_SOURCE_TOKEN: '${{ secrets.TEST_SOURCE_READ_TOKEN }}', LITERAL: %s}
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
			expanded, err := expandGitHubActions(&Plan{}, workflow, files)
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
		raw, err = expandGitHubActions(&Plan{}, workflow, files)
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

func TestGitHubWorkflowExpressionLengthAdmission(t *testing.T) {
	expression := func(body string) string { return "key: '${{" + body + "}}'\n" }
	for _, tc := range []struct {
		name  string
		raw   string
		valid bool
	}{
		{"exact", expression(strings.Repeat("x", maxGitHubExpressionBytes)), true},
		{"over", expression(strings.Repeat("x", maxGitHubExpressionBytes+1)), false},
		{"second expression over", expression("x") + "other: '${{" + strings.Repeat("x", maxGitHubExpressionBytes+1) + "}}'\n", false},
		{"quoted braces stay inside", expression("format(''{{{0}}}'', a)" + strings.Repeat("x", maxGitHubExpressionBytes)), false},
		{"text outside expressions", "key: '" + strings.Repeat("x", maxGitHubExpressionBytes+1) + " ${{ a }}'\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGitHubWorkflowSize([]byte(tc.raw))
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

func TestGitHubWorkflowYAMLNodeAdmission(t *testing.T) {
	// Stream, document and sequence start/end events account for six nodes.
	for _, tc := range []struct {
		name  string
		items int
		valid bool
	}{
		{"under", maxGitHubWorkflowYAMLNodes - 7, true},
		{"exact", maxGitHubWorkflowYAMLNodes - 6, true},
		{"over", maxGitHubWorkflowYAMLNodes - 5, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(strings.Repeat("- x\n", tc.items))
			err := validateGitHubWorkflowSize(raw)
			if !tc.valid {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			nodes, err := countGitHubWorkflowYAMLNodes(raw)
			if err != nil || nodes != tc.items+6 {
				t.Fatalf("nodes=%d error=%v, want %d parsing events", nodes, err, tc.items+6)
			}
		})
	}
}

func TestGitHubWorkflowYAMLNodeAccounting(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want int
	}{
		{"scalar", "value\n", 5},
		{"mapping keys and values", "key: [one, two]\n", 11},
		{"replayed sequence", "- &pair [one, two]\n- *pair\n", 14},
		{"nested aliases", "- &value x\n- &pair [*value, *value]\n- *pair\n", 15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes, err := countGitHubWorkflowYAMLNodes([]byte(tc.raw))
			if err != nil || nodes != tc.want {
				t.Fatalf("nodes=%d error=%v, want %d parsing events", nodes, err, tc.want)
			}
		})
	}
}

func TestGitHubWorkflowYAMLAdmissionRejectsMalformedGraphs(t *testing.T) {
	assertFactoryErrorCode(t, validateGitHubWorkflowSize(nil), e.CodeInvalidCatalog)
	assertFactoryErrorCode(t, validateGitHubWorkflowSize([]byte("# no workflow\n")), e.CodeInvalidCatalog)
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"malformed", "key: [\n"},
		{"missing alias", "key: *absent\n"},
		{"recursive alias", "key: &cycle [*cycle]\n"},
		{"multiple documents", "first\n---\nsecond\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertFactoryErrorCode(t, validateGitHubWorkflowSize([]byte(tc.raw)), e.CodePipelineDrift)
		})
	}
	deep := strings.Repeat("[", maxGitHubWorkflowYAMLDepth) + "x" + strings.Repeat("]", maxGitHubWorkflowYAMLDepth)
	assertFactoryErrorCode(t, validateGitHubWorkflowSize([]byte(deep)), e.CodeEngineCapability)
}

func TestGitHubWorkflowYAMLAliasExpansionStopsAtBudget(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("a0: &a0 [x]\n")
	for i := 1; i <= 20; i++ {
		fmt.Fprintf(&raw, "a%d: &a%d [*a%d, *a%d]\n", i, i, i-1, i-1)
	}
	nodes, err := countGitHubWorkflowYAMLNodes([]byte(raw.String()))
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	if nodes > maxGitHubWorkflowYAMLNodes {
		t.Fatalf("alias traversal exceeded its budget: %d", nodes)
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

func TestOvercomplexWorkflowCannotWriteGeneratedBundle(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(strings.Repeat("- x\n", maxGitHubWorkflowYAMLNodes))
	bundle.Files[GitHubWorkflowPath] = raw
	bundle.WorkflowDigest = pipeline.FileDigest(raw)
	out := filepath.Join(t.TempDir(), "must-not-exist")
	assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), out, bundle), e.CodeEngineCapability)
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("overcomplex workflow wrote output: %v", err)
	}
}

func TestMaintainedGitHubBundles(t *testing.T) {
	for _, selected := range []struct {
		planPath             string
		privateDiskProducers int // -1 explicitly exempts the separate development credential policy.
	}{
		{FactoryPlanPath, 4},
		{GitHubDevelopmentPlanPath, -1},
		{".github/nvkr-run-nvke-nightly-arm64/plan.json", 1},
		{".github/nvkr-run-nvke-nightly-upstream-ovn-arm64/plan.json", 1},
	} {
		planPath := selected.planPath
		t.Run(planPath, func(t *testing.T) {
			plan, err := LoadPlanContext(t.Context(), "../../../"+planPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Environment.SourceAccess) != 0 {
				t.Fatal("maintained public sources must not select credentials")
			}
			bundle, err := RenderGitHub(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			// One render per maintained bundle covers the real product's size,
			// placement and credential contracts. Generic mutation tests use
			// small fixtures; they do not need hundreds of unrelated jobs.
			if planPath == FactoryPlanPath {
				if plan.DefinitionPath != "inputs/definitions/nvke/definition.yaml" || plan.EnvironmentPath != "inputs/environments/github-release.yaml" || plan.Environment.GitHubBundle != "" {
					t.Fatal("committed default workflow must select the maintained combined M2 inputs")
				}
				if err := CheckGeneratedBundle(t.Context(), "../../..", bundle); err != nil {
					t.Fatalf("default namespace changed committed Plan/workflow/manifest bytes: %v", err)
				}
				raw := bundle.Files[bundle.WorkflowPath]
				if len(raw) > maxGitHubWorkflowBytes*95/100 {
					t.Fatalf("maintained workflow has less than 5%% headroom (%d of %d bytes); compact semantic calls before expanding membership", len(raw), maxGitHubWorkflowBytes)
				}
				nodes, err := countGitHubWorkflowYAMLNodes(raw)
				if err != nil {
					t.Fatal(err)
				}
				if nodes > maxGitHubWorkflowYAMLNodes*95/100 {
					t.Fatalf("maintained workflow has less than 5%% YAML parsing headroom (%d of %d events); compact semantic calls before expanding membership", nodes, maxGitHubWorkflowYAMLNodes)
				}
			}
			if selected.privateDiskProducers >= 0 {
				flatReference := githubFlatReference(t, plan, bundle)
				assertGitHubPrivateImageCredentialBoundary(t, plan, flatReference, selected.privateDiskProducers)
			}
			workers := map[string][]byte{}
			for name, raw := range bundle.Files {
				if strings.HasPrefix(name, ".github/workflows/") && name != bundle.WorkflowPath {
					if !plan.Environment.GitHubExecutionPartitions || !githubPartitionWorkflowSuffix.MatchString(name) {
						t.Fatalf("producer bundle emitted an unexpected additional workflow %s", name)
					}
					workers[name] = raw
				}
			}
			var document yamlv3.Node
			if err := yamlv3.Unmarshal(bundle.Files[bundle.WorkflowPath], &document); err != nil {
				t.Fatal(err)
			}
			triggers := githubYAMLField(document.Content[0], "on")
			if githubYAMLField(triggers, "workflow_dispatch") == nil || githubYAMLLength(triggers) != 2 {
				t.Fatal("producer entry must expose only its manual dispatch trigger")
			}
			if plan.Environment.GitHubExecutionPartitions {
				if len(workers) == 0 {
					t.Fatal("partitioned producer bundle omitted its workers")
				}
				assertGitHubPartitionMembership(t, plan, bundle.Files[bundle.WorkflowPath], workers)
				emitted := githubEmittedWorkflows(t, plan, bundle)
				if planPath == FactoryPlanPath {
					// The default suite checks the combined bundle's emitted pin and
					// source credential scope; the extended suite covers the others.
					assertMaintainedPinSetCredentialScope(t, plan, emitted, false)
					// Exercise private-source guards with an explicit test credential.
					credentialPlan := *plan
					addTestSourceCredential(t, &credentialPlan)
					credentialBundle, err := RenderGitHub(t.Context(), &credentialPlan)
					if err != nil {
						t.Fatal(err)
					}
					assertGitHubEmittedCredentialRefusals(t, &credentialPlan, credentialBundle, githubEmittedWorkflows(t, &credentialPlan, credentialBundle))
				}
				if selected.privateDiskProducers >= 0 {
					// The emitted workers, not only their flat reference, keep each
					// image credential in its read-only preparation step.
					emittedJobs := map[string]githubJob{}
					for _, name := range slices.Sorted(maps.Keys(emitted)) {
						maps.Copy(emittedJobs, emitted[name].Jobs)
					}
					assertGitHubPrivateImageJobs(t, emittedJobs, selected.privateDiskProducers)
				}
				t.Logf("one entry: %d bytes, %d workers with exact original membership", len(bundle.Files[bundle.WorkflowPath]), len(workers))
				return
			}
			workflow := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath])
			projection, err := githubNeedsForPlan(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			if len(workflow.Jobs) != len(projection.Reduced) || len(projection.Collectors) == 0 {
				t.Fatal("maintained entry lost execution jobs or result collection")
			}
			for id, job := range workflow.Jobs {
				if job.RunsOn == "" || len(job.Steps) == 0 {
					t.Fatalf("job %s delegates execution outside the entry workflow", id)
				}
			}
			t.Logf("one entry: %d bytes, %d execution/result jobs", len(bundle.Files[bundle.WorkflowPath]), len(workflow.Jobs))
		})
	}
}

// githubEmittedWorkflows decodes each workflow file a bundle emits, the entry
// and any partition workers, with its local actions expanded. An entry's
// worker-calling jobs have no steps; their bodies are the worker documents.
func githubEmittedWorkflows(t *testing.T, plan *Plan, bundle *GeneratedBundle) map[string]githubWorkflowDocument {
	t.Helper()
	documents := map[string]githubWorkflowDocument{}
	for _, name := range slices.Sorted(maps.Keys(bundle.Files)) {
		if name != bundle.WorkflowPath && !githubPartitionWorkflowSuffix.MatchString(name) {
			continue
		}
		raw := bundle.Files[name]
		if name == bundle.WorkflowPath && plan.Environment.GitHubExecutionPartitions {
			var document yamlv3.Node
			if err := yamlv3.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			jobs := githubYAMLField(document.Content[0], "jobs")
			for index := len(jobs.Content) - 2; index >= 0; index -= 2 {
				if githubYAMLField(jobs.Content[index+1], "uses") != nil {
					assertGitHubPartitionCallerSecrets(t, bundle, jobs.Content[index].Value, jobs.Content[index+1])
					jobs.Content = slices.Delete(jobs.Content, index, index+2)
				}
			}
			var err error
			if raw, err = encodeGitHubYAML(&document); err != nil {
				t.Fatal(err)
			}
		}
		expanded, err := expandGitHubActions(plan, raw, bundle.Files)
		if err != nil {
			t.Fatal(err)
		}
		documents[name] = decodeGitHubWorkflow(t, expanded)
	}
	return documents
}

// githubFlatReference returns the single-workflow rendering whose job bodies a
// partitioned bundle's workers project exactly; a flat bundle is its own.
func githubFlatReference(t *testing.T, plan *Plan, bundle *GeneratedBundle) *GeneratedBundle {
	t.Helper()
	if !plan.Environment.GitHubExecutionPartitions {
		return bundle
	}
	flat, err := renderGitHubFlat(t.Context(), plan, false)
	if err != nil {
		t.Fatal(err)
	}
	return flat
}

// assertGitHubPartitionCallerSecrets requires an entry caller to forward exactly
// its worker's declared secrets, each from the same repository secret.
func assertGitHubPartitionCallerSecrets(t testing.TB, bundle *GeneratedBundle, id string, call *yamlv3.Node) {
	t.Helper()
	worker := strings.TrimPrefix(githubYAMLValue(call, "uses"), "./")
	raw, ok := bundle.Files[worker]
	if !ok || !githubPartitionWorkflowSuffix.MatchString(worker) {
		t.Fatalf("entry job %s calls unexpected workflow %q", id, worker)
	}
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	declared := githubYAMLField(githubYAMLField(githubYAMLField(document.Content[0], "on"), "workflow_call"), "secrets")
	// Only the secrets mapping may reference a credential; with values reach
	// the worker's inputs and if expressions are logged.
	for index := 0; index < len(call.Content); index += 2 {
		if call.Content[index].Value == "secrets" {
			continue
		}
		text, err := yamlv3.Marshal(call.Content[index+1])
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(text), "secrets.TEST_SOURCE_READ_TOKEN") || strings.Contains(string(text), "NVKR_PIN_SOURCE_TOKEN_") {
			t.Fatalf("entry job %s carries a pin or source credential outside secrets", id)
		}
	}
	forwarded := githubYAMLField(call, "secrets")
	if (declared == nil) != (forwarded == nil) || forwarded != nil && (forwarded.Kind != yamlv3.MappingNode || len(forwarded.Content) != len(declared.Content)) {
		t.Fatalf("entry job %s forwards secrets beyond %s's declarations", id, worker)
	}
	for index := 0; forwarded != nil && index < len(forwarded.Content); index += 2 {
		name := forwarded.Content[index].Value
		if githubYAMLField(declared, name) == nil || forwarded.Content[index+1].Value != "${{ secrets."+name+" }}" {
			t.Fatalf("entry job %s forwards undeclared or renamed secret %s", id, name)
		}
	}
}

// assertGitHubEmittedCredentialRefusals proves that the caller and emitted-worker
// credential guards refuse mutated copies of the maintained bundle.
func assertGitHubEmittedCredentialRefusals(t *testing.T, plan *Plan, bundle *GeneratedBundle, emitted map[string]githubWorkflowDocument) {
	t.Helper()
	const sourceSecret = "${{ secrets.TEST_SOURCE_READ_TOKEN }}"
	scalar := func(value string) *yamlv3.Node { return &yamlv3.Node{Kind: yamlv3.ScalarNode, Value: value} }
	for _, tc := range []struct {
		name, want string
		edit       func(call *yamlv3.Node)
	}{
		{"caller with", "outside secrets", func(call *yamlv3.Node) {
			call.Content = append(call.Content, scalar("with"),
				&yamlv3.Node{Kind: yamlv3.MappingNode, Content: []*yamlv3.Node{scalar("token"), scalar(sourceSecret)}})
		}},
		{"caller if", "outside secrets", func(call *yamlv3.Node) {
			call.Content = append(call.Content, scalar("if"), scalar("env.NVKR_PIN_SOURCE_TOKEN_0 != ''"))
		}},
		{"renamed secret", "undeclared or renamed secret", func(call *yamlv3.Node) {
			githubYAMLField(githubYAMLField(call, "secrets"), "TEST_SOURCE_READ_TOKEN").Value = "${{ secrets.OTHER }}"
		}},
		{"extra secret", "beyond", func(call *yamlv3.Node) {
			secrets := githubYAMLField(call, "secrets")
			secrets.Content = append(secrets.Content, scalar("OTHER"), scalar("${{ secrets.OTHER }}"))
		}},
	} {
		var document yamlv3.Node
		if err := yamlv3.Unmarshal(bundle.Files[bundle.WorkflowPath], &document); err != nil {
			t.Fatal(err)
		}
		id, call := "", (*yamlv3.Node)(nil)
		jobs := githubYAMLField(document.Content[0], "jobs")
		for index := 0; index < len(jobs.Content) && call == nil; index += 2 {
			if githubYAMLField(githubYAMLField(jobs.Content[index+1], "secrets"), "TEST_SOURCE_READ_TOKEN") != nil {
				id, call = jobs.Content[index].Value, jobs.Content[index+1]
			}
		}
		if call == nil {
			t.Fatal("no entry job forwards the source secret")
		}
		tc.edit(call)
		if got := githubAssertionRefusal(t, func(t testing.TB) { assertGitHubPartitionCallerSecrets(t, bundle, id, call) }); !strings.Contains(got, tc.want) {
			t.Fatalf("%s: caller guard reported %q, want %q", tc.name, got, tc.want)
		}
	}
	name, id := "", ""
	for _, file := range slices.Sorted(maps.Keys(emitted)) {
		for _, job := range slices.Sorted(maps.Keys(emitted[file].Jobs)) {
			if file != bundle.WorkflowPath && id == "" && len(emitted[file].Jobs[job].Steps) != 0 {
				name, id = file, job
			}
		}
	}
	if id == "" {
		t.Fatal("no emitted worker job has steps")
	}
	for _, tc := range []struct {
		name, want string
		edit       func(job *githubJob)
	}{
		{"worker condition", "job condition", func(job *githubJob) { job.If = "env.NVKR_PIN_SOURCE_TOKEN_0 != ''" }},
		{"worker output", "job condition", func(job *githubJob) { job.Outputs = map[string]string{"token": sourceSecret} }},
		{"worker step input", "step inputs", func(job *githubJob) { job.Steps[0].With = map[string]string{"token": sourceSecret} }},
	} {
		job := emitted[name].Jobs[id]
		job.Steps = slices.Clone(job.Steps)
		tc.edit(&job)
		documents := map[string]githubWorkflowDocument{name: {Jobs: map[string]githubJob{id: job}}}
		if got := githubAssertionRefusal(t, func(t testing.TB) { assertMaintainedPinSetCredentialScope(t, plan, documents, true) }); !strings.Contains(got, tc.want) {
			t.Fatalf("%s: worker guard reported %q, want %q", tc.name, got, tc.want)
		}
	}
}

// githubFatalRecorder records a helper's fatal failure instead of failing the test.
type githubFatalRecorder struct {
	testing.TB
	message string
}

func (r *githubFatalRecorder) Fatal(args ...any) {
	r.message = fmt.Sprint(args...)
	panic(r)
}

func (r *githubFatalRecorder) Fatalf(format string, args ...any) {
	r.message = fmt.Sprintf(format, args...)
	panic(r)
}

// githubAssertionRefusal returns the fatal message check reports, or "" if it passes.
func githubAssertionRefusal(t *testing.T, check func(testing.TB)) (message string) {
	t.Helper()
	recorder := &githubFatalRecorder{TB: t}
	defer func() {
		switch recovered := recover(); recovered {
		case nil:
		case recorder:
			message = recorder.message
		default:
			panic(recovered)
		}
	}()
	check(recorder)
	return ""
}
