// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"path"
	"strings"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	yamlv3 "go.yaml.in/yaml/v3"
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
	delivery.Publication = &authored.Publication{RepositoryPrefix: "ghcr.io/example/nvkr-tools", CredentialRef: "github:NVKR_REGISTRY_AUTH"}
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
	workflow := expandedGitHubWorkflow(t, bundle)
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
		[]byte("secrets.NVKR_REGISTRY_AUTH"),
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
	bootstrapJob, bootstrapMember := githubTestBootstrap(t, bundle)
	stages, _, err := githubStages(plan)
	if err != nil || len(stages) == 0 {
		t.Fatalf("fixture has no stages: %v", err)
	}
	stage := stages[0]
	executeMember, _ := githubTestSharedInvocation(t, bundle, stage.ID, "execute", stage.Nodes[0].ID)
	set := func(value string, fields ...string) func(*testing.T, *yamlv3.Node) {
		return func(t *testing.T, root *yamlv3.Node) { githubTestYAMLField(t, root, fields...).Value = value }
	}
	step := func(t *testing.T, root *yamlv3.Node, field, value string) *yamlv3.Node {
		return githubTestYAMLStep(t, githubTestYAMLField(t, root, "runs", "steps"), field, value)
	}
	setup := func(t *testing.T, root *yamlv3.Node) *yamlv3.Node {
		return githubTestYAMLField(t, step(t, root, "uses", plan.Actions["setup-go"].Uses()), "with")
	}
	rewriteRun := func(field, value, before, after string) func(*testing.T, *yamlv3.Node) {
		return func(t *testing.T, root *yamlv3.Node) {
			run := githubTestYAMLField(t, step(t, root, field, value), "run")
			if strings.Count(run.Value, before) != 1 {
				t.Fatalf("fixture command does not uniquely contain %q", before)
			}
			run.Value = strings.Replace(run.Value, before, after, 1)
		}
	}
	tests := []struct {
		name, member string
		mutate       func(*testing.T, *yamlv3.Node)
		code         e.Code
		reason       string
	}{
		{"execution-event", bundle.WorkflowPath, set("true", "jobs", stage.ID, "if"), e.CodeEngineCapability, "admitted branch"},
		{"job-timeout", bundle.WorkflowPath, set("0", "jobs", bootstrapJob, "timeout-minutes"), e.CodeEngineCapability, "timeout"},
		{"stage-timeout", bundle.WorkflowPath, set("59", "jobs", stage.ID, "timeout-minutes"), e.CodeEngineCapability, "timeout"},
		{"finalizer-timeout", bundle.WorkflowPath, set("15", "jobs", githubFinalizerJobID, "timeout-minutes"), e.CodeEngineCapability, "timeout"},
		{"cancel-running-publication", bundle.WorkflowPath, set("true", "concurrency", "cancel-in-progress"), e.CodeEngineCapability, "concurrency"},
		{"unplanned-launch-argument", executeMember, rewriteRun("id", "operation_execute", "factory github run-node --plan", "factory github run-node --network bridge --plan"), e.CodeEngineCapability, ""},
		{"node-dispatch", executeMember, rewriteRun("id", "operation_execute", "factory github run-node --plan", "factory execute-node --plan"), e.CodeEngineCapability, ""},
		{"setup-go-version", bootstrapMember, func(t *testing.T, root *yamlv3.Node) {
			githubTestYAMLField(t, setup(t, root), "go-version-file").Value = "go.mod"
		}, e.CodeInvalidCatalog, "pinned Go toolchain"},
		{"setup-go-cache", bootstrapMember, func(t *testing.T, root *yamlv3.Node) { githubTestYAMLField(t, setup(t, root), "cache").Value = "true" }, e.CodeInvalidCatalog, "pinned Go toolchain"},
		{"setup-go-cache-removed", bootstrapMember, func(t *testing.T, root *yamlv3.Node) { githubYAMLRemove(setup(t, root), "cache") }, e.CodeInvalidCatalog, "pinned Go toolchain"},
		{"output", bootstrapMember, func(t *testing.T, root *yamlv3.Node) {
			githubTestYAMLField(t, step(t, root, "name", "Upload NVKR runner"), "with", "if-no-files-found").Value = "ignore"
		}, e.CodeEngineCapability, "missing declared output"},
		{"checkout-credential", bundle.WorkflowPath, func(t *testing.T, root *yamlv3.Node) {
			checkout := githubTestYAMLStep(t, githubTestYAMLField(t, root, "jobs", bootstrapJob, "steps"), "uses", plan.Actions["checkout"].Uses())
			githubTestYAMLField(t, checkout, "with", "persist-credentials").Value = "true"
		}, e.CodeEngineCapability, "ambient credentials"},
		{"pull-request-trigger", bundle.WorkflowPath, func(t *testing.T, root *yamlv3.Node) {
			githubYAMLSet(githubTestYAMLField(t, root, "on"), "pull_request", githubYAMLMap())
		}, e.CodeEngineCapability, "manual dispatch"},
		{"push-trigger", bundle.WorkflowPath, func(t *testing.T, root *yamlv3.Node) {
			githubYAMLSet(githubTestYAMLField(t, root, "on"), "push", githubYAMLMap())
		}, e.CodeEngineCapability, "manual dispatch"},
		{"regeneration-gate", bootstrapMember, rewriteRun("id", "validate-catalog", "--root . --check", "--root . --dry-run"), e.CodeInvalidCatalog, "build and transport the runner"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := mutateGitHubBundleYAML(t, bundle, test.member, func(root *yamlv3.Node) { test.mutate(t, root) })
			err := ValidateGitHubBundle(context.Background(), plan, mutated)
			assertFactoryErrorCode(t, err, test.code)
			if test.reason != "" && !strings.Contains(err.Error(), test.reason) {
				t.Fatalf("wrong guard rejected mutation: %v", err)
			}
		})
	}
}

