// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"slices"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// A stage is a scheduling choice, not a new graph or authority record. Offline
// consumers may stay with their sole producer; other ordinary grouping follows
// one target's observation-to-SBOM edge, never global adjacency. A disk's
// operations share its live owner. Validate the complete contracted graph.
type githubStage struct {
	DiskOutput            string
	ID                    string
	Name                  string
	Nodes                 []Node
	diskExecutionBudget   time.Duration
	diskJobTimeoutMinutes int
}

const (
	githubMaximumDiskJobTimeoutMinutes = 1440
	githubMaximumStepTimeoutMinutes    = 360
	githubDiskProviderOverheadMinutes  = githubSupportJobTimeoutMinutes
	githubDiskCleanupHeadroomMinutes   = 5
	githubDiskDiagnosticUploadMinutes  = 5
)

// GitHub limits an explicitly configured step timeout to six hours even on
// self-hosted runners. Longer disk work retains its full helper-owned deadline
// and enclosing job bound without emitting an unsupported step timeout.
func githubDiskStepTimeoutMinutes(stage githubStage) int {
	minutes := int((stage.diskExecutionBudget+time.Minute-1)/time.Minute) + githubDiskCleanupHeadroomMinutes
	if minutes > githubMaximumStepTimeoutMinutes {
		return 0
	}
	return minutes
}

// githubStageTimeoutMinutes projects the producer-owned per-node timeout into
// GitHub's whole-minute job limit. Utility-only stages without declared timeouts
// keep the bounded 60-minute adapter default. Producer stages reserve each serial
// node's declared or default budget so co-location cannot shorten producer time.
func githubStageTimeoutMinutes(stage githubStage) (int, error) {
	if stage.DiskOutput != "" {
		if stage.diskExecutionBudget <= 0 || stage.diskJobTimeoutMinutes <= 0 {
			return 0, e.New(e.CodeEngineCapability, "GitHub disk stage %q has no derived execution budget", stage.ID)
		}
		return stage.diskJobTimeoutMinutes, nil
	}
	limit := uint64(githubMaximumJobTimeoutMinutes)
	var total uint64
	declared := false
	undeclared := 0
	for _, node := range stage.Nodes {
		if node.Requirements == nil || node.Requirements.TimeoutSeconds == 0 {
			undeclared++
			continue
		}
		declared = true
		minutes := node.Requirements.TimeoutSeconds / 60
		if node.Requirements.TimeoutSeconds%60 != 0 {
			minutes++
		}
		if minutes > limit || total > limit-minutes {
			return 0, e.New(e.CodeEngineCapability,
				"GitHub stage %q timeout exceeds the %d-minute job limit",
				stage.ID, limit)
		}
		total += minutes
	}
	if !declared && !githubProducerStage(stage) {
		return githubDefaultStageTimeoutMinutes, nil
	}
	// Producer stages and stages with a declared timeout sum serial budgets.
	// Give each undeclared node the bounded adapter default rather than silently
	// counting it as zero.
	for range undeclared {
		if total > limit-githubDefaultStageTimeoutMinutes {
			return 0, e.New(e.CodeEngineCapability,
				"GitHub stage %q timeout exceeds the %d-minute job limit",
				stage.ID, limit)
		}
		total += githubDefaultStageTimeoutMinutes
	}
	return int(total), nil
}

