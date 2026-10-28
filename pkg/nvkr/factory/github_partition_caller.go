// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"fmt"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
)

const githubMaximumExecutionPartitions = 50

func githubPartitionID(index int) string { return fmt.Sprintf("nvkr-partition-%02d", index) }

// lowerGitHubPartitions changes only provider scheduling. The original result
// collectors and single finalizer retain their Plan-derived membership.
func lowerGitHubPartitions(ctx context.Context, plan *Plan, raw []byte) ([]byte, map[string][]byte, error) {
	if err := githubNeedsContext(ctx); err != nil {
		return nil, nil, err
	}
	if plan == nil {
		return nil, nil, e.New(e.CodeUsage, "GitHub partitioning requires a Plan")
	}
	projection, err := githubNeedsForPlan(ctx, plan)
	if err != nil {
		return nil, nil, err
	}
	var document yamlv3.Node
	if err := yamlv3.Unmarshal(raw, &document); err != nil || len(document.Content) != 1 {
		return nil, nil, e.New(e.CodePipelineDrift, "GitHub partitioning requires one workflow")
	}
	root, err := githubResolveAliases(document.Content[0], map[*yamlv3.Node]bool{})
	if err != nil {
		return nil, nil, err
	}
	jobs := githubYAMLField(root, "jobs")
	if !githubYAMLOnlyFields(jobs, sortedKeys(projection.Reduced)...) || githubYAMLLength(jobs) != 2*len(projection.Reduced) {
		return nil, nil, e.New(e.CodePipelineDrift, "GitHub partitioning requires the complete projected jobs")
	}
	dependencies := map[string][]string{}
	for _, id := range sortedKeys(projection.Reduced) {
		if id == githubFinalizerJobID || projection.Collectors[id] != nil {
			continue
		}
		parents, err := githubPartitionPrerequisites(ctx, plan, jobs, id)
		if err != nil {
			return nil, nil, err
		}
		dependencies[id] = parents
	}
	partitions, err := partitionGitHubJobs(ctx, dependencies, githubMaximumResultInputs)
	if err != nil {
		return nil, nil, err
	}
	// Keep OIDC signers in the entry: Fulcio's certificate SAN follows the
	// reusable workflow path, so moving these jobs would change signer policy.
	owners := map[string]string{}
	var workers []githubExecutionPartition
	for _, partition := range partitions {
		var memberIDs []string
		for _, id := range partition.Jobs {
			permissions := githubYAMLField(githubYAMLField(jobs, id), "permissions")
			if githubYAMLValue(permissions, "id-token") == "write" {
				owners[id] = id
			} else {
				memberIDs = append(memberIDs, id)
			}
		}
		if len(memberIDs) != 0 {
			workers = append(workers, githubExecutionPartition{Jobs: memberIDs})
		}
	}
	partitions = workers
	if len(partitions) > githubMaximumExecutionPartitions {
		return nil, nil, e.New(e.CodeEngineCapability, "GitHub Run exceeds %d reusable execution partitions", githubMaximumExecutionPartitions)
	}
	for index, partition := range partitions {
		id := githubPartitionID(index)
		if githubYAMLField(jobs, id) != nil {
			return nil, nil, e.New(e.CodeEngineCapability, "GitHub partition name collides with original job")
		}
		for _, job := range partition.Jobs {
			owners[job] = id
		}
	}
	files := map[string][]byte{}
	callerJobs := githubYAMLMap()
	for index, partition := range partitions {
		child, err := renderGitHubPartition(ctx, plan, raw, partition)
		if err != nil {
			return nil, nil, err
		}
		id := githubPartitionID(index)
		path := strings.TrimSuffix(githubLayoutFor(plan).workflowPath, ".yml") + fmt.Sprintf("__partition-%02d.yml", index)
		files[path] = child
		var childDocument yamlv3.Node
		if err := yamlv3.Unmarshal(child, &childDocument); err != nil {
			return nil, nil, e.Wrap(e.CodePipelineDrift, err, "decode generated partition")
		}
		permissions := githubYAMLMap("contents", githubYAMLString("read"))
		var parents []string
		for _, jobID := range partition.Jobs {
			parents = append(parents, dependencies[jobID]...)
			declared := githubYAMLField(githubYAMLField(jobs, jobID), "permissions")
			if declared == nil {
				continue
			}
			for n := 0; n < len(declared.Content); n += 2 {
				key, value := declared.Content[n].Value, declared.Content[n+1].Value
				if value == "write" || githubYAMLField(permissions, key) == nil {
					githubYAMLSet(permissions, key, githubYAMLString(value))
				}
			}
		}
		parents = uniqueSorted(parents)
		call := githubYAMLMap("uses", githubYAMLString("./"+path), "if", githubYAMLString("${{ always() && "+githubDispatchCondition(plan)+" }}"),
			"permissions", permissions, "with", githubYAMLMap("dependencies", githubYAMLString("${{ "+githubPartitionResultsExpression(parents, owners)+" }}")))
		var needs []string
		for _, parent := range parents {
			needs = append(needs, owners[parent])
		}
		if len(needs) != 0 {
			githubYAMLSet(call, "needs", githubNeedsSequence(uniqueSorted(needs)))
		}
		declarations := githubYAMLField(githubYAMLField(githubYAMLField(childDocument.Content[0], "on"), "workflow_call"), "secrets")
		if declarations != nil {
			secrets := githubYAMLMap()
			for n := 0; n < len(declarations.Content); n += 2 {
				name := declarations.Content[n].Value
				githubYAMLSet(secrets, name, githubYAMLString("${{ secrets."+name+" }}"))
			}
			githubYAMLSet(call, "secrets", secrets)
		}
		githubYAMLSet(callerJobs, id, call)
	}
	for _, id := range sortedKeys(dependencies) {
		if owners[id] != id {
			continue
		}
		job, err := projectGitHubPartitionJob(plan, id, githubYAMLField(jobs, id), dependencies[id], owners)
		if err != nil {
			return nil, nil, err
		}
		var needs []string
		for _, parent := range dependencies[id] {
			needs = append(needs, owners[parent])
		}
		if len(needs) != 0 {
			githubYAMLSet(job, "needs", githubNeedsSequence(uniqueSorted(needs)))
		}
		githubYAMLSet(callerJobs, id, job)
	}
	for _, id := range sortedKeys(projection.Collectors) {
		job := githubInterfaceClone(githubYAMLField(jobs, id))
		members := projection.Collectors[id]
		var needs []string
		for _, member := range members {
			needs = append(needs, owners[member])
		}
		githubYAMLSet(job, "needs", githubNeedsSequence(uniqueSorted(needs)))
		githubYAMLSet(job, "outputs", githubYAMLMap("results", githubYAMLString("${{ "+githubPartitionResultsExpression(members, owners)+" }}"),
			"statuses", githubYAMLString("${{ "+githubPartitionStatusesExpression(members, owners)+" }}")))
		githubYAMLSet(callerJobs, id, job)
	}
	if finalizer := githubYAMLField(jobs, githubFinalizerJobID); finalizer != nil {
		finalizer = githubInterfaceClone(finalizer)
		if len(projection.Collectors) == 0 {
			members := projection.Full[githubFinalizerJobID]
			var needs []string
			for _, member := range members {
				needs = append(needs, owners[member])
			}
			githubYAMLSet(finalizer, "needs", githubNeedsSequence(uniqueSorted(needs)))
			rewriteGitHubPartitionResults(finalizer, members, owners)
		}
		githubYAMLSet(callerJobs, githubFinalizerJobID, finalizer)
	}
	actions, err := resolveGitHubActions(plan.Actions)
	if err != nil {
		return nil, nil, err
	}
	if err := anchorGitHubPreflight(plan, callerJobs, actions); err != nil {
		return nil, nil, err
	}
	githubYAMLSet(root, "jobs", callerJobs)
	entry, err := encodeGitHubYAML(root)
	if err != nil {
		return nil, nil, err
	}
	if err := validateGitHubWorkflowSize(entry); err != nil {
		return nil, nil, err
	}
	return entry, files, githubNeedsContext(ctx)
}

