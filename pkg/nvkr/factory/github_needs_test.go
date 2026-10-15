// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	yamlv3 "go.yaml.in/yaml/v3"
	"sigs.k8s.io/yaml"
)

func TestGitHubNeedsReductionPreservesCompleteReachability(t *testing.T) {
	for _, test := range []struct {
		name string
		full map[string][]string
		want map[string][]string
	}{
		{name: "diamond", full: map[string][]string{"bootstrap": nil, "a": {"bootstrap"}, "b": {"bootstrap"}, "c": {"bootstrap", "a", "b"}},
			want: map[string][]string{"bootstrap": nil, "a": {"bootstrap"}, "b": {"bootstrap"}, "c": {"a", "b"}}},
		{name: "chain and independent component", full: map[string][]string{"a": nil, "b": {"a"}, "c": {"b", "a"}, "d": {"a", "b", "c"}, "x": nil, "y": {"x"}},
			want: map[string][]string{"a": nil, "b": {"a"}, "c": {"b"}, "d": {"c"}, "x": nil, "y": {"x"}}},
		{name: "already reduced", full: map[string][]string{"a": nil, "b": {"a"}, "c": {"a"}},
			want: map[string][]string{"a": nil, "b": {"a"}, "c": {"a"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := githubNeedsClone(test.full)
			got, err := reduceGitHubNeeds(t.Context(), test.full)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("reduction=%v want=%v error=%v", got, test.want, err)
			}
			if !reflect.DeepEqual(test.full, before) {
				t.Fatal("reduction mutated its original dependency map")
			}
			assertGitHubNeedsReachability(t, test.full, got)
			again, err := reduceGitHubNeeds(t.Context(), got)
			if err != nil || !reflect.DeepEqual(got, again) {
				t.Fatalf("reduction is not idempotent: %v", err)
			}
		})
	}
	// A dense deterministic DAG covers overlapping alternate paths. Comparing
	// independent graph walks checks all ancestors, not just each removed edge.
	full := map[string][]string{}
	for index := range 80 {
		id := fmt.Sprintf("job-%02d", index)
		full[id] = nil
		for parent := 0; parent < index; parent++ {
			if (index+parent)%3 == 0 || parent+1 == index {
				full[id] = append(full[id], fmt.Sprintf("job-%02d", parent))
			}
		}
	}
	reduced, err := reduceGitHubNeeds(t.Context(), full)
	if err != nil {
		t.Fatal(err)
	}
	assertGitHubNeedsReachability(t, full, reduced)
}

func githubNeedsClone(input map[string][]string) map[string][]string {
	out := map[string][]string{}
	for id, values := range input {
		out[id] = slices.Clone(values)
	}
	return out
}

func assertGitHubNeedsReachability(t *testing.T, full, reduced map[string][]string) {
	t.Helper()
	ancestors := func(graph map[string][]string, id string) map[string]bool {
		seen := map[string]bool{}
		pending := slices.Clone(graph[id])
		for len(pending) > 0 {
			parent := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if !seen[parent] {
				seen[parent] = true
				pending = append(pending, graph[parent]...)
			}
		}
		return seen
	}
	for id := range full {
		if !maps.Equal(ancestors(full, id), ancestors(reduced, id)) {
			t.Fatalf("reduction changed ancestors of %s", id)
		}
	}
}

func TestGitHubNeedsRejectsInvalidGraphsAndContexts(t *testing.T) {
	for _, test := range []struct {
		name string
		full map[string][]string
	}{
		{name: "empty job", full: map[string][]string{"": nil}},
		{name: "unknown dependency", full: map[string][]string{"a": {"missing"}}},
		{name: "self dependency", full: map[string][]string{"a": {"a"}}},
		{name: "duplicate dependency", full: map[string][]string{"a": nil, "b": {"a", "a"}}},
		{name: "cycle", full: map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"a"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := reduceGitHubNeeds(t.Context(), test.full)
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			if got != nil {
				t.Fatal("invalid graph returned a partial projection")
			}
		})
	}
	_, err := reduceGitHubNeeds(nil, nil) //nolint:staticcheck // Intentional nil-context admission test.
	assertFactoryErrorCode(t, err, e.CodeUsage)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = reduceGitHubNeeds(ctx, map[string][]string{"a": nil})
	assertFactoryErrorCode(t, err, e.CodeTimeout)
	_, err = githubNeedsForPlan(t.Context(), nil)
	assertFactoryErrorCode(t, err, e.CodeUsage)
}

func githubResultPartitionFixture(count int) map[string][]string {
	full := map[string][]string{}
	for index := range count {
		id := fmt.Sprintf("stage-%04d", index)
		full[id] = nil
		full[githubFinalizerJobID] = append(full[githubFinalizerJobID], id)
	}
	return full
}

