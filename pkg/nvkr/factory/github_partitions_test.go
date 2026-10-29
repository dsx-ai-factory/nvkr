// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"reflect"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubExecutionPartitions(t *testing.T) {
	for _, test := range []struct {
		name     string
		graph    map[string][]string
		capacity int
		want     []githubExecutionPartition
	}{
		{"empty", map[string][]string{}, 2, nil},
		{"diamond", map[string][]string{"a": nil, "b": {"a"}, "c": {"a"}, "d": {"b", "c"}}, 2,
			[]githubExecutionPartition{{Jobs: []string{"a"}}, {Jobs: []string{"b", "c"}, Needs: []int{0}}, {Jobs: []string{"d"}, Needs: []int{1}}}},
		{"bounded layer", map[string][]string{"a": nil, "b": nil, "c": nil, "d": {"a", "c"}}, 2,
			[]githubExecutionPartition{{Jobs: []string{"a", "b"}}, {Jobs: []string{"c"}}, {Jobs: []string{"d"}, Needs: []int{0, 1}}}},
		{"long edge", map[string][]string{"a": nil, "b": {"a"}, "c": {"a", "b"}}, 2,
			[]githubExecutionPartition{{Jobs: []string{"a"}}, {Jobs: []string{"b"}, Needs: []int{0}}, {Jobs: []string{"c"}, Needs: []int{0, 1}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := githubNeedsClone(test.graph)
			got, err := partitionGitHubJobs(t.Context(), test.graph, test.capacity)
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
		_, err := partitionGitHubJobs(t.Context(), graph, 64)
		assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
	}
	_, err := partitionGitHubJobs(t.Context(), nil, 0)
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = partitionGitHubJobs(ctx, map[string][]string{"a": nil}, 64)
	assertFactoryErrorCode(t, err, e.CodeTimeout)
}
