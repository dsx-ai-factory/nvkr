// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"fmt"
	"reflect"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// The reference traverses the full ancestor graph. The partitioned evaluator
// consumes only transported prerequisite results, including recovery ancestry.
func TestExecutionGraphPartitionSemanticEquivalence(t *testing.T) {
	operations := map[string]executionOperation{
		"source": {}, "independent": {},
		"recovery": {Needs: []string{"source"}, Condition: executionAfterCompletion},
		"consumer": {Needs: []string{"recovery"}},
		"verify":   {Needs: []string{"consumer", "independent"}, Condition: executionAfterCompletion},
	}
	graph, err := newExecutionGraph(t.Context(), operations)
	if err != nil {
		t.Fatal(err)
	}
	dependencies, err := graph.dependencies(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	order := []string{"independent", "source", "recovery", "consumer", "verify"}
	for _, capacity := range []int{1, 2, 64} {
		t.Run(fmt.Sprintf("capacity-%d", capacity), func(t *testing.T) {
			partitions, err := partitionExecutionGraph(t.Context(), dependencies, unitExecutionLayout(dependencies, capacity, 0))
			if err != nil {
				t.Fatal(err)
			}
			for _, canceled := range []bool{false, true} {
				for mask := 0; mask < 1<<(2*len(order)); mask++ {
					injected := map[string]executionOutcome{}
					for i, id := range order {
						injected[id] = executionOutcome(1 + (mask>>(2*i))&3)
					}
					flat := map[string]executionOutcome{}
					var ancestorsSucceeded func(string) bool
					ancestorsSucceeded = func(id string) bool {
						for _, parent := range operations[id].Needs {
							if flat[parent] != executionSucceeded || !ancestorsSucceeded(parent) {
								return false
							}
						}
						return true
					}
					for _, id := range order {
						flat[id] = injected[id]
						if operations[id].Condition == executionAfterSuccess {
							if canceled {
								flat[id] = executionCanceled
							} else if !ancestorsSucceeded(id) {
								flat[id] = executionSkipped
							}
						}
					}
					projected := map[string]executionOutcome{}
					var reports []executionResult
					for _, partition := range partitions {
						for _, id := range partition.Jobs {
							result := injected[id]
							if operations[id].Condition == executionAfterSuccess {
								if canceled {
									result = executionCanceled
								} else {
									for _, parent := range dependencies[id] {
										if projected[parent] != executionSucceeded {
											result = executionSkipped
										}
									}
								}
							}
							projected[id] = result
							reports = append(reports, executionResult{id, result})
						}
					}
					accounted, err := accountExecutionResults(t.Context(), order, reports)
					if err != nil || !reflect.DeepEqual(accounted, flat) {
						t.Fatalf("cancel=%v outcomes=%010b: flat=%v partitioned=%v err=%v", canceled, mask, flat, accounted, err)
					}
				}
			}
		})
	}
}

func TestExecutionResultsRefuseInexactMembership(t *testing.T) {
	for _, test := range []struct {
		name     string
		expected []string
		results  []executionResult
	}{
		{"missing", []string{"a"}, nil},
		{"unknown", []string{"a"}, []executionResult{{"b", executionSucceeded}}},
		{"duplicate", []string{"a"}, []executionResult{{"a", executionSucceeded}, {"a", executionFailed}}},
		{"unknown outcome", []string{"a"}, []executionResult{{"a", 0}}},
		{"duplicate expectation", []string{"a", "a"}, []executionResult{{"a", executionSucceeded}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := accountExecutionResults(t.Context(), test.expected, test.results)
			assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
		})
	}
}

func TestExecutionGraphOwnsConditionsAndDependencies(t *testing.T) {
	operations := map[string]executionOperation{"a": {}, "b": {Needs: []string{"a"}}}
	graph, err := newExecutionGraph(t.Context(), operations)
	if err != nil {
		t.Fatal(err)
	}
	operations["b"].Needs[0] = "unknown"
	parents, err := graph.prerequisiteResults(t.Context(), "b")
	if err != nil || !reflect.DeepEqual(parents, []string{"a"}) {
		t.Fatalf("parents=%v err=%v", parents, err)
	}
	_, err = newExecutionGraph(t.Context(), map[string]executionOperation{"a": {Condition: 99}})
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
}

func TestExecutionPartitionAdmission(t *testing.T) {
	graph, err := newExecutionGraph(t.Context(), map[string]executionOperation{
		"a": {}, "b": {}, "c": {Needs: []string{"a"}}, "d": {Needs: []string{"b"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		groups [][]string
		valid  bool
	}{
		{"layers", [][]string{{"a", "b"}, {"c", "d"}}, true},
		{"inline", [][]string{{"a"}, {"b"}, {"c", "d"}}, true},
		{"missing", [][]string{{"a", "b"}, {"c"}}, false},
		{"duplicate", [][]string{{"a", "b"}, {"c", "d", "a"}}, false},
		{"unknown", [][]string{{"a", "b"}, {"c", "d", "unknown"}}, false},
		{"empty", [][]string{{"a", "b"}, {"c", "d"}, {}}, false},
		{"internal dependency", [][]string{{"a", "c"}, {"b", "d"}}, false},
		{"contracted cycle", [][]string{{"a", "d"}, {"b", "c"}}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := graph.validatePartitions(t.Context(), test.groups)
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeInvalidCatalog)
			}
		})
	}
}
