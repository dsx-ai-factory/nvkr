// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"strconv"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

const githubMaximumReusableWorkflows = 50

// Each call retains one original scheduling/result slot. Only the closed,
// credential-free native host sequence moves; signing, environments and disk
// ownership remain in their original jobs. No child reports replace needs.
func lowerGitHubReusableWorkflows(plan *Plan, raw []byte, files map[string][]byte, actions githubActionRefs) ([]byte, error) {
	document, jobs, err := githubReusableDocument(raw)
	if err != nil {
		return nil, err
	}
	generated := map[string][]byte{}
	for index := 0; index < len(jobs.Content); index += 2 {
		id, job := jobs.Content[index].Value, jobs.Content[index+1]
		if !githubReusableFields(job, "name", "runs-on", "timeout-minutes", "needs", "if", "steps") {
			continue
		}
		sequence := githubStepSequenceName(plan, job, actions)
		if sequence == "" {
			continue
		}
		timeout := githubYAMLField(job, "timeout-minutes")
		if !githubReusableTimeout(timeout) {
			return nil, e.New(e.CodeEngineCapability, "reusable GitHub stage %q requires a bounded integer timeout", id)
		}
		token := "execute-" + strings.TrimPrefix(sequence, "steps-")
		if len(token) > 64 {
			return nil, e.New(e.CodeEngineCapability, "GitHub reusable template name exceeds 64 bytes")
		}
		path := githubReusableWorkflowPath(plan, token)
		child, err := githubReusableTemplate(job, token)
		if err != nil {
			return nil, err
		}
		encoded, err := encodeGitHubYAML(child)
		if err != nil {
			return nil, err
		}
		if err := validateGitHubWorkflowSize(encoded); err != nil {
			return nil, err
		}
		if previous, exists := generated[path]; exists && !bytes.Equal(previous, encoded) {
			return nil, e.New(e.CodeEngineCapability, "reusable GitHub template %q has conflicting implementations", token)
		}
		if previous, exists := files[path]; exists && !bytes.Equal(previous, encoded) {
			return nil, e.New(e.CodeEngineCapability, "reusable GitHub template %q conflicts with an existing member", token)
		}
		generated[path] = encoded
		if len(generated) > githubMaximumReusableWorkflows {
			return nil, e.New(e.CodeEngineCapability, "GitHub workflow exceeds %d unique reusable workflows", githubMaximumReusableWorkflows)
		}
		call := githubYAMLMap()
		for _, field := range []string{"name", "needs", "if"} {
			if value := githubYAMLField(job, field); value != nil {
				githubYAMLSet(call, field, value)
			}
		}
		githubYAMLSet(call, "uses", githubYAMLString("./"+path))
		githubYAMLSet(call, "with", githubYAMLMap("stage", githubYAMLString(id), "timeout", timeout))
		jobs.Content[index+1] = call
	}
	if len(generated) == 0 {
		return raw, nil
	}
	// Extracted jobs may have owned anchors used by the remaining inline jobs.
	// Rebuild only those inline aliases; calls have no steps to anchor.
	inline := githubYAMLMap()
	for index := 0; index < len(jobs.Content); index += 2 {
		if githubYAMLField(jobs.Content[index+1], "uses") == nil {
			githubYAMLSet(inline, jobs.Content[index].Value, jobs.Content[index+1])
		}
	}
	if len(inline.Content) != 0 {
		if err := anchorGitHubPreflight(plan, inline, actions); err != nil {
			return nil, err
		}
		for index := 0; index < len(jobs.Content); index += 2 {
			if job := githubYAMLField(inline, jobs.Content[index].Value); job != nil {
				jobs.Content[index+1] = job
			}
		}
	}
	workflow, err := encodeGitHubYAML(document)
	if err != nil {
		return nil, err
	}
	for path, encoded := range generated {
		files[path] = encoded
	}
	return workflow, nil
}

