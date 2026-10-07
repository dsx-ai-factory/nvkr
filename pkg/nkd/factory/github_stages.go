package factory

import (
	"slices"
	"strings"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// A stage is a scheduling choice, not a new graph or authority record. Compatible
// offline utilities group consecutively; a disk's operations share its live owner
// even when interleaved with another output. Validate the contracted job graph
// because grouping nonconsecutive nodes can otherwise introduce a cycle.
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
	githubDiskProviderOverheadMinutes  = githubSupportJobTimeoutMinutes
	githubDiskCleanupHeadroomMinutes   = 5
	githubDiskDiagnosticUploadMinutes  = 5
)

// githubStageTimeoutMinutes projects the producer-owned per-node timeout into
// GitHub's whole-minute job limit. A stage with no declared timeout keeps the
// bounded 60-minute adapter default. Grouped nodes execute serially, so their
// declared budgets are added rather than taking only the largest value.
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
	if !declared {
		return githubDefaultStageTimeoutMinutes, nil
	}
	// Once any node declares a timeout, the stage budget is the sum of its
	// serial node budgets. Give each undeclared node the bounded adapter default
	// rather than silently counting it as zero.
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
	if !diskBuildProvenance(plan) {
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
	overhead := uint64((githubDiskProviderOverheadMinutes + githubDiskCleanupHeadroomMinutes + githubDiskDiagnosticUploadMinutes) * 60)
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
			if diskBuildProvenance(plan) {
				stage.Name = "Build and package producer evidence for " + stage.DiskOutput
			}
			var err error
			stage.diskExecutionBudget, stage.diskJobTimeoutMinutes, err = githubDiskStageBudget(plan, *stage)
			if err != nil {
				return nil, nil, err
			}
		}
		for _, node := range stage.Nodes {
			jobs[node.ID] = stage.ID
		}
	}
	if err := validateGitHubStageDAG(plan, stages, jobs); err != nil {
		return nil, nil, err
	}
	return stages, jobs, nil
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
