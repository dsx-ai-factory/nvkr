// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import "sort"

// stageAuthority is the authority a provider job must hold to run an operation.
// A GitHub job is one runner and one token scope, so the job boundary follows
// authority and runner rather than operation kind (ADR-18 §8).
type stageAuthority int

const (
	// authorityProducer runs producer-controlled code and offline utilities. It
	// holds no deployment environment, identity token or registry credential.
	authorityProducer stageAuthority = iota
	// authoritySource is one source fetch that holds a source credential. It
	// never shares a job, so producer code cannot run beside that credential.
	authoritySource
	// authorityRelease runs only NVKR's own attestation, publication, signing and
	// verification operations inside the protected release environment.
	authorityRelease
	// authorityObject is one object-store publication with its own workload identity.
	authorityObject
)

func stageAuthorityFor(plan *Plan, node Node) stageAuthority {
	switch {
	case IsObjectPublicationNode(plan, node.ID):
		return authorityObject
	case node.Kind == NodePublish || signFamilyNode(node.Kind):
		return authorityRelease
	case len(githubSourceEnvironment(plan, node)) > 0:
		return authoritySource
	}
	return authorityProducer
}

// rootToolAcquisition is an independent pinned tool download: no inputs, no
// producer instruction and no output of its own. Many jobs wait for it, so it
// never gains members and may be awaited by any job.
func rootToolAcquisition(node Node) bool {
	return node.Kind == NodeAcquire && node.BuildTarget == "" && node.IRNode == "" && len(node.Inputs) == 0
}

func rootToolStage(stage githubStage) bool {
	return len(stage.Nodes) == 1 && rootToolAcquisition(stage.Nodes[0])
}

// authorityStages contracts ordinary operations under the authority-and-runner
// policy. It owns only membership; IDs, names, budgets and the DAG check stay
// with projectExecutionStages.
type authorityStages struct {
	plan      *Plan
	artifacts map[string]Artifact
	// needs holds each stage's direct prerequisite stages by index.
	needs map[int]map[int]bool
}

// join returns the existing stage an operation extends. A member never adds a
// prerequisite its job did not already await, other than a root tool: every
// input comes from the job itself, one of its ancestors, or a root tool
// acquisition. Contraction therefore cannot create a cycle or make one output
// wait for another.
func (grouping *authorityStages) join(node Node, stages []githubStage, owners map[string]int) (int, bool) {
	if rootToolAcquisition(node) {
		return 0, false
	}
	if len(node.Inputs) == 0 {
		return grouping.joinRoots(node, stages)
	}
	producers := map[int]bool{}
	for _, input := range node.Inputs {
		owner, ok := owners[grouping.artifacts[input].Producer]
		if !ok {
			return 0, false
		}
		producers[owner] = true
	}
	candidates := make([]int, 0, len(producers))
	for candidate := range producers {
		candidates = append(candidates, candidate)
	}
	sort.Ints(candidates)
	for _, candidate := range candidates {
		if !grouping.admits(stages[candidate], node) {
			continue
		}
		awaited := true
		for _, other := range candidates {
			if other != candidate && !rootToolStage(stages[other]) && !grouping.ancestor(candidate, other) {
				awaited = false
				break
			}
		}
		if awaited && stageFits(stages[candidate], node) {
			return candidate, true
		}
	}
	return 0, false
}

// joinRoots places an input-free operation with the other input-free operations
// of its Build target on the same runner. None of them has a prerequisite, and
// every consumer builds that one target, so sharing a job delays no other output.
func (grouping *authorityStages) joinRoots(node Node, stages []githubStage) (int, bool) {
	if node.BuildTarget == "" {
		return 0, false
	}
	for index, stage := range stages {
		if !grouping.admits(stage, node) || stage.Nodes[0].BuildTarget != node.BuildTarget {
			continue
		}
		roots := true
		for _, member := range stage.Nodes {
			roots = roots && len(member.Inputs) == 0
		}
		if roots && stageFits(stage, node) {
			return index, true
		}
	}
	return 0, false
}

// stageFits keeps the job inside the provider's node and duration ceilings.
func stageFits(stage githubStage, node Node) bool {
	if len(stage.Nodes) >= githubMaximumStageNodes {
		return false
	}
	stage.Nodes = append(append([]Node(nil), stage.Nodes...), node)
	_, err := githubStageTimeoutMinutes(stage)
	return err == nil
}

// admits reports whether a stage and an operation share one authority class,
// runner and subject. A producer job serves one Build target; a release job
// serves one publication route. Credentialed source fetches, object
// publications, disks and root tools keep their own jobs.
func (grouping *authorityStages) admits(stage githubStage, node Node) bool {
	first := stage.Nodes[0]
	authority := stageAuthorityFor(grouping.plan, node)
	if stage.DiskOutput != "" || rootToolStage(stage) || first.RunsOn != node.RunsOn ||
		(authority != authorityProducer && authority != authorityRelease) ||
		stageAuthorityFor(grouping.plan, first) != authority {
		return false
	}
	for _, member := range stage.Nodes {
		if authority == authorityRelease && member.Target != node.Target {
			return false
		}
		if node.BuildTarget != "" && member.BuildTarget != "" && member.BuildTarget != node.BuildTarget {
			return false
		}
		// One job has one native platform; a source fetch names none.
		if node.Platform != "" && member.Platform != "" && member.Platform != node.Platform {
			return false
		}
	}
	return true
}

// record registers a member's prerequisites after it has been placed.
func (grouping *authorityStages) record(node Node, stage int, owners map[string]int) {
	if grouping.needs[stage] == nil {
		grouping.needs[stage] = map[int]bool{}
	}
	for _, input := range node.Inputs {
		if owner, ok := owners[grouping.artifacts[input].Producer]; ok && owner != stage {
			grouping.needs[stage][owner] = true
		}
	}
}

// ancestor reports whether target is a direct or transitive prerequisite of stage.
func (grouping *authorityStages) ancestor(stage, target int) bool {
	seen := map[int]bool{}
	var visit func(int) bool
	visit = func(current int) bool {
		for parent := range grouping.needs[current] {
			if parent == target {
				return true
			}
			if !seen[parent] {
				seen[parent] = true
				if visit(parent) {
					return true
				}
			}
		}
		return false
	}
	return visit(stage)
}