// The helper arrives from download-artifact WITHOUT its execute bit, and every input
// Restore step is itself a `./nvkr-exec` invocation. The rendered job must therefore carry
// a dedicated preparation step directly after the helper download, before any helper use.
func TestRenderGitHubPreparesHelperBeforeFirstUse(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	var document githubWorkflowDocument
	if err := yaml.UnmarshalStrict(expandedGitHubWorkflow(t, bundle), &document); err != nil {
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
			if strings.Contains(step.Run, "./nvkr-exec") {
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
	document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
	changed := 0
	for id, job := range document.Jobs {
		var steps []githubStep
		for _, step := range job.Steps {
			if step.Name == prepareHelperStepName {
				changed++
				continue
			}
			if strings.Contains(step.Run, "factory github run-node") {
				step.Run = prepareHelperRun + "\n" + step.Run
			}
			steps = append(steps, step)
		}
		job.Steps = steps
		document.Jobs[id] = job
	}
	if changed == 0 {
		t.Fatal("legacy workflow reconstruction removed no preparation steps")
	}
	legacy, err := yaml.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	// This deliberately malformed helper order cannot pass shared-action
	// preparation admission. Keep it unshared to exercise the helper-order guard.
	mutated := unsharedGitHubTestBundle(t, plan, bundle, legacy)
	err = ValidateGitHubBundle(context.Background(), plan, mutated)
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

// Expand the generated local actions before asserting the logical job contract.
func expandedGitHubWorkflow(t *testing.T, bundle *GeneratedBundle) []byte {
	t.Helper()
	var plan Plan
	if err := canonical.UnmarshalExact(bundle.Files[bundle.PlanPath], &plan); err != nil {
		t.Fatal(err)
	}
	raw, err := expandGitHubActions(&plan, bundle.Files[bundle.WorkflowPath], bundle.Files)
	if err != nil {
		t.Fatal(err)
	}
	return logicalGitHubWorkflow(t, &plan, raw)
}

// Admit physical scheduling before examining logical artifact dependencies.
// Preserve the actual expanded programs and YAML styles used by the assertions.
func logicalGitHubWorkflow(t *testing.T, plan *Plan, raw []byte) []byte {
	t.Helper()
	logical := decodeGitHubWorkflow(t, raw)
	if err := admitGitHubNeeds(t.Context(), plan, &logical); err != nil {
		t.Fatal(err)
	}
	var parsed yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &parsed); err != nil || len(parsed.Content) != 1 {
		t.Fatalf("decode logical workflow: %v", err)
	}
	document, err := githubResolveAliases(&parsed, map[*yamlv3.Node]bool{})
	if err != nil {
		t.Fatal(err)
	}
	jobs := githubTestYAMLField(t, document.Content[0], "jobs")
	for _, id := range githubSerializedJobOrder(t, raw) {
		job, retained := logical.Jobs[id]
		if !retained {
			githubYAMLRemove(jobs, id)
			continue
		}
		node := githubYAMLField(jobs, id)
		if len(job.Needs) == 0 {
			githubYAMLRemove(node, "needs")
		} else {
			needs := &yamlv3.Node{Kind: yamlv3.SequenceNode, Tag: "!!seq", Style: yamlv3.FlowStyle}
			for _, dependency := range job.Needs {
				needs.Content = append(needs.Content, githubYAMLString(dependency))
			}
			githubYAMLSet(node, "needs", needs)
		}
		if id == "nvkr-lifecycle-run" {
			steps := githubYAMLField(node, "steps")
			if steps != nil && len(steps.Content) > 0 {
				last := steps.Content[len(steps.Content)-1]
				if githubYAMLValue(last, "id") == githubRunEndSummaryID {
					githubYAMLSet(githubYAMLField(last, "env"), "NVKR_JOB_RESULTS", githubYAMLString(githubRunEndSummary(len(job.Needs)).Env["NVKR_JOB_RESULTS"]))
				}
			}
		}
	}
	result, err := encodeGitHubYAML(document)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func githubTestYAMLBytes(t *testing.T, node *yamlv3.Node) []byte {
	t.Helper()
	raw, err := encodeGitHubYAML(node)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Mutation fixtures name the exact member instead of searching unrelated jobs,
// templates or serialized strings for the first coincidental match.
func mutateGitHubBundleFile(t *testing.T, bundle *GeneratedBundle, member string, mutate func([]byte) []byte) *GeneratedBundle {
	t.Helper()
	original, exists := bundle.Files[member]
	if !exists || len(original) == 0 {
		t.Fatalf("mutation member %q is missing", member)
	}
	raw := mutate(append([]byte(nil), original...))
	if bytes.Equal(raw, original) {
		t.Fatalf("mutation did not change %q", member)
	}
	changed := *bundle
	changed.Files = make(map[string][]byte, len(bundle.Files))
	for name, previous := range bundle.Files {
		changed.Files[name] = previous
	}
	changed.Files[member] = raw
	if member == bundle.WorkflowPath {
		changed.WorkflowDigest = pipeline.FileDigest(raw)
	}
	return &changed
}

func mutateGitHubBundleYAML(t *testing.T, bundle *GeneratedBundle, member string, mutate func(*yamlv3.Node)) *GeneratedBundle {
	t.Helper()
	return mutateGitHubBundleFile(t, bundle, member, func(raw []byte) []byte {
		var document yamlv3.Node
		if err := yamlv3.Unmarshal(raw, &document); err != nil || len(document.Content) != 1 {
			t.Fatalf("decode mutation member %q: %v", member, err)
		}
		before, err := encodeGitHubYAML(&document)
		if err != nil {
			t.Fatal(err)
		}
		mutate(document.Content[0])
		after, err := encodeGitHubYAML(&document)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(before, after) {
			t.Fatalf("mutation did not change a field in %q", member)
		}
		return after
	})
}

func githubTestYAMLField(t *testing.T, node *yamlv3.Node, fields ...string) *yamlv3.Node {
	t.Helper()
	for _, field := range fields {
		if node != nil && node.Kind == yamlv3.AliasNode {
			node = node.Alias
		}
		node = githubYAMLField(node, field)
		if node == nil {
			t.Fatalf("fixture omits YAML field %q in %v", field, fields)
		}
	}
	if node.Kind == yamlv3.AliasNode {
		node = node.Alias
	}
	return node
}

func githubTestYAMLStep(t *testing.T, steps *yamlv3.Node, field, value string) *yamlv3.Node {
	t.Helper()
	if steps.Kind != yamlv3.SequenceNode {
		t.Fatal("fixture steps are not a sequence")
	}
	var found *yamlv3.Node
	for _, step := range steps.Content {
		if step.Kind == yamlv3.AliasNode {
			step = step.Alias
		}
		if githubYAMLValue(step, field) == value {
			if found != nil {
				t.Fatalf("fixture repeats step %s=%q", field, value)
			}
			found = step
		}
	}
	if found == nil {
		t.Fatalf("fixture omits step %s=%q", field, value)
	}
	return found
}

func githubTestBootstrap(t *testing.T, bundle *GeneratedBundle) (jobID, member string) {
	t.Helper()
	document := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath])
	for _, id := range sortedKeys(document.Jobs) {
		if !strings.HasPrefix(id, "build-runner-") {
			continue
		}
		for _, step := range document.Jobs[id].Steps {
			if !strings.HasPrefix(step.Uses, "./") || !strings.Contains(step.Uses, "/bootstrap-") {
				continue
			}
			if member != "" {
				t.Fatalf("fixture bootstrap %q has more than one implementation", id)
			}
			jobID, member = id, strings.TrimPrefix(step.Uses, "./")+"/action.yml"
		}
		if member != "" && len(bundle.Files[member]) > 0 {
			return jobID, member
		}
	}
	t.Fatal("fixture has no referenced bootstrap implementation")
	return "", ""
}

func githubTestSharedInvocation(t *testing.T, bundle *GeneratedBundle, jobID, role, selector string) (member, part string) {
	t.Helper()
	var data githubActionDataDocument
	if err := canonical.UnmarshalExact(bundle.Files[path.Join(path.Dir(bundle.PlanPath), "action-data.json")], &data); err != nil {
		t.Fatal(err)
	}
	job, exists := decodeGitHubWorkflow(t, bundle.Files[bundle.WorkflowPath]).Jobs[jobID]
	if !exists {
		t.Fatalf("fixture omits job %q", jobID)
	}
	for _, step := range job.Steps {
		binding := step.With["parameters"]
		if binding == "" {
			continue
		}
		prefix := "${{ fromJSON(steps." + githubContextStepID + ".outputs.data)['"
		key := strings.TrimSuffix(strings.TrimPrefix(binding, prefix), "'] }}")
		if binding != githubParametersExpression(key) {
			t.Fatalf("fixture job %q has an unknown parameter binding", jobID)
		}
		var values map[string]string
		if err := canonical.UnmarshalExact([]byte(data.Entries[jobID].Values[key]), &values); err != nil {
			t.Fatal(err)
		}
		if values[role+"_present"] != "true" || (selector != "" && values[role+"_selector"] != selector) {
			continue
		}
		if member != "" {
			t.Fatalf("fixture job %q repeats role %q selector %q", jobID, role, selector)
		}
		member, part = strings.TrimPrefix(step.Uses, "./")+"/action.yml", key
	}
	if member == "" || len(bundle.Files[member]) == 0 {
		t.Fatalf("fixture job %q omits role %q selector %q", jobID, role, selector)
	}
	return member, part
}

func mutateGitHubActionParameters(t *testing.T, bundle *GeneratedBundle, jobID, part string, mutate func(map[string]string)) *GeneratedBundle {
	t.Helper()
	return mutateGitHubBundleFile(t, bundle, path.Join(path.Dir(bundle.PlanPath), "action-data.json"), func(raw []byte) []byte {
		var data githubActionDataDocument
		if err := canonical.UnmarshalExact(raw, &data); err != nil {
			t.Fatal(err)
		}
		entry := data.Entries[jobID]
		var values map[string]string
		if err := canonical.UnmarshalExact([]byte(entry.Values[part]), &values); err != nil {
			t.Fatal(err)
		}
		mutate(values)
		parameters, err := canonical.Marshal(values)
		if err != nil {
			t.Fatal(err)
		}
		entry.Values[part] = string(parameters)
		changed, err := canonical.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		return changed
	})
}

// Lower a deliberately hostile logical workflow through the same action boundary
// so semantic tests reach their intended guard instead of failing on stale members.
func relowerGitHubTestBundle(t *testing.T, plan *Plan, bundle *GeneratedBundle, raw []byte) *GeneratedBundle {
	t.Helper()
	changed := unsharedGitHubTestBundle(t, plan, bundle, raw)
	workflow, err := shareGitHubActions(plan, changed.Files[bundle.WorkflowPath], changed.Files, testActionRefs(t, plan))
	if err != nil {
		t.Fatal(err)
	}
	workflow, changed.Files, err = privatizeGitHubTransfers(plan, workflow, changed.Files, testActionRefs(t, plan))
	if err != nil {
		t.Fatal(err)
	}
	changed.Files[bundle.WorkflowPath] = workflow
	changed.WorkflowDigest = pipeline.FileDigest(workflow)
	return changed
}

// Fixtures with operations intentionally outside the shared interface use the
// lower-level boundary so their semantic violation reaches bundle validation.
func unsharedGitHubTestBundle(t *testing.T, plan *Plan, bundle *GeneratedBundle, raw []byte) *GeneratedBundle {
	t.Helper()
	if !bytes.Contains(raw, []byte("# plan: ")) {
		raw = append([]byte("# qualification: "+plan.Qualification.String()+"\n# plan: "+bundle.PlanPath+"\n# manifest: "+bundle.ManifestPath+"\n"), raw...)
	}
	workflow, files, err := lowerGitHubActions(plan, raw, testActionRefs(t, plan))
	if err != nil {
		t.Fatal(err)
	}
	workflow = lowerGitHubTestNeeds(t, plan, workflow)
	changed := *bundle
	changed.Files = files
	changed.Files[bundle.PlanPath] = append([]byte(nil), bundle.Files[bundle.PlanPath]...)
	changed.Files[bundle.WorkflowPath] = workflow
	changed.WorkflowDigest = pipeline.FileDigest(workflow)
	return &changed
}

// Logical mutation fixtures retain the original dependencies and summary input.
// Restore only their emitted scheduling representation, leaving mutated job
// policy, programs and summary fields available to the semantic validator.
func lowerGitHubTestNeeds(t *testing.T, plan *Plan, raw []byte) []byte {
	t.Helper()
	projection, err := githubNeedsForPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Collectors) != 0 {
		var document yamlv3.Node
		if err := yamlv3.Unmarshal(raw, &document); err != nil || len(document.Content) != 1 {
			t.Fatalf("decode logical mutation workflow: %v", err)
		}
		finalizer := githubYAMLField(githubYAMLField(document.Content[0], "jobs"), githubFinalizerJobID)
		steps := githubYAMLField(finalizer, "steps")
		if steps != nil && len(steps.Content) > 0 {
			last := steps.Content[len(steps.Content)-1]
			results := githubYAMLField(githubYAMLField(last, "env"), "NVKR_JOB_RESULTS")
			expected := len(projection.Full[githubFinalizerJobID])
			if githubYAMLValue(last, "id") == githubRunEndSummaryID && results != nil &&
				results.Value == githubRunEndSummary(expected).Env["NVKR_JOB_RESULTS"] {
				results.Value = githubCollectedRunEndSummary(expected).Env["NVKR_JOB_RESULTS"]
			}
		}
		raw, err = encodeGitHubYAML(&document)
		if err != nil {
			t.Fatal(err)
		}
	}
	lowered, err := lowerGitHubNeeds(t.Context(), plan, raw)
	if err != nil {
		t.Fatal(err)
	}
	return lowered
}

func assertGeneratedGitHubMembers(t *testing.T, bundle *GeneratedBundle) {
	t.Helper()
	if len(bundle.Files[bundle.PlanPath]) == 0 || len(bundle.Files[GitHubWorkflowPath]) == 0 || len(bundle.Files[GitHubActionDataPath]) == 0 {
		t.Fatal("generated bundle omits its Plan, workflow, or action data")
	}
	actions := 0
	for name := range bundle.Files {
		if name == bundle.PlanPath || name == GitHubWorkflowPath || name == GitHubActionDataPath {
			continue
		}
		if !strings.HasPrefix(name, ".github/nvkr-run/actions/") || !strings.HasSuffix(name, "/action.yml") {
			t.Fatalf("unexpected generated member %q", name)
		}
		actions++
	}
	if actions == 0 {
		t.Fatal("generated bundle omits local actions")
	}
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
			document := decodeGitHubWorkflow(t, expandedGitHubWorkflow(t, bundle))
			for name, job := range document.Jobs {
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