// Disk stages execute producers, then terminal observation and publication
// preparation under one shared reserve. Synthetic SBOM/package nodes are
// recorded by completion, not separate hour-long jobs.
// Bound each real producer and reuse the strict inspector's member accounting.
func githubDiskStageBudget(plan *Plan, stage githubStage) (time.Duration, int, error) {
	maximum := uint64(githubMaximumDiskJobTimeoutMinutes * 60)
	total := uint64(githubDefaultStageTimeoutMinutes * 60) // shared terminal observation and publication preparation
	members, roots := 0, 0
	platform := ""
	for _, node := range stage.Nodes {
		if node.Kind != NodeBuild && node.Kind != NodeAcquire {
			continue
		}
		seconds, err := githubDiskProducerTimeoutSeconds(node)
		if err != nil {
			return 0, 0, err
		}
		if seconds > maximum || total > maximum-seconds {
			return 0, 0, e.New(e.CodeEngineCapability, "GitHub disk stage %q exceeds its job duration bound", stage.ID)
		}
		total += seconds
		members++
		if node.ExecutionBinding != nil {
			roots++
			platform = string(node.ExecutionBinding.Class.HostPlatform)
		}
	}
	if roots != 1 || members == 0 || plan == nil {
		return 0, 0, e.New(e.CodeEngineCapability, "GitHub disk stage %q requires one bound VM producer", stage.ID)
	}
	if githubDiskInspectionRequired(plan) {
		started := time.Unix(0, 0)
		deadline, err := diskEvidenceJoinDeadline(started, plan.Environment.DiskInspectionProfiles[platform].TimeoutSeconds, members)
		if err != nil {
			return 0, 0, err
		}
		inspectionSeconds := uint64(deadline.Sub(started) / time.Second)
		if inspectionSeconds > maximum || total > maximum-inspectionSeconds {
			return 0, 0, e.New(e.CodeEngineCapability, "GitHub disk stage %q inspection exceeds its job duration bound", stage.ID)
		}
		total += inspectionSeconds
	}
	// The helper returns before the provider boundary, leaving time for owner
	// cleanup and failure diagnostics. Earlier caller deadlines still win.
	uploadMinutes := githubDiskDiagnosticUploadMinutes // failed Packer diagnostics
	if githubDiskInspectionRequired(plan) {
		uploadMinutes += githubDiskDiagnosticUploadMinutes // always-run inspection diagnostics
	}
	overhead := uint64((githubDiskProviderOverheadMinutes + githubDiskCleanupHeadroomMinutes + uploadMinutes) * 60)
	if total > maximum-overhead {
		return 0, 0, e.New(e.CodeEngineCapability, "GitHub disk stage %q has no provider/cleanup headroom", stage.ID)
	}
	return time.Duration(total) * time.Second, int((total + overhead + 59) / 60), nil
}

func githubDiskProducerTimeoutSeconds(node Node) (uint64, error) {
	seconds := uint64(githubDefaultStageTimeoutMinutes * 60)
	if node.Requirements != nil && node.Requirements.TimeoutSeconds != 0 {
		seconds = node.Requirements.TimeoutSeconds
	}
	if node.ExecutionBinding != nil {
		ceiling := node.ExecutionBinding.Class.MaxTimeoutSeconds
		if ceiling == 0 {
			return 0, e.New(e.CodeEngineCapability, "GitHub disk producer %q has no class timeout ceiling", node.ID)
		}
		if node.Requirements == nil || node.Requirements.TimeoutSeconds == 0 {
			seconds = ceiling
		}
		if seconds > ceiling {
			return 0, e.New(e.CodeEngineCapability, "GitHub disk producer %q exceeds its selected class timeout ceiling", node.ID)
		}
	}
	if seconds > uint64(githubMaximumDiskJobTimeoutMinutes*60) {
		return 0, e.New(e.CodeEngineCapability, "GitHub disk producer %q exceeds its duration bound", node.ID)
	}
	return seconds, nil
}