// Expand only a same-bundle, one-job template with exact generated inputs and
// structure. The old selector binding is restored only after stage == caller ID
// is established. Existing action/graph validation then sees the original job.
func expandGitHubReusableWorkflows(ctx context.Context, plan *Plan, raw []byte, files map[string][]byte) ([]byte, error) {
	document, jobs, err := githubReusableDocument(raw)
	if err != nil {
		// The existing workflow/action parser owns malformed entry diagnostics.
		// Preserve its coded errors when no valid call structure can be examined.
		return raw, nil
	}
	seen := map[string]bool{}
	var actions githubActionRefs
	for index := 0; index < len(jobs.Content); index += 2 {
		id, call := jobs.Content[index].Value, jobs.Content[index+1]
		if githubYAMLField(call, "uses") == nil {
			continue
		}
		if len(seen) == 0 {
			actions, err = resolveGitHubActions(plan.Actions)
			if err != nil {
				return nil, err
			}
		}
		with := githubYAMLField(call, "with")
		stage := githubYAMLField(with, "stage")
		if !githubReusableFields(call, "name", "needs", "if", "uses", "with") ||
			!githubReusableFields(with, "stage", "timeout") || githubYAMLLength(with) != 4 ||
			stage == nil || stage.Kind != yamlv3.ScalarNode || stage.Tag != "!!str" || stage.Value != id ||
			!githubReusableTimeout(githubYAMLField(with, "timeout")) {
			return nil, e.New(e.CodeEngineCapability, "GitHub reusable call %q changes its exact stage interface", id)
		}
		uses := githubYAMLField(call, "uses")
		prefix := "./" + strings.TrimSuffix(githubLayoutFor(plan).workflowPath, ".yml") + "__"
		if uses.Kind != yamlv3.ScalarNode || uses.Tag != "!!str" || !strings.HasPrefix(uses.Value, prefix) || !strings.HasSuffix(uses.Value, ".yml") {
			return nil, e.New(e.CodeEngineCapability, "GitHub reusable call %q is outside its owned workflow templates", id)
		}
		path := strings.TrimPrefix(uses.Value, "./")
		if !safeGeneratedPath(path) || len(files[path]) == 0 {
			return nil, e.New(e.CodePipelineDrift, "GitHub reusable workflow %q is missing", path)
		}
		if err := validateGitHubWorkflowSize(files[path]); err != nil {
			return nil, err
		}
		seen[path] = true
		if len(seen) > githubMaximumReusableWorkflows {
			return nil, e.New(e.CodeEngineCapability, "GitHub workflow exceeds %d unique reusable workflows", githubMaximumReusableWorkflows)
		}
		child, childJobs, err := githubReusableDocument(files[path])
		if err != nil {
			return nil, err
		}
		if !githubReusableFields(childJobs, "execute") || githubYAMLLength(childJobs) != 2 {
			return nil, e.New(e.CodeEngineCapability, "GitHub reusable workflow %q requires one execution job", path)
		}
		execution := githubYAMLField(childJobs, "execute")
		steps := githubYAMLField(execution, "steps")
		if steps == nil || steps.Kind != yamlv3.SequenceNode || len(steps.Content) < 3 ||
			githubYAMLValue(githubYAMLField(steps.Content[2], "with"), "action") != "${{ inputs.stage }}" {
			return nil, e.New(e.CodeEngineCapability, "GitHub reusable workflow %q changes its stage selector", path)
		}
		restored, err := githubResolveAliases(execution, map[*yamlv3.Node]bool{})
		if err != nil {
			return nil, err
		}
		githubYAMLSet(githubYAMLField(githubYAMLField(restored, "steps").Content[2], "with"), "action", githubYAMLString("${{ github.job }}"))
		sequence := githubStepSequenceName(plan, restored, actions)
		if sequence == "" {
			return nil, e.New(e.CodeEngineCapability, "GitHub reusable workflow %q has an unsupported execution sequence", path)
		}
		token := "execute-" + strings.TrimPrefix(sequence, "steps-")
		expected, err := githubReusableTemplate(restored, token)
		if err != nil {
			return nil, err
		}
		if len(token) > 64 || path != githubReusableWorkflowPath(plan, token) || !githubReusableEqual(child.Content[0], expected) {
			return nil, e.New(e.CodeEngineCapability, "GitHub reusable workflow %q differs from its closed execution template", path)
		}
		job := githubYAMLMap()
		if name := githubYAMLField(call, "name"); name != nil {
			githubYAMLSet(job, "name", name)
		}
		githubYAMLSet(job, "runs-on", githubYAMLField(restored, "runs-on"))
		githubYAMLSet(job, "timeout-minutes", githubYAMLField(with, "timeout"))
		for _, field := range []string{"needs", "if"} {
			if value := githubYAMLField(call, field); value != nil {
				githubYAMLSet(job, field, value)
			}
		}
		githubYAMLSet(job, "steps", githubYAMLField(restored, "steps"))
		jobs.Content[index+1] = job
	}
	if len(seen) == 0 {
		return raw, nil
	}
	return encodeGitHubYAML(document)
}

