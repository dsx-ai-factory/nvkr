// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strconv"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// A DAG has a unique transitive reduction. Compute ancestor sets once, then
// remove only an edge already supplied by another direct dependency. Conditions,
// execution bodies and all original ancestor relationships remain unchanged.
func reduceExecutionDependencies(ctx context.Context, full map[string][]string) (map[string][]string, error) {
	if err := executionContext(ctx); err != nil {
		return nil, err
	}
	ordered := sortedKeys(full)
	dependencies := make(map[string][]string, len(full))
	for _, id := range ordered {
		if err := executionContext(ctx); err != nil {
			return nil, err
		}
		if id == "" {
			return nil, e.New(e.CodeInvalidCatalog, "execution graph has an empty job")
		}
		dependencies[id] = uniqueSorted(full[id])
		if len(dependencies[id]) != len(full[id]) {
			return nil, e.New(e.CodeInvalidCatalog, "execution operation %q repeats a dependency", id)
		}
		for _, parent := range dependencies[id] {
			if _, exists := full[parent]; !exists || parent == id {
				return nil, e.New(e.CodeInvalidCatalog, "execution operation %q has an unknown or self dependency", id)
			}
		}
	}
	ancestors := make(map[string]map[string]bool, len(full))
	visiting := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if err := executionContext(ctx); err != nil {
			return err
		}
		if _, done := ancestors[id]; done {
			return nil
		}
		if visiting[id] {
			return e.New(e.CodeInvalidCatalog, "execution graph contains a cycle")
		}
		visiting[id] = true
		parents := map[string]bool{}
		for _, parent := range dependencies[id] {
			if err := visit(parent); err != nil {
				return err
			}
			parents[parent] = true
			for ancestor := range ancestors[parent] {
				if err := executionContext(ctx); err != nil {
					return err
				}
				parents[ancestor] = true
			}
		}
		delete(visiting, id)
		ancestors[id] = parents
		return nil
	}
	for _, id := range ordered {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	reduced := make(map[string][]string, len(full))
	for _, id := range ordered {
		reduced[id] = nil
		for _, parent := range dependencies[id] {
			if err := executionContext(ctx); err != nil {
				return nil, err
			}
			redundant := false
			for _, other := range dependencies[id] {
				if other != parent && ancestors[other][parent] {
					redundant = true
					break
				}
			}
			if !redundant {
				reduced[id] = append(reduced[id], parent)
			}
		}
	}
	return reduced, executionContext(ctx)
}

func executionContext(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "execution projection requires context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "execution projection canceled")
	}
	return nil
}

// A partition schedules jobs from one dependency depth. It does not replace
// their individual result slots or grant artifact acceptance.
type executionPartition struct {
	Jobs []string
}

// executionPartitioning shapes provider scheduling only. Bounds are declared
// per-operation time limits; they order work, never membership or identity.
type executionPartitioning struct {
	Bounds  map[string]int
	Pinned  map[string]bool // operations the adapter runs outside every partition
	MaxJobs int
	Target  int // partition count up to which layers split where bounds diverge
}

