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
	ID    string
	Name  string
	Nodes []Node
}

// githubStageTimeoutMinutes projects the producer-owned per-node timeout into
// GitHub's whole-minute job limit. A stage with no declared timeout keeps the
// bounded 60-minute adapter default. Grouped nodes execute serially, so their
// declared budgets are added rather than taking only the largest value.
func githubStageTimeoutMinutes(stage githubStage) (int, error) {
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
		if minutes > githubMaximumJobTimeoutMinutes || total > githubMaximumJobTimeoutMinutes-minutes {
			return 0, e.New(e.CodeEngineCapability,
				"GitHub stage %q timeout exceeds the %d-minute job limit",
				stage.ID, githubMaximumJobTimeoutMinutes)
		}
		total += minutes
	}
	if !declared {
		return githubDefaultStageTimeoutMinutes, nil
	}
	// Once any node declares a timeout, the stage budget is the sum of its
	// serial node budgets. Give each undeclared node the bounded adapter default
	// rather than silently counting it as zero.
	for range undeclared {
		if total > githubMaximumJobTimeoutMinutes-githubDefaultStageTimeoutMinutes {
			return 0, e.New(e.CodeEngineCapability,
				"GitHub stage %q timeout exceeds the %d-minute job limit",
				stage.ID, githubMaximumJobTimeoutMinutes)
		}
		total += githubDefaultStageTimeoutMinutes
	}
	return int(total), nil
}

func githubStages(plan *Plan) ([]githubStage, map[string]string, error) {
	var stages []githubStage
	for _, node := range topologicalFactoryNodes(plan.Graph) {
		node, err := plan.ExecutionFor(node)
		if err != nil {
			return nil, nil, err
		}
		if len(stages) > 0 {
			last := &stages[len(stages)-1]
			first := last.Nodes[0]
			if len(last.Nodes) < 16 && githubUtilityNode(first) && githubUtilityNode(node) &&
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
	for _, node := range stage.Nodes {
		if node.Kind == NodeVerify {
			return "${{ always() && " + githubDispatchCondition(plan) + " }}"
		}
	}
	return "${{ " + githubDispatchCondition(plan) + " }}"
}
