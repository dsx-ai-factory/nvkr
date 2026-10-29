// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"regexp"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

const githubPartitionReporter = "nvkr-partition-results"

var (
	githubPartitionSecret      = regexp.MustCompile(`(?i)\bsecrets(?:\s*\.\s*([a-z_][a-z0-9_]*)|\s*\[\s*'([a-z_][a-z0-9_]*)'\s*\])`)
	githubPartitionSecretsWord = regexp.MustCompile(`(?i)\bsecrets\b`)
)

// renderGitHubPartition keeps original job IDs: shared action selection uses
// github.job to select parameters from the complete, unchanged Plan.
func renderGitHubPartition(ctx context.Context, plan *Plan, raw []byte, partition githubExecutionPartition, model *executionGraph) ([]byte, error) {
	if err := githubNeedsContext(ctx); err != nil {
		return nil, err
	}
	if plan == nil {
		return nil, e.New(e.CodeUsage, "GitHub partition requires a Plan")
	}
	if len(partition.Jobs) == 0 || len(partition.Jobs) > githubMaximumResultInputs {
		return nil, e.New(e.CodeEngineCapability, "GitHub execution partition has invalid result capacity")
	}
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil || len(document.Content) != 1 {
		return nil, e.New(e.CodePipelineDrift, "GitHub partition requires a workflow document")
	}
	resolved, err := githubResolveAliases(document.Content[0], map[*yamlv3.Node]bool{})
	if err != nil {
		return nil, err
	}
	jobs := githubYAMLField(resolved, "jobs")
	if jobs == nil || jobs.Kind != yamlv3.MappingNode || githubYAMLField(resolved, "permissions") == nil {
		return nil, e.New(e.CodePipelineDrift, "GitHub partition requires jobs and explicit permissions")
	}
	selected := map[string]bool{}
	for _, id := range partition.Jobs {
		if selected[id] || id == githubPartitionReporter || githubYAMLField(jobs, id) == nil {
			return nil, e.New(e.CodePipelineDrift, "GitHub partition has duplicate, reserved or missing job %q", id)
		}
		selected[id] = true
	}
	dependencies := make(map[string][]string, len(partition.Jobs))
	for _, id := range partition.Jobs {
		if err := githubNeedsContext(ctx); err != nil {
			return nil, err
		}
		parents, err := model.prerequisiteResults(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, parent := range parents {
			if selected[parent] || githubYAMLField(jobs, parent) == nil {
				return nil, e.New(e.CodePipelineDrift, "GitHub partition is not one admitted dependency layer")
			}
		}
		dependencies[id] = parents
	}
	childJobs := githubYAMLMap()
	for _, id := range partition.Jobs {
		job, err := projectGitHubPartitionJob(plan, id, githubYAMLField(jobs, id), dependencies[id], nil, model.operations[id].Condition)
		if err != nil {
			return nil, err
		}
		githubYAMLSet(childJobs, id, job)
	}
	githubYAMLSet(childJobs, githubPartitionReporter, githubResultCollectorYAML(plan, githubPartitionReporter, partition.Jobs))
	outputs := githubYAMLMap()
	for _, name := range []string{"results", "statuses"} {
		githubYAMLSet(outputs, name, githubYAMLMap("value", githubYAMLString("${{ jobs."+githubPartitionReporter+".outputs."+name+" }}")))
	}
	call := githubYAMLMap("inputs", githubYAMLMap("dependencies", githubYAMLMap(
		"type", githubYAMLString("string"), "required", &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!bool", Value: "true"})), "outputs", outputs)
	child := githubYAMLMap("name", githubYAMLString("NVKR execution partition"), "on", githubYAMLMap("workflow_call", call),
		"permissions", githubInterfaceClone(githubYAMLField(resolved, "permissions")), "jobs", childJobs)
	if env := githubYAMLField(resolved, "env"); env != nil {
		githubYAMLSet(child, "env", githubInterfaceClone(env))
	}
	names, err := githubPartitionSecretNames(child)
	if err != nil {
		return nil, err
	}
	secrets := githubYAMLMap()
	for _, name := range names {
		githubYAMLSet(secrets, name, githubYAMLMap("required", &yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!bool", Value: "true"}))
	}
	if len(secrets.Content) != 0 {
		githubYAMLSet(call, "secrets", secrets)
	}
	encoded, err := encodeGitHubYAML(child)
	if err != nil {
		return nil, err
	}
	if err := validateGitHubWorkflowSize(encoded); err != nil {
		return nil, err
	}
	return encoded, githubNeedsContext(ctx)
}

func githubPartitionSecretNames(node *yamlv3.Node) ([]string, error) {
	var names []string
	if node.Kind == yamlv3.ScalarNode {
		expressions, err := githubExpressions(node.Value)
		if err != nil {
			return nil, err
		}
		for _, expression := range expressions {
			expressionNames, err := githubPartitionExpressionSecretNames(expression)
			if err != nil {
				return nil, err
			}
			names = append(names, expressionNames...)
		}
	}
	for index, child := range node.Content {
		// GitHub evaluates an if value as an expression even without ${{ }}.
		if node.Kind == yamlv3.MappingNode && index%2 == 1 && node.Content[index-1].Value == "if" &&
			child.Kind == yamlv3.ScalarNode && !strings.Contains(child.Value, "${{") {
			childNames, err := githubPartitionExpressionSecretNames(child.Value)
			if err != nil {
				return nil, err
			}
			names = append(names, childNames...)
			continue
		}
		childNames, err := githubPartitionSecretNames(child)
		if err != nil {
			return nil, err
		}
		names = append(names, childNames...)
	}
	return uniqueSorted(names), nil
}

func githubPartitionExpressionSecretNames(expression string) ([]string, error) {
	var names []string
	for _, match := range githubPartitionSecret.FindAllStringSubmatch(expression, -1) {
		name := strings.ToUpper(match[1] + match[2])
		if name != "GITHUB_TOKEN" {
			names = append(names, name)
		}
	}
	if githubPartitionSecretsWord.MatchString(githubPartitionSecret.ReplaceAllString(expression, "")) {
		return nil, e.New(e.CodeEngineCapability, "GitHub partition requires statically named secret references")
	}
	return names, nil
}

// githubExpressions returns the bodies of ${{ }} spans. A closing }} inside a
// single-quoted string literal does not end the span; an unterminated span is
// refused rather than treated as literal text.
func githubExpressions(value string) ([]string, error) {
	var expressions []string
	for {
		start := strings.Index(value, "${{")
		if start < 0 {
			return expressions, nil
		}
		value = value[start+3:]
		quoted, end := false, -1
		for index := 0; index < len(value) && end < 0; index++ {
			switch {
			case value[index] == '\'':
				quoted = !quoted
			case !quoted && strings.HasPrefix(value[index:], "}}"):
				end = index
			}
		}
		if end < 0 {
			return nil, e.New(e.CodeEngineCapability, "GitHub partition found an unterminated expression")
		}
		expressions = append(expressions, value[:end])
		value = value[end+2:]
	}
}

func projectGitHubPartitionJob(plan *Plan, id string, source *yamlv3.Node, parents []string, owners map[string]string, scheduling executionCondition) (*yamlv3.Node, error) {
	job := githubInterfaceClone(source)
	githubYAMLRemove(job, "needs")
	condition := githubYAMLValue(job, "if")
	// Needs-observing authored expressions need an exact context projection;
	// never silently evaluate them against the worker's empty local needs.
	encoded, err := encodeGitHubYAML(job)
	if err != nil {
		return nil, err
	}
	if githubNeedsWord.Match(encoded) {
		return nil, e.New(e.CodeEngineCapability, "GitHub partition job %q observes an unprojected needs context", id)
	}
	ordinary := "${{ " + githubDispatchCondition(plan) + " }}"
	unconditional := "${{ always() && " + githubDispatchCondition(plan) + " }}"
	expectedCondition := ordinary
	if scheduling == executionAfterCompletion {
		expectedCondition = unconditional
	}
	if (scheduling != executionAfterSuccess && scheduling != executionAfterCompletion) || condition != expectedCondition {
		return nil, e.New(e.CodeEngineCapability, "GitHub partition job %q has an unsupported scheduling condition", id)
	}
	guard := "always() && " + githubDispatchCondition(plan)
	if scheduling == executionAfterSuccess {
		guard = "!cancelled() && " + githubDispatchCondition(plan) //nolint:misspell // GitHub expression function spelling.
		for _, parent := range parents {
			result := "fromJSON(inputs.dependencies)['" + parent + "']"
			if owners != nil {
				result = githubPartitionResultExpression(parent, owners)
			}
			guard += " && " + result + ".result == 'success'"
		}
	}
	githubYAMLSet(job, "if", githubYAMLString("${{ "+guard+" }}"))
	return job, nil
}