// Members of one depth are independent and every edge increases depth, so any
// split of a layer remains an exact, acyclic cover. Splitting where the bounded
// finish times diverge stops short work from waiting on long siblings.
func partitionExecutionGraph(ctx context.Context, dependencies map[string][]string, options executionPartitioning) ([]executionPartition, error) {
	if options.MaxJobs < 1 {
		return nil, e.New(e.CodeEngineCapability, "execution partition requires a positive job capacity")
	}
	// Reuse the complete provider-DAG admission, including cancellation and cycles.
	if _, err := reduceExecutionDependencies(ctx, dependencies); err != nil {
		return nil, err
	}
	for id := range dependencies {
		if options.Bounds[id] < 1 {
			return nil, e.New(e.CodeInvalidCatalog, "execution operation %q has no positive time bound", id)
		}
	}
	for id := range options.Pinned {
		if _, known := dependencies[id]; !known {
			return nil, e.New(e.CodeInvalidCatalog, "pinned execution operation %q is unknown", id)
		}
	}
	depths := make(map[string]int, len(dependencies))
	finish := make(map[string]int, len(dependencies))
	var depth func(string) (int, error)
	depth = func(id string) (int, error) {
		if err := executionContext(ctx); err != nil {
			return 0, err
		}
		if value, ok := depths[id]; ok {
			return value, nil
		}
		value, start := 0, 0
		for _, parent := range dependencies[id] {
			parentDepth, err := depth(parent)
			if err != nil {
				return 0, err
			}
			value, start = max(value, parentDepth+1), max(start, finish[parent])
		}
		depths[id], finish[id] = value, start+options.Bounds[id]
		return value, nil
	}
	layers := map[int][]string{}
	for _, id := range sortedKeys(dependencies) {
		value, err := depth(id)
		if err != nil {
			return nil, err
		}
		if !options.Pinned[id] {
			layers[value] = append(layers[value], id)
		}
	}
	var groups [][]string
	for _, level := range slices.Sorted(maps.Keys(layers)) {
		jobs := layers[level]
		slices.SortStableFunc(jobs, func(a, b string) int { return cmp.Compare(finish[a], finish[b]) })
		for chunk := range slices.Chunk(jobs, options.MaxJobs) {
			groups = append(groups, chunk)
		}
	}
	// Split the widest bounded-finish gap first; ties keep the earliest group and cut.
	for len(groups) < options.Target {
		if err := executionContext(ctx); err != nil {
			return nil, err
		}
		group, cut, gap := -1, 0, 0
		for n, jobs := range groups {
			for i := 1; i < len(jobs); i++ {
				if delta := finish[jobs[i]] - finish[jobs[i-1]]; delta > gap {
					group, cut, gap = n, i, delta
				}
			}
		}
		if group < 0 {
			break
		}
		jobs := groups[group]
		groups = slices.Insert(groups, group+1, jobs[cut:])
		groups[group] = jobs[:cut]
	}
	partitions := make([]executionPartition, 0, len(groups))
	for _, jobs := range groups {
		partitions = append(partitions, executionPartition{Jobs: slices.Clone(jobs)})
	}
	return partitions, nil
}

// executionCondition describes scheduling, never artifact acceptance.
type executionCondition uint8

const (
	executionAfterSuccess executionCondition = iota
	executionAfterCompletion
)

type executionOperation struct {
	Needs     []string
	Condition executionCondition
}

// executionGraph is derived from admitted operations, not from provider output.
// Its private copy prevents adapter rewrites from changing the admitted graph.
type executionGraph struct{ operations map[string]executionOperation }

func newExecutionGraph(ctx context.Context, operations map[string]executionOperation) (*executionGraph, error) {
	dependencies := make(map[string][]string, len(operations))
	owned := make(map[string]executionOperation, len(operations))
	for id, operation := range operations {
		if operation.Condition != executionAfterSuccess && operation.Condition != executionAfterCompletion {
			return nil, e.New(e.CodeEngineCapability, "execution operation %q has an unsupported condition", id)
		}
		operation.Needs = slices.Clone(operation.Needs)
		owned[id] = operation
		dependencies[id] = operation.Needs
	}
	if _, err := reduceExecutionDependencies(ctx, dependencies); err != nil {
		return nil, err
	}
	return &executionGraph{operations: owned}, nil
}

// prerequisiteResults carries failures through recovery operations. A successful
// recovery does not authorize ordinary descendants of its failed ancestors.
func (graph *executionGraph) prerequisiteResults(ctx context.Context, id string) ([]string, error) {
	if err := executionContext(ctx); err != nil {
		return nil, err
	}
	operation, ok := graph.operations[id]
	if !ok {
		return nil, e.New(e.CodeInvalidCatalog, "unknown execution operation %q", id)
	}
	parents := slices.Clone(operation.Needs)
	if operation.Condition == executionAfterCompletion {
		return uniqueSorted(parents), nil
	}
	seen := map[string]bool{}
	for i := 0; i < len(parents); i++ {
		if err := executionContext(ctx); err != nil {
			return nil, err
		}
		parent := parents[i]
		if seen[parent] {
			continue
		}
		seen[parent] = true
		if graph.operations[parent].Condition == executionAfterCompletion {
			parents = append(parents, graph.operations[parent].Needs...)
		}
	}
	return uniqueSorted(parents), nil
}

