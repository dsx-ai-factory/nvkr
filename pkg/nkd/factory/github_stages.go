package factory

import (
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// A stage is a scheduling choice, not a new graph or authority record. Only
// consecutive compatible offline utility nodes share a runner. Contracting a
// consecutive interval of a topological ordering cannot introduce a cycle.
type githubStage struct {
	DiskOutput string
	ID         string
	Name       string
	Nodes      []Node
}

// githubStageTimeoutMinutes projects the producer-owned per-node timeout into
// GitHub's whole-minute job limit. A stage with no declared timeout keeps the
// bounded 60-minute adapter default. Grouped nodes execute serially, so their
// declared budgets are added rather than taking only the largest value.
func githubStageTimeoutMinutes(stage githubStage) (int, error) {
	limit := uint64(githubMaximumJobTimeoutMinutes)
	if stage.DiskOutput != "" {
		limit = 1440
	}
	executionLimit := limit
	if stage.DiskOutput != "" {
		if limit <= githubDiskJobHeadroomMinutes {
			return 0, e.New(e.CodeEngineCapability, "GitHub disk job has no execution budget after reserved headroom")
		}
		executionLimit -= githubDiskJobHeadroomMinutes
	}
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
		if minutes > executionLimit || total > executionLimit-minutes {
			return 0, e.New(e.CodeEngineCapability,
				"GitHub stage %q execution timeout exceeds the %d-minute job limit",
				stage.ID, executionLimit)
		}
		total += minutes
	}
	if !declared {
		if stage.DiskOutput != "" {
			return githubDefaultStageTimeoutMinutes + int(githubDiskJobHeadroomMinutes), nil
		}
		return githubDefaultStageTimeoutMinutes, nil
	}
	// Once any node declares a timeout, the stage budget is the sum of its
	// serial node budgets. Give each undeclared node the bounded adapter default
	// rather than silently counting it as zero.
	for range undeclared {
		if total > executionLimit-githubDefaultStageTimeoutMinutes {
			return 0, e.New(e.CodeEngineCapability,
				"GitHub stage %q execution timeout exceeds the %d-minute job limit",
				stage.ID, executionLimit)
		}
		total += githubDefaultStageTimeoutMinutes
	}
	if stage.DiskOutput != "" {
		total += githubDiskJobHeadroomMinutes
	}
	return int(total), nil
}

// Disk output collection, job finalization and artifact upload continue after
// the producer deadline. Keep a small fixed margin in the provider job timeout.
const githubDiskJobHeadroomMinutes uint64 = 15

func githubStageExecutionTimeoutMinutes(stage githubStage) (int, error) {
	jobMinutes, err := githubStageTimeoutMinutes(stage)
	if err != nil {
		return 0, err
	}
	if stage.DiskOutput != "" {
		return jobMinutes - int(githubDiskJobHeadroomMinutes), nil
	}
	return jobMinutes, nil
}

func githubStages(plan *Plan) ([]githubStage, map[string]string, error) {
	var stages []githubStage
	diskStages := make(map[string]int)
	for _, node := range topologicalFactoryNodes(plan.Graph) {
		node, err := plan.ExecutionFor(node)
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
			continue
		}
		if len(stages) > 0 {
			last := &stages[len(stages)-1]
			first := last.Nodes[0]
			if !(PerOutputPublication(plan) && (first.PackageKind == PackageOCI || node.PackageKind == PackageOCI)) &&
				last.DiskOutput == "" && len(last.Nodes) < 16 && githubUtilityNode(first) && githubUtilityNode(node) &&
				first.RunsOn == node.RunsOn && first.Container == node.Container && first.Platform == node.Platform {
				last.Nodes = append(last.Nodes, node)
				continue
			}
		}
		stages = append(stages, githubStage{Nodes: []Node{node}})
	}
	jobs := make(map[string]string, len(plan.Graph.Nodes))
	for i := range stages {
		stage := &stages[i]
		stage.ID = githubJobID(stage.Nodes[0].ID)
		stage.Name = githubStageLabel(plan, stage.Nodes)
		if len(stage.Nodes) > 1 {
			var ids []string
			for _, node := range stage.Nodes {
				ids = append(ids, node.ID)
			}
			stage.ID = githubJobID("process/" + strings.Join(ids, "+"))
		}
		if stage.DiskOutput != "" {
			stage.ID = githubJobID("disk/" + stage.DiskOutput)
			stage.Name = "Build, inspect and package " + stage.DiskOutput
		}
		for _, node := range stage.Nodes {
			jobs[node.ID] = stage.ID
		}
	}
	return stages, jobs, nil
}

func githubUtilityNode(node Node) bool {
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

func (stage githubStage) needs(artifacts map[string]Artifact, nodeJobs map[string]string, bootstrap string) []string {
	needs := []string{bootstrap}
	for _, input := range stage.inputs(artifacts, nodeJobs) {
		needs = append(needs, nodeJobs[artifacts[input].Producer])
	}
	return uniqueSorted(needs)
}

func (stage githubStage) condition(plan *Plan) string {
	if stage.DiskOutput != "" {
		return "${{ " + githubDispatchCondition(plan) + " }}"
	}
	for _, node := range stage.Nodes {
		if node.Kind == NodeVerify {
			return "${{ always() && " + githubDispatchCondition(plan) + " }}"
		}
	}
	return "${{ " + githubDispatchCondition(plan) + " }}"
}
