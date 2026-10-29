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
	model, inline, err := githubExecutionModel(ctx, plan, projection)
	if err != nil {
		return nil, nil, err
	}
	dependencies, err := model.dependencies(ctx)
	if err != nil {
		return nil, nil, err
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
			if inline[id] {
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
	var groups [][]string
	for _, partition := range partitions {
		groups = append(groups, partition.Jobs)
	}
	for _, id := range sortedKeys(inline) {
		groups = append(groups, []string{id})
	}
	if err := model.validatePartitions(ctx, groups); err != nil {
		return nil, nil, err
	}
	partitionParents := make([][]string, len(partitions))
	consumers := []githubPartitionExport{}
	for index, partition := range partitions {
		var parents []string
		for _, jobID := range partition.Jobs {
			parents = append(parents, dependencies[jobID]...)
		}
		partitionParents[index] = uniqueSorted(parents)
		consumers = append(consumers, githubPartitionExport{Name: githubPartitionID(index), Members: partitionParents[index]})
	}
	for _, id := range sortedKeys(projection.Collectors) {
		consumers = append(consumers, githubPartitionExport{Name: id, Members: projection.Collectors[id], Statuses: true})
	}
	if len(projection.Collectors) == 0 {
		consumers = append(consumers, githubPartitionExport{Name: githubFinalizerJobID, Members: projection.Full[githubFinalizerJobID], Statuses: true})
	}
	files := map[string][]byte{}
	callerJobs := githubYAMLMap()
	for index, partition := range partitions {
		id := githubPartitionID(index)
		child, err := renderGitHubPartition(ctx, plan, raw, partition, model, githubPartitionExports(id, consumers, owners))
		if err != nil {
			return nil, nil, err
		}
		path := strings.TrimSuffix(githubLayoutFor(plan).workflowPath, ".yml") + fmt.Sprintf("__partition-%02d.yml", index)
		files[path] = child
		var childDocument yamlv3.Node
		if err := yamlv3.Unmarshal(child, &childDocument); err != nil {
			return nil, nil, e.Wrap(e.CodePipelineDrift, err, "decode generated partition")
		}
		if githubYAMLHasKey(&childDocument, "id-token") {
			return nil, nil, e.New(e.CodeEngineCapability, "GitHub execution partition %s would mint OIDC tokens outside the admitted signer workflow", id)
		}
		permissions := githubYAMLMap("contents", githubYAMLString("read"))
		parents := partitionParents[index]
		for _, jobID := range partition.Jobs {
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
		if githubYAMLField(permissions, "id-token") != nil {
			return nil, nil, e.New(e.CodeEngineCapability, "GitHub execution partition %s caller would grant OIDC tokens outside the admitted signer workflow", id)
		}
		call := githubYAMLMap("uses", githubYAMLString("./"+path), "if", githubYAMLString("${{ always() && "+githubDispatchCondition(plan)+" }}"),
			"permissions", permissions, "with", githubYAMLMap("dependencies", githubYAMLString("${{ "+githubPartitionResultsExpression(id, parents, owners)+" }}")))
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
		job, err := projectGitHubPartitionJob(plan, id, githubYAMLField(jobs, id), dependencies[id], owners, model.operations[id].Condition)
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
		githubYAMLSet(job, "outputs", githubYAMLMap("results", githubYAMLString("${{ "+githubPartitionResultsExpression(id, members, owners)+" }}"),
			"statuses", githubYAMLString("${{ "+githubPartitionStatusesExpression(id, members, owners)+" }}")))
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
			rewriteGitHubPartitionResults(finalizer, githubFinalizerJobID, members, owners)
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

// githubPartitionExport is one consumer's slice of a worker's results. Workers
// pre-project their members so the entry forwards one fragment per worker
// instead of re-deriving every member from the worker's complete report.
type githubPartitionExport struct {
	Name     string
	Members  []string
	Statuses bool
}

func githubPartitionExports(worker string, consumers []githubPartitionExport, owners map[string]string) []githubPartitionExport {
	var exports []githubPartitionExport
	for _, consumer := range consumers {
		var members []string
		for _, id := range consumer.Members {
			if owners[id] == worker {
				members = append(members, id)
			}
		}
		if len(members) != 0 {
			exports = append(exports, githubPartitionExport{Name: consumer.Name, Members: members, Statuses: consumer.Statuses})
		}
	}
	return exports
}

// githubPartitionResultsFragment renders comma-joined JSON members without
// braces; the entry wraps one or more fragments into the original report.
func githubPartitionResultsFragment(ids []string) string {
	var fields, args []string
	for n, id := range ids {
		fields = append(fields, fmt.Sprintf(`"%s":{%d}`, id, n))
		args = append(args, "toJSON(needs['"+id+"'])")
	}
	return "format('" + strings.Join(fields, ",") + "', " + strings.Join(args, ", ") + ")"
}

func githubPartitionStatusesFragment(ids []string) string {
	var fields, args []string
	for n, id := range ids {
		fields = append(fields, fmt.Sprintf("{%d}", n))
		args = append(args, "needs['"+id+"'].result")
	}
	return "format('" + strings.Join(fields, " ") + "', " + strings.Join(args, ", ") + ")"
}

// githubPartitionForwarded groups ids by owner in first-use order: inline jobs
// forward themselves, while each worker forwards one fragment of its members.
func githubPartitionForwarded(ids []string, owners map[string]string) ([]string, map[string][]string) {
	var order []string
	members := map[string][]string{}
	for _, id := range ids {
		owner := owners[id]
		if members[owner] == nil {
			order = append(order, owner)
		}
		members[owner] = append(members[owner], id)
	}
	return order, members
}

// A worker that never reported yields the same null results and empty statuses
// as the per-member projection it replaces, so outcome accounting is unchanged.
func githubPartitionResultsExpression(consumer string, ids []string, owners map[string]string) string {
	if len(ids) == 0 {
		return "'{}'"
	}
	var fields, args []string
	order, members := githubPartitionForwarded(ids, owners)
	for _, owner := range order {
		if owners[owner] == owner {
			fields = append(fields, fmt.Sprintf(`"%s":{%d}`, owner, len(args)))
			args = append(args, "toJSON(needs['"+owner+"'])")
			continue
		}
		var missing []string
		for _, id := range members[owner] {
			missing = append(missing, `"`+id+`":null`)
		}
		fields = append(fields, fmt.Sprintf("{%d}", len(args)))
		args = append(args, "(needs['"+owner+"'].outputs['"+consumer+"'] || '"+strings.Join(missing, ",")+"')")
	}
	return "format('{{" + strings.Join(fields, ",") + "}}', " + strings.Join(args, ", ") + ")"
}

func githubPartitionStatusesExpression(consumer string, ids []string, owners map[string]string) string {
	var fields, args []string
	order, members := githubPartitionForwarded(ids, owners)
	for _, owner := range order {
		fields = append(fields, fmt.Sprintf("{%d}", len(args)))
		if owners[owner] == owner {
			args = append(args, "needs['"+owner+"'].result")
		} else {
			args = append(args, "(needs['"+owner+"'].outputs['"+consumer+"-statuses'] || '"+strings.Repeat(" ", len(members[owner])-1)+"')")
		}
	}
	return "format('" + strings.Join(fields, " ") + "', " + strings.Join(args, ", ") + ")"
}

func rewriteGitHubPartitionResults(node *yamlv3.Node, consumer string, ids []string, owners map[string]string) {
	if node.Kind == yamlv3.ScalarNode {
		node.Value = strings.ReplaceAll(node.Value, "toJSON(needs)", githubPartitionResultsExpression(consumer, ids, owners))
		node.Value = strings.ReplaceAll(node.Value, "join(needs.*.result, ' ')", githubPartitionStatusesExpression(consumer, ids, owners))
	}
	for _, child := range node.Content {
		rewriteGitHubPartitionResults(child, consumer, ids, owners)
	}
}

// githubYAMLHasKey reports whether any mapping beneath node declares key.
func githubYAMLHasKey(node *yamlv3.Node, key string) bool {
	if node == nil {
		return false
	}
	if node.Kind == yamlv3.MappingNode {
		for n := 0; n+1 < len(node.Content); n += 2 {
			if node.Content[n].Value == key {
				return true
			}
		}
	}
	for _, child := range node.Content {
		if githubYAMLHasKey(child, key) {
			return true
		}
	}
	return node.Kind == yamlv3.AliasNode && githubYAMLHasKey(node.Alias, key)
}
