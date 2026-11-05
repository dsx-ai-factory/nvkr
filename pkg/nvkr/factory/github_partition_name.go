// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"fmt"
	"strings"

	yamlv3 "go.yaml.in/yaml/v3"
)

// githubPartitionName summarizes an admitted, nonempty partition using its first
// job in ID order. The count is additional jobs, not additional distinct outputs:
// partitions can mix build, acquisition and publication operations.
func githubPartitionName(index int, partition githubExecutionPartition, jobs *yamlv3.Node) string {
	ids := uniqueSorted(partition.Jobs)
	label := githubYAMLValue(githubYAMLField(jobs, ids[0]), "name")
	if label == "" {
		label = ids[0]
	}
	label = githubPartitionJobSummary(label)
	prefix, suffix := fmt.Sprintf("p%02d ", index), ""
	if len(ids) > 1 {
		suffix = fmt.Sprintf(" +%d", len(ids)-1)
	}
	// Preserve the end of long labels, where platform qualifiers usually live.
	const maxName = 60
	budget := maxName - len(prefix) - len(suffix)
	runes := []rune(label)
	if len(runes) > budget {
		const tail = 16
		label = string(runes[:budget-tail-3]) + "..." + string(runes[len(runes)-tail:])
	}
	return prefix + label + suffix
}

func githubPartitionJobSummary(label string) string {
	if strings.HasPrefix(label, "Acquire pinned ") {
		output, _, _ := strings.Cut(strings.TrimPrefix(label, "Acquire pinned "), "/")
		return "Acquire " + output
	}
	for _, replacement := range [][2]string{
		{"Build and package producer evidence for ", "Build "},
		{"Fetch pinned source for ", "Fetch "},
		{"Verify artifacts and generate SBOMs: ", "Verify "},
		{"Verify release and deliverable signatures: output-", "Verify release "},
		{"Package publication bundle: publication-", "Package "},
		{"Publish output-", "Publish "},
	} {
		if strings.HasPrefix(label, replacement[0]) {
			label = replacement[1] + strings.TrimPrefix(label, replacement[0])
			break
		}
	}
	// Build/fetch labels carry the output slot in parentheses after the producer.
	for _, verb := range []string{"Build ", "Fetch "} {
		if start := strings.LastIndex(label, " ("); strings.HasPrefix(label, verb) && start >= 0 && strings.HasSuffix(label, ")") {
			return verb + label[start+2:len(label)-1]
		}
	}
	return label
}
