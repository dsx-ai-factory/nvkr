package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
	"sigs.k8s.io/yaml"
)

type gitlabWorkflow struct {
	Rules []gitlabRule `json:"rules"`
}

type gitlabRule struct {
	If string `json:"if"`
}

type gitlabJob struct {
	Stage     string            `json:"stage"`
	Tags      []string          `json:"tags"`
	Needs     []string          `json:"needs,omitempty"`
	When      string            `json:"when,omitempty"`
	Variables map[string]string `json:"variables,omitempty"`
	Script    []string          `json:"script"`
	Artifacts struct {
		Paths []string `json:"paths"`
	} `json:"artifacts,omitempty"`
}

// ValidateGitLabBundle performs semantic static analysis of the generated GitLab
// include. It does not contact GitLab or claim provider execution.
func ValidateGitLabBundle(ctx context.Context, plan *Plan, bundle *GeneratedBundle) error {
	if err := gitlabSigningUnsupported(plan); err != nil {
		return err
	}
	workflow, err := validateGeneratedInputs(ctx, plan, bundle)
	if err != nil {
		return err
	}
	var root map[string]json.RawMessage
	if err := yaml.UnmarshalStrict(workflow, &root); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "GitLab workflow schema is invalid")
	}
	var stages []string
	if err := json.Unmarshal(root["stages"], &stages); err != nil || len(stages) < 2 || stages[0] != "bootstrap" {
		return e.New(e.CodeInvalidCatalog, "GitLab workflow stages are incomplete")
	}
	var workflowRules gitlabWorkflow
	if err := json.Unmarshal(root["workflow"], &workflowRules); err != nil ||
		!gitlabHasRule(workflowRules.Rules, `$CI_PIPELINE_SOURCE == "merge_request_event"`) ||
		!gitlabHasRule(workflowRules.Rules, `$CI_PIPELINE_SOURCE == "web"`) {
		return e.New(e.CodeInvalidCatalog, "GitLab workflow must run for merge requests and manual requests")
	}
	delete(root, "stages")
	delete(root, "workflow")
	if !strings.Contains(string(workflow), "# qualification: "+plan.Qualification.String()) ||
		!strings.Contains(string(workflow), "# planDigest: "+bundle.PlanDigest) {
		return e.New(e.CodeInvalidCatalog, "GitLab workflow omits static qualification or plan identity header")
	}

	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	nodeJobs := make(map[string]string, len(plan.Graph.Nodes))
	bootstrap := map[string]string{}
	for _, node := range plan.Graph.Nodes {
		nodeJobs[node.ID] = generatedJobID(node.ID)
		bootstrap[node.RunsOn] = "build-runner-" + shortStableName(node.RunsOn)
	}
	expectedJobCount := len(plan.Graph.Nodes) + len(bootstrap)
	if len(plan.LifecycleOutputs) > 0 {
		expectedJobCount += 2
	}
	if len(root) != expectedJobCount {
		return e.New(e.CodeInvalidCatalog, "GitLab workflow has %d jobs; artifact graph requires %d",
			len(root), expectedJobCount)
	}
	depths, maxDepth, err := factoryNodeDepths(plan.Graph)
	if err != nil {
		return err
	}
	expectedStageCount := maxDepth + 1
	if len(plan.LifecycleOutputs) > 0 {
		expectedStageCount += 2
	}
	if len(stages) != expectedStageCount {
		return e.New(e.CodeInvalidCatalog, "GitLab workflow stage count does not match artifact depth")
	}
	linkerFlags, err := generatorLinkerFlags(plan.GeneratorVersion)
	if err != nil {
		return err
	}
	for runner, jobID := range bootstrap {
		job, err := decodeGitLabJob(root, jobID)
		if err != nil {
			return err
		}
		script := strings.Join(job.Script, "\n")
		if job.Stage != "bootstrap" || !containsString(job.Tags, runner) || len(job.Needs) != 0 ||
			!strings.Contains(script, "go build -trimpath") ||
			!strings.Contains(script, "-ldflags "+shellQuote(linkerFlags)) ||
			!strings.Contains(script, generationCommand(plan, EngineGitLab)) ||
			!containsString(job.Artifacts.Paths, "nkd-exec") {
			return e.New(e.CodeInvalidCatalog, "GitLab runner bootstrap %q is incomplete", jobID)
		}
	}
	for _, node := range plan.Graph.Nodes {
		jobID := nodeJobs[node.ID]
		job, err := decodeGitLabJob(root, jobID)
		if err != nil {
			return err
		}
		if job.Stage != stageForDepth(depths[node.ID]) || !containsString(job.Tags, node.RunsOn) {
			return e.New(e.CodeInvalidCatalog, "GitLab job %q has the wrong stage or runner tag", jobID)
		}
		// No network-none runner tag is required (ADR-18 §8): the isolation contract
		// is the `--network none` container the node script launches, checked below.
		expectedNeeds := []string{bootstrap[node.RunsOn]}
		for _, input := range node.Inputs {
			expectedNeeds = append(expectedNeeds, nodeJobs[artifacts[input].Producer])
		}
		expectedNeeds = uniqueSorted(expectedNeeds)
		actualNeeds := append([]string(nil), job.Needs...)
		sort.Strings(actualNeeds)
		if strings.Join(actualNeeds, "\x00") != strings.Join(expectedNeeds, "\x00") {
			return e.New(e.CodeInvalidCatalog, "GitLab job %q needs %v; artifact dataflow requires %v", jobID, actualNeeds, expectedNeeds)
		}
		script := strings.Join(job.Script, "\n")
		if !strings.Contains(script, "factory execute-node") && !strings.Contains(script, "exec-node") {
			return e.New(e.CodeInvalidCatalog, "GitLab job %q does not execute an NKD plan node", jobID)
		}
		// ADR-18 §8: an IR-projected containerized node is isolated by nkd-exec
		// orchestrating its pinned container via --launch (which reads the container, platform,
		// and network-none posture from the committed graph); glue nodes keep the inline
		// `<runtime> run --network …` wrapper and its YAML-level checks.
		if launchedExecNode(node) {
			if !strings.Contains(script, "exec-node --launch") {
				return e.New(e.CodeEngineCapability, "GitLab isolated job %q does not orchestrate its pinned container via exec-node --launch", jobID)
			}
		} else {
			if node.Network == ir.NetworkNone && !strings.Contains(script, "--network none") {
				return e.New(e.CodeEngineCapability, "GitLab isolated job %q does not enforce network none", jobID)
			}
			if node.Container != "" && (!strings.Contains(script, "--platform "+node.Platform) || node.Platform == "") {
				return e.New(e.CodeEngineCapability, "GitLab container job %q does not bind its target platform", jobID)
			}
		}
		if node.Kind == NodeVerify && job.When != "always" {
			return e.New(e.CodeEngineCapability, "GitLab terminal observation job %q is not unconditional", jobID)
		}
		if node.Kind == NodePublish {
			target := plan.ExecutionSettings.Targets[node.Target]
			if target.CredentialRef != "" {
				secret, ok := strings.CutPrefix(target.CredentialRef, "gitlab:")
				if !ok || job.Variables["NKD_CREDENTIAL"] != "$"+secret {
					return e.New(e.CodeEngineCapability, "GitLab publish job %q omits its credential reference", jobID)
				}
			}
		}
		for _, input := range node.Inputs {
			if !strings.Contains(script, "transport-unpack --archive "+carrierPath(input)+" --root . --path "+artifacts[input].Path) {
				return e.New(e.CodeEngineCapability, "GitLab job %q does not restore exact input %q", jobID, input)
			}
		}
		for _, output := range node.Outputs {
			carrier := carrierPath(output)
			if !strings.Contains(script, "transport-pack --archive "+carrier+" --root . --path "+artifacts[output].Path) ||
				!containsString(job.Artifacts.Paths, carrier) {
				return e.New(e.CodeEngineCapability, "GitLab job %q does not transport output %q", jobID, output)
			}
		}
	}
	if len(plan.LifecycleOutputs) > 0 {
		statusJob, err := decodeGitLabJob(root, "nkd-lifecycle-jobs-succeeded")
		if err != nil {
			return err
		}
		expectedNeeds := make([]string, 0, len(nodeJobs))
		for _, jobID := range nodeJobs {
			expectedNeeds = append(expectedNeeds, jobID)
		}
		expectedNeeds = uniqueSorted(expectedNeeds)
		actualNeeds := append([]string(nil), statusJob.Needs...)
		sort.Strings(actualNeeds)
		statusScript := strings.Join(statusJob.Script, "\n")
		if statusJob.Stage != "lifecycle-status" || strings.Join(actualNeeds, "\x00") != strings.Join(expectedNeeds, "\x00") ||
			!strings.Contains(statusScript, "all-jobs-success") || !containsString(statusJob.Artifacts.Paths, ".nkd/provider-outcomes/all-jobs-success") {
			return e.New(e.CodeEngineCapability, "GitLab lifecycle status job does not bind every provider job success")
		}
		finalizer, err := decodeGitLabJob(root, "nkd-lifecycle-run")
		if err != nil {
			return err
		}
		finalizerScript := strings.Join(finalizer.Script, "\n")
		if finalizer.Stage != "lifecycle" || finalizer.When != "always" || !containsString(finalizer.Needs, "nkd-lifecycle-jobs-succeeded") ||
			!strings.Contains(finalizerScript, "job-failed") || !strings.Contains(finalizerScript, "run finalize") ||
			!strings.Contains(finalizerScript, "go build -trimpath") {
			return e.New(e.CodeEngineCapability, "GitLab lifecycle finalizer omits provider success accounting or helper recovery")
		}
	}
	return nil
}

func decodeGitLabJob(root map[string]json.RawMessage, id string) (gitlabJob, error) {
	raw, ok := root[id]
	if !ok {
		return gitlabJob{}, e.New(e.CodeInvalidCatalog, "GitLab workflow omits job %q", id)
	}
	var job gitlabJob
	if err := json.Unmarshal(raw, &job); err != nil {
		return gitlabJob{}, e.Wrap(e.CodeInvalidCatalog, err, "decoding GitLab job %q", id)
	}
	return job, nil
}

func gitlabHasRule(rules []gitlabRule, value string) bool {
	for _, rule := range rules {
		if rule.If == value {
			return true
		}
	}
	return false
}

func stageForDepth(depth int) string {
	return "factory-" + fmt.Sprintf("%03d", depth)
}
