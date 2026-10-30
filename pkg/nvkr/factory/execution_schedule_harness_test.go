// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"cmp"
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
)

// The scheduling harness measures partition layouts over real maintained
// inputs. Its durations are estimates, not recorded per-job timings; it
// compares layouts and never claims hosted wall time. See
// docs/execution-partition-scheduling.md for the method and finding.

// Estimated minutes per Plan node kind, summed for each GitHub stage. Only the
// disk job and the signing/verification anchors come from recorded runs cited
// in the finding; every other value is an estimate.
var harnessKindMinutes = map[string]float64{
	NodeFetch: 1, NodeAcquire: 2, NodeBuild: 6, NodeVerify: 1, NodeSBOM: 1.5,
	NodePackage: 1, NodeInventory: 1, NodeAttestEvidence: 0.5, NodePublish: 1.5,
	NodeSign: 0.5, NodeVerifyPublication: 0.5, NodeAssembleRelease: 3,
}

const (
	harnessJobOverheadMinutes  = 1.5  // estimated runner pickup, checkout and helper restore
	harnessDiskMinutes         = 43   // recorded native FDE disk job, Run 38443178035
	harnessBootstrapMinutes    = 3    // estimated helper build
	harnessCallOverheadMinutes = 0.5  // estimated reusable-workflow call start
	harnessJitterSeeds         = 8    // deterministic duration perturbations per layout
	harnessClearImprovement    = 0.05 // required worst-case gain on a partitioned Plan
	harnessRegressionTolerance = 0.01 // largest accepted loss on any Plan, capacity or seed
)

type harnessOp struct {
	pool    string
	minutes float64
	inline  bool
}

type harnessGraph struct {
	name        string
	partitioned bool // the Environment enables partitioning
	model       *executionGraph
	layout      executionPartitioning
	deps        map[string][]string
	ops         map[string]harnessOp
}

// Capacity scenarios are assumptions: GitHub limits concurrent hosted jobs per
// account, and native ARC pools have operator-configured maximum runners.
var harnessCapacities = []struct {
	name  string
	pools map[string]int // zero or missing means unbounded
}{
	{"unbounded", nil},
	{"hosted60-arc2", map[string]int{"hosted": 60, "arc-amd64-kvm": 2, "arc-arm64-kvm": 2}},
	{"hosted20-arc1", map[string]int{"hosted": 20, "arc-amd64-kvm": 1, "arc-arm64-kvm": 1}},
}

func loadHarnessGraphs(t *testing.T) []harnessGraph {
	t.Helper()
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	var plans []*Plan
	var names []string
	// Committed Plans are the drift-checked compilations of the NVKE Definitions.
	for _, path := range []string{
		GitHubDevelopmentPlanPath, FactoryPlanPath,
		".github/nvkr-run-nvke-core/plan.json", ".github/nvkr-run-nvke-services/plan.json",
		".github/nvkr-run-nvke-telemetry/plan.json", ".github/nvkr-run-nvke-disks/plan.json",
	} {
		plan, err := LoadPlanContext(t.Context(), path)
		if err != nil {
			t.Fatal(err)
		}
		plans, names = append(plans, plan), append(names, filepath.Base(filepath.Dir(path)))
	}
	// Diagnostics have no committed Plan; compile their hosted selections.
	for _, pair := range [][2]string{
		{"inputs/definitions/diagnostics/fde-amd64.yaml", "inputs/environments/github-diagnostics.yaml"},
		{"inputs/definitions/diagnostics/ovn.yaml", "inputs/environments/github-ovn-diagnostics.yaml"},
		{"inputs/definitions/diagnostics/ubuntu24/definition-github-fde-amd64.yaml", "inputs/environments/github-ubuntu24-amd64.yaml"},
		{"inputs/definitions/diagnostics/ubuntu24/definition-github-image-arm64.yaml", "inputs/environments/github-ubuntu24-arm64.yaml"},
	} {
		definition, err := authored.LoadDefinition(t.Context(), pair[0])
		if err != nil {
			t.Fatal(err)
		}
		environment, err := authored.LoadEnvironment(t.Context(), pair[1])
		if err != nil {
			t.Fatal(err)
		}
		plan, err := CompileDocuments(t.Context(), pair[0], definition, pair[1], environment, "test (deadbeef)")
		if err != nil {
			t.Fatalf("%s: %v", pair[0], err)
		}
		plans, names = append(plans, plan), append(names, strings.TrimSuffix(strings.TrimPrefix(pair[0], "inputs/definitions/"), ".yaml"))
	}
	graphs := make([]harnessGraph, 0, len(plans))
	for n, plan := range plans {
		graphs = append(graphs, harnessGraphFor(t, names[n], plan))
	}
	return graphs
}