func githubStages(plan *Plan) ([]githubStage, map[string]string, error) {
	ordered, schedulingKeys, err := githubSchedulingProjection(plan)
	if err != nil {
		return nil, nil, err
	}
	var stages []githubStage
	diskStages := make(map[string]int)
	observationStages := make(map[string]int)
	nodeStages := make(map[string]int, len(ordered))
	nodes, artifacts := planIndex(plan)
	for _, node := range ordered {
		node, err := plan.GitHubExecutionFor(node)
		if err != nil {
			return nil, nil, err
		}
		if output := githubDiskOutput(plan, node); output != "" {
			index, exists := diskStages[output]
			if !exists {
				index = len(stages)
				diskStages[output] = index
				stages = append(stages, githubStage{DiskOutput: output})
			}
			stages[index].Nodes = append(stages[index].Nodes, node)
			nodeStages[node.ID] = index
			continue
		}
		if index, ok := githubProducerStageFor(node, stages, nodeStages, artifacts); ok {
			stages[index].Nodes = append(stages[index].Nodes, node)
			nodeStages[node.ID] = index
			continue
		}
		if index, exists := observationStages[node.BuildTarget]; exists {
			stage := &stages[index]
			if githubObservationPair(*stage, node, nodes, artifacts) {
				stage.Nodes = append(stage.Nodes, node)
				nodeStages[node.ID] = index
				continue
			}
		}
		if node.Kind == NodeVerify && node.BuildTarget != "" {
			observationStages[node.BuildTarget] = len(stages)
		}
		stages = append(stages, githubStage{Nodes: []Node{node}})
		nodeStages[node.ID] = len(stages) - 1
	}
	jobs := make(map[string]string, len(plan.Graph.Nodes))
	jobOwners := map[string]string{"nvkr-lifecycle-run": "Run finalizer"}
	for i := range stages {
		stage := &stages[i]
		stage.ID = githubJobID(schedulingKeys[stage.Nodes[0].ID])
		stage.Name = githubStageLabel(plan, stage.Nodes)
		if len(stage.Nodes) > 1 {
			var ids []string
			for _, node := range stage.Nodes {
				ids = append(ids, schedulingKeys[node.ID])
			}
			stage.ID = githubJobID("process/" + strings.Join(ids, "+"))
			if githubProducerStage(*stage) {
				stage.Name = "Build and prepare " + stage.Nodes[0].BuildTarget
				if stage.Nodes[0].Kind == NodeAcquire {
					stage.Name = "Acquire and prepare " + stage.Nodes[0].BuildTarget
				}
			}
		}
		if stage.DiskOutput != "" {
			stage.ID = githubJobID("disk/" + stage.DiskOutput)
			stage.Name = "Build, inspect and package " + stage.DiskOutput
			if diskBuildProvenance(plan) {
				stage.Name = "Build and package producer evidence for " + stage.DiskOutput
			}
			if GitHubDiskBuildOnly(plan) {
				stage.Name = "Build and retain unqualified disk " + stage.DiskOutput
			}
			var err error
			stage.diskExecutionBudget, stage.diskJobTimeoutMinutes, err = githubDiskStageBudget(plan, *stage)
			if err != nil {
				return nil, nil, err
			}
		}
		if previous, exists := jobOwners[stage.ID]; exists {
			return nil, nil, e.New(e.CodeEngineCapability,
				"GitHub job ID %q collides for %q and %q", stage.ID, previous, stage.Nodes[0].ID)
		}
		jobOwners[stage.ID] = stage.Nodes[0].ID
		for _, node := range stage.Nodes {
			jobs[node.ID] = stage.ID
		}
	}
	if err := validateGitHubStageDAG(plan, stages, jobs); err != nil {
		return nil, nil, err
	}
	return stages, jobs, nil
}

func githubProducerStage(stage githubStage) bool {
	if stage.DiskOutput != "" || len(stage.Nodes) == 0 {
		return false
	}
	return stage.Nodes[0].Kind == NodeBuild || stage.Nodes[0].Kind == NodeAcquire
}

// Only append an offline consumer whose complete input set is already owned by
// one producer job. This leaves cross-output joins and independent producers as
// explicit job dependencies and cannot introduce a new contracted graph edge.
// Containers need not match: each logical node retains its admitted execution path.
// Credential-bearing operations are never candidates, and a full producer
// timeout never loses utility reserve.
func githubProducerStageFor(node Node, stages []githubStage, owners map[string]int, artifacts map[string]Artifact) (int, bool) {
	if !githubProducerUtilityNode(node) || len(node.Inputs) == 0 {
		return 0, false
	}
	index := -1
	for _, input := range node.Inputs {
		owner, ok := owners[artifacts[input].Producer]
		if !ok || index >= 0 && owner != index {
			return 0, false
		}
		index = owner
	}
	stage := stages[index]
	first := stage.Nodes[0]
	if !githubProducerStage(stage) || first.BuildTarget == "" || len(stage.Nodes) >= 16 ||
		first.RunsOn != node.RunsOn || first.Platform != node.Platform ||
		node.BuildTarget != "" && node.BuildTarget != first.BuildTarget {
		return 0, false
	}
	stage.Nodes = append(slices.Clone(stage.Nodes), node)
	if _, err := githubStageTimeoutMinutes(stage); err != nil {
		return 0, false
	}
	return index, true
}

func githubProducerUtilityNode(node Node) bool {
	if node.Network != ir.NetworkNone || node.Container == "" {
		return false
	}
	switch node.Kind {
	case NodeVerify, NodeSBOM, NodePackage:
		return true
	default:
		return false
	}
}