func TestGitHubResultCollectorsHaveStableExactMembership(t *testing.T) {
	for _, count := range []int{64, 65, 596} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			full := githubResultPartitionFixture(count)
			projection, err := projectGitHubNeeds(t.Context(), full, false)
			if err != nil {
				t.Fatal(err)
			}
			if count == 64 {
				if len(projection.Collectors) != 0 || !slices.Equal(projection.Reduced[githubFinalizerJobID], full[githubFinalizerJobID]) {
					t.Fatal("small finalizer changed its direct provider-result interface")
				}
				return
			}
			seen := map[string]bool{}
			for id, members := range projection.Collectors {
				if len(members) > 64 || len(members) == 0 || !slices.IsSorted(members) || !slices.Equal(projection.Reduced[id], members) {
					t.Fatalf("collector %s has an invalid bounded dependency set", id)
				}
				for _, member := range members {
					if seen[member] || githubResultCollectorID(member) != id {
						t.Fatalf("collector duplicates or reroutes %s", member)
					}
					seen[member] = true
				}
			}
			if len(seen) != count || !slices.Equal(projection.Reduced[githubFinalizerJobID], sortedKeys(projection.Collectors)) {
				t.Fatal("collectors do not cover the exact original finalizer set")
			}
			added := githubNeedsClone(full)
			newID := "independent-new-output"
			added[newID] = nil
			added[githubFinalizerJobID] = append(added[githubFinalizerJobID], newID)
			after, err := projectGitHubNeeds(t.Context(), added, false)
			if err != nil {
				t.Fatal(err)
			}
			for id, members := range projection.Collectors {
				want := slices.Clone(members)
				if id == githubResultCollectorID(newID) {
					want = append(want, newID)
					slices.Sort(want)
				}
				if !slices.Equal(want, after.Collectors[id]) {
					t.Fatalf("independent output changed unrelated membership in %s", id)
				}
			}
			for id, needs := range projection.Reduced {
				if id != githubFinalizerJobID && projection.Collectors[id] == nil && !slices.Equal(needs, after.Reduced[id]) {
					t.Fatalf("independent output changed existing scheduling for %s", id)
				}
			}
		})
	}
}

func TestGitHubResultCollectorCapacityAndCollisionFailClosed(t *testing.T) {
	for _, test := range []struct {
		name string
		full func() map[string][]string
	}{
		{name: "fixed bucket overflow", full: func() map[string][]string {
			full := map[string][]string{}
			for index := 0; len(full[githubFinalizerJobID]) < 65; index++ {
				id := fmt.Sprintf("same-bucket-%d", index)
				if githubResultCollectorID(id) == "nvkr-results-00" {
					full[id] = nil
					full[githubFinalizerJobID] = append(full[githubFinalizerJobID], id)
				}
			}
			return full
		}},
		{name: "original job collision", full: func() map[string][]string {
			full := githubResultPartitionFixture(65)
			id := githubResultCollectorID(full[githubFinalizerJobID][0])
			full[id] = nil
			full[githubFinalizerJobID] = append(full[githubFinalizerJobID], id)
			return full
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := projectGitHubNeeds(t.Context(), test.full(), false)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if got != nil {
				t.Fatal("invalid collector set returned a partial projection")
			}
		})
	}
}

func TestGitHubNeedsRetainsAuthoredDependencyContext(t *testing.T) {
	plan := githubTestPlan(t)
	original, err := githubNeedsForPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	changed := false
	for id, needs := range original.Full {
		changed = changed || !slices.Equal(needs, original.Reduced[id])
	}
	if !changed {
		t.Fatal("fixture does not exercise redundant dependencies")
	}
	for _, test := range []struct {
		name string
		edit func(*Plan)
	}{
		{name: "whole context", edit: func(plan *Plan) { plan.Environment.Runtime.Setup = []string{"echo '${{ toJSON(needs) }}'"} }},
		{name: "bracket access", edit: func(plan *Plan) { plan.Environment.Runtime.Setup = []string{"echo '${{ needs[\"job\"].result }}'"} }},
		{name: "case insensitive", edit: func(plan *Plan) { plan.Environment.Runtime.Setup = []string{"echo '${{ NEEDS.job.result }}'"} }},
		{name: "environment context", edit: func(plan *Plan) {
			plan.Environment.Runtime.Variables = map[string]string{"RESULT": "${{ needs.job.result }}"}
		}},
		{name: "container context", edit: func(plan *Plan) {
			plan.Environment.Runtime.RuntimeImage = "${{ needs.job.outputs.image }}@sha256:pin"
		}},
		{name: "service context", edit: func(plan *Plan) {
			plan.Environment.Runtime.Services = []string{"${{ needs.job.outputs.image }}@sha256:pin"}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			copy := *plan
			test.edit(&copy)
			projection, err := githubNeedsForPlan(t.Context(), &copy)
			if err != nil {
				t.Fatal(err)
			}
			for id, needs := range projection.Full {
				if !slices.Equal(needs, projection.Reduced[id]) {
					t.Fatalf("authored needs context changed for %s", id)
				}
			}
		})
	}
}