func (graph *executionGraph) dependencies(ctx context.Context) (map[string][]string, error) {
	dependencies := make(map[string][]string, len(graph.operations))
	for _, id := range sortedKeys(graph.operations) {
		parents, err := graph.prerequisiteResults(ctx, id)
		if err != nil {
			return nil, err
		}
		dependencies[id] = parents
	}
	return dependencies, nil
}

// Validate the final layout after adapter constraints move operations inline.
// Each group executes independent operations; contracting groups must stay acyclic.
func (graph *executionGraph) validatePartitions(ctx context.Context, groups [][]string) error {
	dependencies, err := graph.dependencies(ctx)
	if err != nil {
		return err
	}
	owners := make(map[string]string, len(dependencies))
	contracted := make(map[string][]string, len(groups))
	for index, members := range groups {
		if len(members) == 0 {
			return e.New(e.CodeInvalidCatalog, "execution partition is empty")
		}
		owner := strconv.Itoa(index)
		contracted[owner] = nil
		for _, id := range members {
			if _, known := dependencies[id]; !known {
				return e.New(e.CodeInvalidCatalog, "execution partition contains an unknown operation")
			}
			if _, repeated := owners[id]; repeated {
				return e.New(e.CodeInvalidCatalog, "execution partitions repeat an operation")
			}
			owners[id] = owner
		}
	}
	if len(owners) != len(dependencies) {
		return e.New(e.CodeInvalidCatalog, "execution partitions omit a required operation")
	}
	for id, parents := range dependencies {
		for _, parent := range parents {
			if owners[id] == owners[parent] {
				return e.New(e.CodeInvalidCatalog, "execution partition contains dependent operations")
			}
			contracted[owners[id]] = append(contracted[owners[id]], owners[parent])
		}
	}
	for owner, parents := range contracted {
		contracted[owner] = uniqueSorted(parents)
	}
	_, err = reduceExecutionDependencies(ctx, contracted)
	return err
}

// validateExecutionMembers accepts an exact disjoint cover, not aggregate success.
func validateExecutionMembers(expected []string, groups map[string][]string) error {
	unclaimed := make(map[string]bool, len(expected))
	for _, id := range expected {
		if id == "" || unclaimed[id] {
			return e.New(e.CodeInvalidCatalog, "execution membership contains an empty or duplicate operation")
		}
		unclaimed[id] = true
	}
	claimed := 0
	for id, members := range groups {
		if _, collision := unclaimed[id]; id == "" || collision || len(members) == 0 {
			return e.New(e.CodeInvalidCatalog, "execution groups require distinct names and nonempty membership")
		}
		for _, member := range members {
			if !unclaimed[member] {
				return e.New(e.CodeInvalidCatalog, "execution groups contain an unknown or repeated operation")
			}
			unclaimed[member] = false
			claimed++
		}
	}
	if claimed != len(expected) {
		return e.New(e.CodeInvalidCatalog, "execution groups omit a required operation")
	}
	return nil
}

// Results describe observed execution. Even a complete successful set grants no
// artifact or release authority without the owning evidence checks.
type executionOutcome uint8

const (
	executionSucceeded executionOutcome = iota + 1
	executionFailed
	executionCanceled
	executionSkipped
)

type executionResult struct {
	Operation string
	Outcome   executionOutcome
}

func accountExecutionResults(ctx context.Context, expected []string, results []executionResult) (map[string]executionOutcome, error) {
	if err := executionContext(ctx); err != nil {
		return nil, err
	}
	allowed := map[string]bool{}
	for _, id := range expected {
		if id == "" || allowed[id] {
			return nil, e.New(e.CodeInvalidCatalog, "execution results require unique nonempty expected operations")
		}
		allowed[id] = true
	}
	accounted := make(map[string]executionOutcome, len(expected))
	for _, result := range results {
		if err := executionContext(ctx); err != nil {
			return nil, err
		}
		if !allowed[result.Operation] || accounted[result.Operation] != 0 {
			return nil, e.New(e.CodeInvalidCatalog, "execution results contain an unknown or duplicate operation")
		}
		if result.Outcome < executionSucceeded || result.Outcome > executionSkipped {
			return nil, e.New(e.CodeInvalidCatalog, "execution result has an unknown outcome")
		}
		accounted[result.Operation] = result.Outcome
	}
	if len(accounted) != len(expected) {
		return nil, e.New(e.CodeInvalidCatalog, "execution results omit a required operation")
	}
	return accounted, nil
}