func validateGitHubStageDAG(plan *Plan, stages []githubStage, jobs map[string]string) error {
	_, artifacts := planIndex(plan)
	// Reuse the artifact scheduler's topological walk over the projected stage
	// edges; this description does not replace or weaken complete Plan admission.
	graph := ArtifactGraph{Artifacts: append([]Artifact(nil), plan.Graph.Artifacts...)}
	for i := range graph.Artifacts {
		graph.Artifacts[i].Producer = jobs[graph.Artifacts[i].Producer]
	}
	for _, stage := range stages {
		graph.Nodes = append(graph.Nodes, Node{ID: stage.ID, Inputs: stage.inputs(artifacts, jobs)})
	}
	if len(topologicalFactoryNodes(graph)) != len(stages) {
		return e.New(e.CodeEngineCapability, "GitHub grouped stages introduce a dependency cycle")
	}
	return nil
}

// Only pair the exact target's observation and SBOM. Additional SBOM inputs may
// come from independent root tool acquisition, never another output's work.
// This cannot create a dependency that leaves and reenters the pair. Packaging
// never joins this pair, and the complete contracted DAG is still checked.
func githubObservationPair(stage githubStage, node Node, nodes map[string]Node, artifacts map[string]Artifact) bool {
	if stage.DiskOutput != "" || len(stage.Nodes) != 1 {
		return false
	}
	first := stage.Nodes[0]
	if first.Kind != NodeVerify || node.Kind != NodeSBOM || first.BuildTarget == "" || first.BuildTarget != node.BuildTarget ||
		first.Network != ir.NetworkNone || node.Network != ir.NetworkNone || first.Container == "" ||
		first.RunsOn != node.RunsOn || first.Container != node.Container || first.Platform != node.Platform {
		return false
	}
	observed := false
	for _, input := range node.Inputs {
		if slices.Contains(first.Outputs, input) {
			observed = true
		} else if !slices.Contains(first.Inputs, input) {
			artifact := artifacts[input]
			producer := nodes[artifact.Producer]
			if artifact.Kind != "pipeline-tool" || producer.Kind != NodeAcquire || producer.BuildTarget != "" ||
				producer.IRNode != "" || len(producer.Inputs) != 0 {
				return false
			}
		}
	}
	return observed
}

func (stage githubStage) inputs(artifacts map[string]Artifact, nodeJobs map[string]string) []string {
	var inputs []string
	for _, node := range stage.Nodes {
		for _, input := range node.Inputs {
			if nodeJobs[artifacts[input].Producer] != stage.ID {
				inputs = append(inputs, input)
			}
		}
	}
	return uniqueSorted(inputs)
}

// carrierInputs projects logical dependencies onto transported bytes. A compiled
// disk's raw subject remains a graph input, but its bytes travel in the exact
// publication wrapper, which publication observation rehashes against the raw
// producer result. Never omit a raw subject unless both are also selected.
func (stage githubStage) carrierInputs(plan *Plan, artifacts map[string]Artifact, nodeJobs map[string]string) ([]string, error) {
	inputs := stage.inputs(artifacts, nodeJobs)
	carriers := make([]string, 0, len(inputs))
	for _, id := range inputs {
		if output, raw := diskPublicationRequirement(plan, artifacts[id]); raw {
			if !slices.Contains(inputs, diskPublicationWrapperID(output)) ||
				!slices.Contains(inputs, diskPublicationProducerResultID(output, artifacts[id])) {
				return nil, e.New(e.CodeEngineCapability,
					"GitHub stage %q consumes raw disk %q without its publication wrapper and producer record", stage.ID, id)
			}
			continue
		}
		carriers = append(carriers, id)
	}
	return carriers, nil
}

func (stage githubStage) needs(artifacts map[string]Artifact, nodeJobs map[string]string, bootstrap string) []string {
	needs := []string{bootstrap}
	for _, input := range stage.inputs(artifacts, nodeJobs) {
		needs = append(needs, nodeJobs[artifacts[input].Producer])
	}
	return uniqueSorted(needs)
}

func (stage githubStage) condition(plan *Plan) string {
	if stage.executionCondition() == executionAfterCompletion {
		return "${{ always() && " + githubDispatchCondition(plan) + " }}"
	}
	return "${{ " + githubDispatchCondition(plan) + " }}"
}