func githubNeedsWorkflowFixture(t *testing.T, projection *githubNeedsProjection) []byte {
	t.Helper()
	jobs := githubYAMLMap()
	steps := &yamlv3.Node{Kind: yamlv3.SequenceNode, Tag: "!!seq", Anchor: "original-steps", Content: []*yamlv3.Node{
		githubYAMLMap("name", githubYAMLString("Keep original body"), "run", githubYAMLString(":")),
	}}
	for index, id := range sortedKeys(projection.Full) {
		jobSteps := steps
		if index != 0 {
			jobSteps = &yamlv3.Node{Kind: yamlv3.AliasNode, Value: "original-steps", Alias: steps}
		}
		job := githubYAMLMap("name", githubYAMLString(id), "if", githubYAMLString("${{ always() && false }}"), "steps", jobSteps)
		if len(projection.Full[id]) != 0 {
			githubYAMLSet(job, "needs", githubNeedsSequence(projection.Full[id]))
		}
		githubYAMLSet(jobs, id, job)
	}
	root := githubYAMLMap("name", githubYAMLString("Dependency fixture"), "jobs", jobs)
	root.HeadComment = "Keep generated header"
	return githubTestYAMLBytes(t, root)
}

func TestGitHubNeedsLoweringPreservesBodiesAliasesAndExactCollectors(t *testing.T) {
	plan, err := LoadPlanContext(t.Context(), "../../../"+FactoryPlanPath)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := githubNeedsForPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Collectors) == 0 {
		t.Fatal("maintained fixture no longer exercises result collectors")
	}
	raw := githubNeedsWorkflowFixture(t, projection)
	lowered, err := lowerGitHubNeeds(t.Context(), plan, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(lowered, []byte("# Keep generated header")) || bytes.Count(lowered, []byte("*original-steps")) != bytes.Count(raw, []byte("*original-steps")) {
		t.Fatal("dependency lowering changed comments or expanded original step aliases")
	}
	var original, document githubWorkflowDocument
	if err := yaml.UnmarshalStrict(raw, &original); err != nil {
		t.Fatal(err)
	}
	if err := yaml.UnmarshalStrict(lowered, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Jobs) != len(projection.Reduced) {
		t.Fatal("lowering emitted the wrong complete job set")
	}
	for id, before := range original.Jobs {
		after := document.Jobs[id]
		if !slices.Equal(after.Needs, projection.Reduced[id]) {
			t.Fatalf("job %s has incorrect reduced dependencies", id)
		}
		after.Needs = before.Needs
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("lowering changed original execution or condition for %s", id)
		}
	}
	for id, members := range projection.Collectors {
		if !reflect.DeepEqual(document.Jobs[id], githubResultCollector(plan, id, members)) {
			t.Fatalf("collector %s differs from its closed provider-results interface", id)
		}
	}
	again, err := lowerGitHubNeeds(t.Context(), plan, raw)
	if err != nil || !bytes.Equal(lowered, again) {
		t.Fatalf("same-input dependency lowering changed bytes: %v", err)
	}
}

func TestGitHubNeedsLoweringRejectsChangedOriginalGraph(t *testing.T) {
	plan := githubTestPlan(t)
	projection, err := githubNeedsForPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	raw := githubNeedsWorkflowFixture(t, projection)
	var selected string
	for _, id := range sortedKeys(projection.Full) {
		if len(projection.Full[id]) > 0 {
			selected = id
			break
		}
	}
	for _, test := range []struct {
		name string
		edit func(*yamlv3.Node)
	}{
		{name: "missing job", edit: func(jobs *yamlv3.Node) { githubYAMLRemove(jobs, selected) }},
		{name: "additional job", edit: func(jobs *yamlv3.Node) { githubYAMLSet(jobs, "unplanned", githubYAMLMap()) }},
		{name: "removed dependency", edit: func(jobs *yamlv3.Node) { githubYAMLRemove(githubYAMLField(jobs, selected), "needs") }},
		{name: "unknown dependency", edit: func(jobs *yamlv3.Node) {
			githubYAMLSet(githubYAMLField(jobs, selected), "needs", githubNeedsSequence([]string{"unknown"}))
		}},
		{name: "duplicate dependency", edit: func(jobs *yamlv3.Node) {
			value := projection.Full[selected][0]
			githubYAMLSet(githubYAMLField(jobs, selected), "needs", githubNeedsSequence([]string{value, value}))
		}},
		{name: "invalid job", edit: func(jobs *yamlv3.Node) { githubYAMLSet(jobs, selected, githubYAMLString("invalid")) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var document yamlv3.Node
			if err := yamlv3.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			test.edit(githubYAMLField(document.Content[0], "jobs"))
			got, err := lowerGitHubNeeds(t.Context(), plan, githubTestYAMLBytes(t, &document))
			assertFactoryErrorCode(t, err, e.CodePipelineDrift)
			if got != nil {
				t.Fatal("changed graph returned partial workflow bytes")
			}
		})
	}
}