func harnessGraphFor(t *testing.T, name string, plan *Plan) harnessGraph {
	t.Helper()
	projection, err := githubNeedsForPlan(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	model, layout, err := githubExecutionModel(t.Context(), plan, projection)
	if err != nil {
		t.Fatal(err)
	}
	deps, err := model.dependencies(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stages, _, err := githubStages(plan)
	if err != nil {
		t.Fatal(err)
	}
	ops := make(map[string]harnessOp, len(deps))
	for _, stage := range stages {
		op := harnessOp{pool: harnessPool(stage.Nodes[0].RunsOn), inline: layout.Pinned[stage.ID], minutes: harnessJobOverheadMinutes}
		if stage.DiskOutput != "" {
			op.minutes = harnessDiskMinutes
		} else {
			for _, node := range stage.Nodes {
				minutes, ok := harnessKindMinutes[node.Kind]
				if !ok {
					t.Fatalf("no duration estimate for node kind %q", node.Kind)
				}
				op.minutes += minutes
			}
		}
		ops[stage.ID] = op
	}
	for _, node := range plan.Graph.Nodes {
		resolved, err := plan.GitHubExecutionFor(node)
		if err != nil {
			t.Fatal(err)
		}
		// Bootstraps for ARC targets run on the hosted runner the workflow renders.
		ops["build-runner-"+shortStableName(resolved.RunsOn)] = harnessOp{pool: harnessPool(githubBootstrapRunner(resolved.RunsOn)), minutes: harnessBootstrapMinutes}
	}
	for id := range deps {
		if _, ok := ops[id]; !ok {
			t.Fatalf("%s: execution operation %q has no harness classification", name, id)
		}
	}
	if len(ops) != len(deps) {
		t.Fatalf("%s: harness classified operations outside the execution graph", name)
	}
	return harnessGraph{name: name, partitioned: plan.Environment.GitHubExecutionPartitions, model: model, layout: layout, deps: deps, ops: ops}
}

func harnessPool(runsOn string) string {
	if strings.HasPrefix(runsOn, "ubuntu-") {
		return "hosted" // one account-wide hosted concurrency limit
	}
	return runsOn
}

// harnessMinutes perturbs each estimate by a deterministic factor in [0.5, 1.5].
func (graph harnessGraph) harnessMinutes(seed int) map[string]float64 {
	minutes := make(map[string]float64, len(graph.ops))
	for id, op := range graph.ops {
		minutes[id] = op.minutes
		if seed != 0 {
			hash := fnv.New64a()
			fmt.Fprintf(hash, "%d/%s", seed, id)
			minutes[id] *= 0.5 + float64(hash.Sum64()%1001)/1000
		}
	}
	return minutes
}

// A layout lists worker partitions; inline operations run as singleton caller
// jobs. Layouts see only the Plan-time estimate, never the perturbed durations
// they are scored against, because the generator cannot observe a future run.
type harnessLayout func(graph harnessGraph, estimate map[string]float64, capacity map[string]int) ([][]string, error)

// harnessDepthLayout freezes the original grouping for comparison: lexical
// dependency-depth layers in chunks of 64, then OIDC signers moved inline.
func harnessDepthLayout(graph harnessGraph, _ map[string]float64, _ map[string]int) ([][]string, error) {
	depths := harnessDepths(graph.deps)
	layers := map[int][]string{}
	for _, id := range sortedKeys(graph.deps) {
		layers[depths[id]] = append(layers[depths[id]], id)
	}
	var groups [][]string
	for level := 0; level < len(layers); level++ {
		for chunk := range slices.Chunk(layers[level], githubMaximumResultInputs) {
			var members []string
			for _, id := range chunk {
				if !graph.ops[id].inline {
					members = append(members, id)
				}
			}
			if len(members) != 0 {
				groups = append(groups, members)
			}
		}
	}
	return groups, nil
}

// harnessAdoptedLayout is the generator's production grouping.
func harnessAdoptedLayout(graph harnessGraph, _ map[string]float64, _ map[string]int) ([][]string, error) {
	partitions, err := partitionGitHubJobs(context.Background(), graph.deps, graph.layout)
	if err != nil {
		return nil, err
	}
	groups := make([][]string, 0, len(partitions))
	for _, partition := range partitions {
		groups = append(groups, partition.Jobs)
	}
	return groups, nil
}

// harnessRefinedLayout splits each dependency-depth layer. Members of one layer
// are mutually independent and every contracted edge increases depth, so any
// refinement stays an exact disjoint, acyclic cover.
type harnessRefinement struct {
	byPool     bool // separate runner pools within a layer
	byFinish   bool // order a layer by estimated job-level finish before chunking
	byCapacity bool // chunk no larger than the pool's concurrent runners
	budget     int  // split at estimated finish gaps up to this many partitions
}

func harnessRefinedLayout(options harnessRefinement) harnessLayout {
	return func(graph harnessGraph, estimate map[string]float64, capacity map[string]int) ([][]string, error) {
		depths := harnessDepths(graph.deps)
		finish := harnessEarliestFinish(graph.deps, estimate)
		type group struct {
			depth   int
			members []string
		}
		buckets := map[string][]string{}
		for _, id := range sortedKeys(graph.deps) {
			if graph.ops[id].inline {
				continue
			}
			key := fmt.Sprintf("%06d", depths[id])
			if options.byPool {
				key += "/" + graph.ops[id].pool
			}
			buckets[key] = append(buckets[key], id)
		}
		var groups []group
		for _, key := range sortedKeys(buckets) {
			members := buckets[key]
			if options.byFinish {
				slices.SortStableFunc(members, func(a, b string) int { return cmp.Compare(finish[a], finish[b]) })
			}
			size := githubMaximumResultInputs
			if limit := capacity[graph.ops[members[0]].pool]; options.byCapacity && limit > 0 {
				size = min(size, limit)
			}
			for chunk := range slices.Chunk(members, size) {
				groups = append(groups, group{depths[chunk[0]], chunk})
			}
		}
		// Spend the remaining provider partition budget at the largest gaps in
		// ideal finish time, so short work stops waiting on long siblings.
		for len(groups) < options.budget {
			best, cut, gap := -1, 0, 0.0
			for n, candidate := range groups {
				for i := 1; i < len(candidate.members); i++ {
					if delta := finish[candidate.members[i]] - finish[candidate.members[i-1]]; delta > gap {
						best, cut, gap = n, i, delta
					}
				}
			}
			if best < 0 {
				break
			}
			split := groups[best]
			groups = slices.Insert(slices.Delete(groups, best, best+1), best,
				group{split.depth, split.members[:cut]}, group{split.depth, split.members[cut:]})
		}
		slices.SortStableFunc(groups, func(a, b group) int { return cmp.Compare(a.depth, b.depth) })
		layout := make([][]string, 0, len(groups))
		for _, candidate := range groups {
			layout = append(layout, candidate.members)
		}
		return layout, nil
	}
}

func harnessDepths(deps map[string][]string) map[string]int {
	depths := make(map[string]int, len(deps))
	var visit func(string) int
	visit = func(id string) int {
		if value, ok := depths[id]; ok {
			return value
		}
		value := 0
		for _, parent := range deps[id] {
			value = max(value, visit(parent)+1)
		}
		depths[id] = value
		return value
	}
	for id := range deps {
		visit(id)
	}
	return depths
}

// harnessEarliestFinish is the job-level, unbounded-capacity finish time.
func harnessEarliestFinish(deps map[string][]string, minutes map[string]float64) map[string]float64 {
	finish := make(map[string]float64, len(deps))
	var visit func(string) float64
	visit = func(id string) float64 {
		if value, ok := finish[id]; ok {
			return value
		}
		start := 0.0
		for _, parent := range deps[id] {
			start = max(start, visit(parent))
		}
		finish[id] = start + minutes[id]
		return finish[id]
	}
	for id := range deps {
		visit(id)
	}
	return finish
}

type harnessResult struct {
	makespan float64
	waitSum  float64 // minutes eligible by data dependencies but not started
	waitMax  float64
}

// harnessSimulate runs execution units in an event loop. A unit is a worker
// partition or a singleton job; a unit starts after every unit that owns one of
// its members' prerequisites completes, and completes when all its members do.
// Released jobs take free runners from their pool in (release, id) order.
func harnessSimulate(graph harnessGraph, units [][]string, overhead []float64, minutes map[string]float64, capacity map[string]int) (harnessResult, error) {
	owner := make(map[string]int, len(graph.deps))
	for unit, members := range units {
		for _, id := range members {
			owner[id] = unit
		}
	}
	needs := make([]map[int]bool, len(units))
	dependents := make([][]int, len(units))
	for unit, members := range units {
		needs[unit] = map[int]bool{}
		for _, id := range members {
			for _, parent := range graph.deps[id] {
				if parentUnit := owner[parent]; !needs[unit][parentUnit] {
					needs[unit][parentUnit] = true
					dependents[parentUnit] = append(dependents[parentUnit], unit)
				}
			}
		}
	}
	type queued struct {
		release float64
		id      string
	}
	var queue []queued
	pending := make([]int, len(units))
	remaining := make([]int, len(units))
	release := func(unit int, at float64) {
		for _, id := range units[unit] {
			queue = append(queue, queued{at + overhead[unit], id})
		}
	}
	for unit := range units {
		pending[unit], remaining[unit] = len(needs[unit]), len(units[unit])
		if pending[unit] == 0 {
			release(unit, 0)
		}
	}
	finish := map[string]float64{}
	start := map[string]float64{}
	busy := map[string]int{}
	running := map[string]float64{}
	now := 0.0
	for len(finish) < len(graph.deps) {
		slices.SortFunc(queue, func(a, b queued) int {
			return cmp.Or(cmp.Compare(a.release, b.release), cmp.Compare(a.id, b.id))
		})
		waiting := queue[:0]
		for _, job := range queue {
			pool := graph.ops[job.id].pool
			if job.release <= now && (capacity[pool] == 0 || busy[pool] < capacity[pool]) {
				busy[pool]++
				start[job.id], running[job.id] = now, now+minutes[job.id]
				continue
			}
			waiting = append(waiting, job)
		}
		queue = waiting
		next := math.Inf(1)
		for _, at := range running {
			next = min(next, at)
		}
		for _, job := range queue {
			if job.release > now {
				next = min(next, job.release)
			}
		}
		if math.IsInf(next, 1) {
			return harnessResult{}, fmt.Errorf("%s: schedule cannot make progress", graph.name)
		}
		now = next
		for _, id := range sortedKeys(running) {
			if running[id] > now {
				continue
			}
			finish[id] = running[id]
			delete(running, id)
			busy[graph.ops[id].pool]--
			unit := owner[id]
			if remaining[unit]--; remaining[unit] == 0 {
				for _, dependent := range dependents[unit] {
					if pending[dependent]--; pending[dependent] == 0 {
						release(dependent, now)
					}
				}
			}
		}
	}
	var result harnessResult
	for id, done := range finish {
		result.makespan = max(result.makespan, done)
		eligible := 0.0
		for _, parent := range graph.deps[id] {
			eligible = max(eligible, finish[parent])
		}
		wait := start[id] - eligible
		result.waitSum += wait
		result.waitMax = max(result.waitMax, wait)
	}
	return result, nil
}

// harnessRun simulates a worker layout with inline singletons in the caller.
func harnessRun(graph harnessGraph, groups [][]string, minutes map[string]float64, capacity map[string]int) (harnessResult, error) {
	units := slices.Clone(groups)
	overhead := make([]float64, len(groups), len(graph.deps))
	for i := range overhead {
		overhead[i] = harnessCallOverheadMinutes
	}
	for _, id := range sortedKeys(graph.ops) {
		if graph.ops[id].inline {
			units, overhead = append(units, []string{id}), append(overhead, 0)
		}
	}
	return harnessSimulate(graph, units, overhead, minutes, capacity)
}

// harnessIdeal is job-level scheduling with no partition barriers.
func harnessIdeal(graph harnessGraph, minutes map[string]float64, capacity map[string]int) (harnessResult, error) {
	var units [][]string
	for _, id := range sortedKeys(graph.deps) {
		units = append(units, []string{id})
	}
	return harnessSimulate(graph, units, make([]float64, len(units)), minutes, capacity)
}

var harnessLayouts = []struct {
	name   string
	layout harnessLayout
}{
	{"depth (original)", harnessDepthLayout},
	{"timeout (adopted)", harnessAdoptedLayout},
	{"finish-ordered", harnessRefinedLayout(harnessRefinement{byFinish: true})},
	{"duration<=30", harnessRefinedLayout(harnessRefinement{byFinish: true, budget: 30})},
	{"duration<=50", harnessRefinedLayout(harnessRefinement{byFinish: true, budget: githubMaximumExecutionPartitions})},
	{"resource-aware", harnessRefinedLayout(harnessRefinement{byPool: true})},
	{"resource+dur<=30", harnessRefinedLayout(harnessRefinement{byPool: true, byFinish: true, budget: 30})},
	{"capacity<=30", harnessRefinedLayout(harnessRefinement{byPool: true, byFinish: true, byCapacity: true, budget: 30})},
}

// harnessAdmit checks the same provider and graph contracts lowerGitHubPartitions enforces.
func harnessAdmit(t *testing.T, graph harnessGraph, groups [][]string) error {
	t.Helper()
	if len(groups) > githubMaximumExecutionPartitions {
		return fmt.Errorf("%d partitions exceed the %d-call limit", len(groups), githubMaximumExecutionPartitions)
	}
	all := slices.Clone(groups)
	for _, group := range groups {
		if len(group) > githubMaximumResultInputs {
			return fmt.Errorf("partition of %d exceeds %d result inputs", len(group), githubMaximumResultInputs)
		}
	}
	for _, id := range sortedKeys(graph.ops) {
		if graph.ops[id].inline {
			all = append(all, []string{id})
		}
	}
	return graph.model.validatePartitions(t.Context(), all)
}

const harnessFinding = "docs/execution-partition-scheduling.md"

func TestExecutionScheduleHarness(t *testing.T) {
	for _, runner := range []string{"arc-amd64-kvm", "arc-arm64-kvm"} {
		if pool := harnessPool(githubBootstrapRunner(runner)); pool != "hosted" {
			t.Fatalf("%s bootstrap modeled in pool %q, want hosted", runner, pool)
		}
	}
	graphs := loadHarnessGraphs(t)
	// Relative makespan change against the original depth layout, over every
	// plan, capacity and duration seed: the best and worst cases decide adoption.
	best, worst := map[string]float64{}, map[string]float64{}
	for _, graph := range graphs {
		t.Run(graph.name, func(t *testing.T) {
			// Simulator self-check: equal durations make every refinement of the
			// depth layers exact, so without call overhead it matches job-level scheduling.
			unit := map[string]float64{}
			for id := range graph.deps {
				unit[id] = 1
			}
			ideal, err := harnessIdeal(graph, unit, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, layout := range []harnessLayout{harnessDepthLayout, harnessAdoptedLayout} {
				groups, err := layout(graph, unit, nil)
				if err != nil {
					t.Fatal(err)
				}
				units := slices.Clone(groups)
				for _, id := range sortedKeys(graph.ops) {
					if graph.ops[id].inline {
						units = append(units, []string{id})
					}
				}
				layered, err := harnessSimulate(graph, units, make([]float64, len(units)), unit, nil)
				if err != nil {
					t.Fatal(err)
				}
				if layered.makespan != ideal.makespan || layered.waitSum != 0 {
					t.Fatalf("unit-duration layered layout should be barrier-free: %+v vs %+v", layered, ideal)
				}
			}
			for _, capacity := range harnessCapacities {
				for _, candidate := range harnessLayouts {
					var costs, waits, makespans, changes []float64
					partitions := 0
					estimate := graph.harnessMinutes(0)
					for seed := 0; seed <= harnessJitterSeeds; seed++ {
						minutes := graph.harnessMinutes(seed)
						groups, err := candidate.layout(graph, estimate, capacity.pools)
						if err != nil {
							t.Fatal(err)
						}
						again, err := candidate.layout(graph, estimate, capacity.pools)
						if err != nil || !slices.EqualFunc(groups, again, slices.Equal) {
							t.Fatalf("%s layout is not deterministic: %v", candidate.name, err)
						}
						if err := harnessAdmit(t, graph, groups); err != nil {
							t.Fatalf("%s layout violates partition contracts: %v", candidate.name, err)
						}
						result, err := harnessRun(graph, groups, minutes, capacity.pools)
						if err != nil {
							t.Fatal(err)
						}
						ideal, err := harnessIdeal(graph, minutes, capacity.pools)
						if err != nil {
							t.Fatal(err)
						}
						currentGroups, err := harnessDepthLayout(graph, estimate, nil)
						if err != nil {
							t.Fatal(err)
						}
						baseline, err := harnessRun(graph, currentGroups, minutes, capacity.pools)
						if err != nil {
							t.Fatal(err)
						}
						partitions = max(partitions, len(groups))
						costs = append(costs, result.makespan-ideal.makespan)
						waits = append(waits, result.waitSum)
						makespans = append(makespans, result.makespan)
						changes = append(changes, (result.makespan-baseline.makespan)/baseline.makespan)
					}
					if _, seen := best[candidate.name]; !seen {
						best[candidate.name], worst[candidate.name] = math.Inf(1), math.Inf(-1)
					}
					best[candidate.name] = min(best[candidate.name], slices.Min(changes))
					worst[candidate.name] = max(worst[candidate.name], slices.Max(changes))
					t.Logf("%-13s %-17s parts %2d  makespan %5.1f..%5.1f  vs ideal +%4.1f..%4.1f  waits %6.1f..%6.1f op-min  vs depth %+5.1f%%..%+5.1f%%",
						capacity.name, candidate.name, partitions, slices.Min(makespans), slices.Max(makespans),
						slices.Min(costs), slices.Max(costs), slices.Min(waits), slices.Max(waits),
						100*slices.Min(changes), 100*slices.Max(changes))
					// The adoption rule is a performance decision over maintained inputs and
					// estimates, so an input edit can fail it without a generator change.
					if candidate.name == "timeout (adopted)" {
						if slices.Max(changes) > harnessRegressionTolerance {
							t.Fatalf("adopted layout regresses %s by %.1f%%; remeasure and review the decision in %s before changing thresholds",
								capacity.name, 100*slices.Max(changes), harnessFinding)
						}
						if graph.partitioned && slices.Max(changes) > -harnessClearImprovement {
							t.Fatalf("adopted layout gains less than %.0f%% under %s; remeasure and review the decision in %s before changing thresholds",
								100*harnessClearImprovement, capacity.name, harnessFinding)
						}
					}
				}
			}
		})
	}
	for _, candidate := range harnessLayouts[1:] {
		t.Logf("%-17s makespan change vs depth: best %+.1f%%, worst %+.1f%%",
			candidate.name, 100*best[candidate.name], 100*worst[candidate.name])
	}
}
