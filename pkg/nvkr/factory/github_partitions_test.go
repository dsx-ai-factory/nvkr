// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"reflect"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func unitExecutionLayout(graph map[string][]string, maxJobs, target int) executionPartitioning {
	bounds := make(map[string]int, len(graph))
	for id := range graph {
		bounds[id] = 1
	}
	return executionPartitioning{Bounds: bounds, MaxJobs: maxJobs, Target: target}
}

func TestGitHubExecutionPartitions(t *testing.T) {
	split := map[string][]string{"a": nil, "b": nil, "c": nil, "d": {"a"}}
	bounded := unitExecutionLayout(split, 64, 3)
	bounded.Bounds["b"] = 10
	pinned := unitExecutionLayout(map[string][]string{"a": nil, "b": nil, "c": {"b"}}, 64, 0)
	pinned.Pinned = map[string]bool{"b": true}
	for _, test := range []struct {
		name   string
		graph  map[string][]string
		layout executionPartitioning
		want   []githubExecutionPartition
	}{
		{"empty", map[string][]string{}, unitExecutionLayout(nil, 2, 0), []githubExecutionPartition{}},
		{"diamond", map[string][]string{"a": nil, "b": {"a"}, "c": {"a"}, "d": {"b", "c"}},
			unitExecutionLayout(map[string][]string{"a": nil, "b": nil, "c": nil, "d": nil}, 2, 0),
			[]githubExecutionPartition{{Jobs: []string{"a"}}, {Jobs: []string{"b", "c"}}, {Jobs: []string{"d"}}}},
		{"bounded layer", map[string][]string{"a": nil, "b": nil, "c": nil, "d": {"a", "c"}},
			unitExecutionLayout(map[string][]string{"a": nil, "b": nil, "c": nil, "d": nil}, 2, 0),
			[]githubExecutionPartition{{Jobs: []string{"a", "b"}}, {Jobs: []string{"c"}}, {Jobs: []string{"d"}}}},
		{"long edge", map[string][]string{"a": nil, "b": {"a"}, "c": {"a", "b"}},
			unitExecutionLayout(map[string][]string{"a": nil, "b": nil, "c": nil}, 2, 0),
			[]githubExecutionPartition{{Jobs: []string{"a"}}, {Jobs: []string{"b"}}, {Jobs: []string{"c"}}}},
		{"long sibling ordered", split, unitExecutionLayout(split, 64, 0),
			[]githubExecutionPartition{{Jobs: []string{"a", "b", "c"}}, {Jobs: []string{"d"}}}},
		{"long sibling split", split, bounded,
			[]githubExecutionPartition{{Jobs: []string{"a", "c"}}, {Jobs: []string{"b"}}, {Jobs: []string{"d"}}}},
		{"target without bound gap", split, unitExecutionLayout(split, 64, 50),
			[]githubExecutionPartition{{Jobs: []string{"a", "b", "c"}}, {Jobs: []string{"d"}}}},
		{"pinned operation", map[string][]string{"a": nil, "b": nil, "c": {"b"}}, pinned,
			[]githubExecutionPartition{{Jobs: []string{"a"}}, {Jobs: []string{"c"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := githubNeedsClone(test.graph)
			got, err := partitionGitHubJobs(t.Context(), test.graph, test.layout)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("partitions=%v want=%v error=%v", got, test.want, err)
			}
			if !reflect.DeepEqual(before, test.graph) {
				t.Fatal("partitioning mutated dependency graph")
			}
		})
	}
}

func TestGitHubExecutionPartitionsRejectInvalidGraph(t *testing.T) {
	for _, graph := range []map[string][]string{
		{"a": {"missing"}}, {"a": {"a"}}, {"a": {"b"}, "b": {"a"}}, {"a": nil, "b": {"a", "a"}}, {"": nil},
	} {
		_, err := partitionGitHubJobs(t.Context(), graph, unitExecutionLayout(graph, 64, 0))
		assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
	}
	_, err := partitionGitHubJobs(t.Context(), nil, unitExecutionLayout(nil, 0, 0))
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	graph := map[string][]string{"a": nil, "b": {"a"}}
	unbounded := unitExecutionLayout(graph, 64, 0)
	delete(unbounded.Bounds, "b")
	_, err = partitionGitHubJobs(t.Context(), graph, unbounded)
	assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
	unknown := unitExecutionLayout(graph, 64, 0)
	unknown.Pinned = map[string]bool{"missing": true}
	_, err = partitionGitHubJobs(t.Context(), graph, unknown)
	assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = partitionGitHubJobs(ctx, map[string][]string{"a": nil}, unitExecutionLayout(map[string][]string{"a": nil}, 64, 0))
	assertFactoryErrorCode(t, err, e.CodeTimeout)
}