func githubReusableWorkflowPath(plan *Plan, token string) string {
	return strings.TrimSuffix(githubLayoutFor(plan).workflowPath, ".yml") + "__" + token + ".yml"
}

func githubReusableTemplate(job *yamlv3.Node, token string) (*yamlv3.Node, error) {
	steps, err := githubResolveAliases(githubYAMLField(job, "steps"), map[*yamlv3.Node]bool{})
	if err != nil {
		return nil, err
	}
	githubYAMLSet(githubYAMLField(steps.Content[2], "with"), "action", githubYAMLString("${{ inputs.stage }}"))
	input := func(kind string) *yamlv3.Node {
		return githubYAMLMap("required", &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!bool", Value: "true"}, "type", githubYAMLString(kind))
	}
	template := githubYAMLMap("name", githubYAMLString("NVKR "+token),
		"on", githubYAMLMap("workflow_call", githubYAMLMap("inputs", githubYAMLMap("stage", input("string"), "timeout", input("number")))),
		"permissions", githubYAMLMap("contents", githubYAMLString("read")),
		"jobs", githubYAMLMap("execute", githubYAMLMap("name", githubYAMLString("Execute stage"),
			"runs-on", githubYAMLField(job, "runs-on"), "timeout-minutes", githubYAMLString("${{ inputs.timeout }}"), "steps", steps)))
	template.HeadComment = "SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.\nSPDX-License-Identifier: Apache-2.0\n\nGENERATED BY nvkr run generate — DO NOT EDIT"
	return template, nil
}

func githubReusableDocument(raw []byte) (*yamlv3.Node, *yamlv3.Node, error) {
	var parsed yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &parsed); err != nil || len(parsed.Content) != 1 {
		return nil, nil, e.New(e.CodePipelineDrift, "GitHub reusable workflow requires one valid YAML document")
	}
	document, err := githubResolveAliases(&parsed, map[*yamlv3.Node]bool{})
	if err != nil {
		return nil, nil, err
	}
	jobs := githubYAMLField(document.Content[0], "jobs")
	if jobs == nil || jobs.Kind != yamlv3.MappingNode {
		return nil, nil, e.New(e.CodePipelineDrift, "GitHub reusable workflow requires jobs")
	}
	return document, jobs, nil
}

func githubReusableFields(node *yamlv3.Node, allowed ...string) bool {
	if node == nil || node.Kind != yamlv3.MappingNode || len(node.Content)%2 != 0 {
		return false
	}
	seen := map[string]bool{}
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index]
		if key.Kind != yamlv3.ScalarNode || key.Tag != "!!str" || seen[key.Value] {
			return false
		}
		seen[key.Value] = true
		found := false
		for _, name := range allowed {
			found = found || key.Value == name
		}
		if !found {
			return false
		}
	}
	return true
}

func githubReusableTimeout(node *yamlv3.Node) bool {
	if node == nil || node.Kind != yamlv3.ScalarNode || node.Tag != "!!int" {
		return false
	}
	value, err := strconv.Atoi(node.Value)
	return err == nil && value > 0 && value <= githubMaximumJobTimeoutMinutes && strconv.Itoa(value) == node.Value
}

// YAML formatting and anchors are presentation. All scalar types, map keys,
// sequence order and field presence remain part of the closed child interface.
func githubReusableEqual(left, right *yamlv3.Node) bool {
	if left == nil || right == nil {
		return left == right
	}
	if left.Kind != right.Kind || left.Tag != right.Tag || left.Value != right.Value || len(left.Content) != len(right.Content) {
		return false
	}
	if left.Kind == yamlv3.MappingNode {
		seen := map[string]bool{}
		for index := 0; index < len(left.Content); index += 2 {
			key := left.Content[index]
			if key.Kind != yamlv3.ScalarNode || key.Tag != "!!str" || seen[key.Value] ||
				!githubReusableEqual(left.Content[index+1], githubYAMLField(right, key.Value)) {
				return false
			}
			seen[key.Value] = true
		}
		return true
	}
	for index := range left.Content {
		if !githubReusableEqual(left.Content[index], right.Content[index]) {
			return false
		}
	}
	return true
}