func githubPartitionResultExpression(id string, owners map[string]string) string {
	if owners[id] == id {
		return "needs['" + id + "']"
	}
	return "fromJSON(needs['" + owners[id] + "'].outputs.results || '{}')['" + id + "']"
}

func githubPartitionResultsExpression(ids []string, owners map[string]string) string {
	if len(ids) == 0 {
		return "'{}'"
	}
	var fields, args []string
	for n, id := range ids {
		fields = append(fields, fmt.Sprintf(`"%s":{%d}`, id, n))
		args = append(args, "toJSON("+githubPartitionResultExpression(id, owners)+")")
	}
	return "format('{{" + strings.Join(fields, ",") + "}}', " + strings.Join(args, ", ") + ")"
}

func githubPartitionStatusesExpression(ids []string, owners map[string]string) string {
	var fields, args []string
	for n, id := range ids {
		fields = append(fields, fmt.Sprintf("{%d}", n))
		args = append(args, githubPartitionResultExpression(id, owners)+".result")
	}
	return "format('" + strings.Join(fields, " ") + "', " + strings.Join(args, ", ") + ")"
}

func rewriteGitHubPartitionResults(node *yamlv3.Node, ids []string, owners map[string]string) {
	if node.Kind == yamlv3.ScalarNode {
		node.Value = strings.ReplaceAll(node.Value, "toJSON(needs)", githubPartitionResultsExpression(ids, owners))
		node.Value = strings.ReplaceAll(node.Value, "join(needs.*.result, ' ')", githubPartitionStatusesExpression(ids, owners))
	}
	for _, child := range node.Content {
		rewriteGitHubPartitionResults(child, ids, owners)
	}
}
