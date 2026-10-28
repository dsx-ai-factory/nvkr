// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// A partition schedules jobs from one dependency depth. It does not replace
// their individual result slots or grant artifact acceptance.
type githubExecutionPartition struct {
	Jobs  []string
	Needs []int
}

func partitionGitHubJobs(ctx context.Context, dependencies map[string][]string, maxJobs int) ([]githubExecutionPartition, error) {
	if maxJobs < 1 {
		return nil, e.New(e.CodeEngineCapability, "GitHub execution partition requires a positive job capacity")
	}
	// Reuse the complete provider-DAG admission, including cancellation and cycles.
	if _, err := reduceGitHubNeeds(ctx, dependencies); err != nil {
		return nil, err
	}
	depths := make(map[string]int, len(dependencies))
	var depth func(string) (int, error)
	depth = func(id string) (int, error) {
		if err := githubNeedsContext(ctx); err != nil {
			return 0, err
		}
		if value, ok := depths[id]; ok {
			return value, nil
		}
		value := 0
		for _, parent := range dependencies[id] {
			parentDepth, err := depth(parent)
			if err != nil {
				return 0, err
			}
			value = max(value, parentDepth+1)
		}
		depths[id] = value
		return value, nil
	}
	layers := map[int][]string{}
	for _, id := range sortedKeys(dependencies) {
		value, err := depth(id)
		if err != nil {
			return nil, err
		}
		layers[value] = append(layers[value], id)
	}
	var partitions []githubExecutionPartition
	owners := make(map[string]int, len(dependencies))
	for level := 0; level < len(layers); level++ {
		for jobs := layers[level]; len(jobs) != 0; {
			if err := githubNeedsContext(ctx); err != nil {
				return nil, err
			}
			n := min(maxJobs, len(jobs))
			partition := githubExecutionPartition{Jobs: slices.Clone(jobs[:n])}
			for _, id := range partition.Jobs {
				owners[id] = len(partitions)
				for _, parent := range dependencies[id] {
					partition.Needs = append(partition.Needs, owners[parent])
				}
			}
			slices.Sort(partition.Needs)
			partition.Needs = slices.Compact(partition.Needs)
			partitions = append(partitions, partition)
			jobs = jobs[n:]
		}
	}
	return partitions, nil
}
