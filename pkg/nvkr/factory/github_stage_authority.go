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
	// Shared source owners release their carrier before producer work begins.
	sourceSeedOwners map[string]bool
	// needs holds each stage's direct prerequisite stages by index.
	needs map[int]map[int]bool
}

// join returns the existing stage an operation extends. An ordinary member adds
// no prerequisite its job did not already await, other than a root tool: every
// input comes from the job itself, one of its ancestors, or a root tool
// acquisition. Contraction therefore cannot create a cycle or make one output
// wait for another. Image publication batches additionally admit acyclic
// cross-output dependencies through joinImagePublications.
func (grouping *authorityStages) join(node Node, stages []githubStage, owners map[string]int) (int, bool) {
	if rootToolAcquisition(node) {
		return 0, false
	}
	if node.Kind == NodeFetch {
		if stage, ok := grouping.joinSourceFetches(node, stages, owners); ok {
			return stage, true
		}
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
	if grouping.imagePublication(node) {
		return grouping.joinImagePublications(node, stages, candidates)
	}
	return 0, false
}

// Image publication operations may share setup with other image publications.
// Keep every operation's existing timeout, command, credential scope and result
// slot. A chain can span jobs when its next operation exhausts the job budget.
// New prerequisites are allowed only when they cannot introduce a back edge.
func (grouping *authorityStages) joinImagePublications(node Node, stages []githubStage, producers []int) (int, bool) {
	for candidate, stage := range stages {
		if stage.DiskOutput != "" || !stageFits(stage, node) {
			continue
		}
		compatible := true
		targets := map[string]bool{node.Target: true}
		for _, member := range stage.Nodes {
			targets[member.Target] = true
			if !grouping.imagePublication(member) || member.RunsOn != node.RunsOn || member.Platform != node.Platform ||
				grouping.plan.Targets[member.Target].CredentialRef != grouping.plan.Targets[node.Target].CredentialRef {
				compatible = false
				break
			}
		}
		if !compatible || len(targets) > 2 {
			continue
		}
		for _, producer := range producers {
			if producer != candidate && grouping.ancestor(producer, candidate) {
				compatible = false
				break
			}
		}
		if compatible {
			return candidate, true
		}
	}
	return 0, false
}

// Source-only stages may acquire prerequisites from shared seeds. Keep sibling
// fetches together when that contraction is acyclic; otherwise a multi-source
// consumer would gain one job per seed. Credentialed fetches remain separate.
func (grouping *authorityStages) joinSourceFetches(node Node, stages []githubStage, owners map[string]int) (int, bool) {
	for candidate, stage := range stages {
		if !grouping.admits(stage, node) || !stageFits(stage, node) {
			continue
		}
		compatible := true
		for _, member := range stage.Nodes {
			if member.Kind != NodeFetch || member.BuildTarget != node.BuildTarget {
				compatible = false
				break
			}
		}
		for _, input := range node.Inputs {
			producer, exists := owners[grouping.artifacts[input].Producer]
			if !exists || producer != candidate && grouping.ancestor(producer, candidate) {
				compatible = false
				break
			}
		}
		if compatible {
			return candidate, true
		}
	}
	return 0, false
}

func (grouping *authorityStages) imagePublication(node Node) bool {
	if !PerOutputPublication(grouping.plan) || stageAuthorityFor(grouping.plan, node) != authorityRelease {
		return false
	}
	switch node.Kind {
	case NodeAttestEvidence, NodePublish, NodeSign, NodeVerifyPublication:
	default:
		return false
	}
	if grouping.plan.Targets[node.Target].Kind != "oci" {
		return false
	}
	for _, output := range grouping.plan.LifecycleOutputs {
		if node.Target == "output-"+output.Name {
			return lifecyclePackageKind(output.Kind) == PackageImage && output.ImageIndex == nil
		}
	}
	return false
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
		if grouping.sourceSeedOwners[member.ID] && node.Kind != NodeFetch ||
			grouping.sourceSeedOwners[node.ID] && member.Kind != NodeFetch {
			return false
		}
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
