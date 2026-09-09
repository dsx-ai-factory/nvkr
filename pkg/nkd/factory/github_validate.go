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

type githubWorkflowDocument struct {
	Name        string                     `json:"name"`
	On          map[string]json.RawMessage `json:"on"`
	Permissions map[string]string          `json:"permissions"`
	Jobs        map[string]githubJob       `json:"jobs"`
}

type githubJob struct {
	Name        string                     `json:"name"`
	RunsOn      string                     `json:"runs-on"`
	Container   json.RawMessage            `json:"container,omitempty"`
	Services    map[string]json.RawMessage `json:"services,omitempty"`
	Needs       []string                   `json:"needs,omitempty"`
	If          string                     `json:"if,omitempty"`
	Permissions map[string]string          `json:"permissions,omitempty"`
	Env         map[string]string          `json:"env,omitempty"`
	Steps       []githubStep               `json:"steps"`
}

type githubStep struct {
	Name            string            `json:"name,omitempty"`
	Uses            string            `json:"uses,omitempty"`
	Run             string            `json:"run,omitempty"`
	ContinueOnError bool              `json:"continue-on-error,omitempty"`
	If              string            `json:"if,omitempty"`
	With            map[string]string `json:"with,omitempty"`
}

// ValidateGitHubBundle performs local schema and semantic analysis only. It does not
// contact GitHub. The checks tie every job dependency back to an artifact producer,
// enforce pinned actions and offline build commands, and verify all generated inputs.
func ValidateGitHubBundle(ctx context.Context, plan *Plan, bundle *GeneratedBundle) error {
	workflow, err := validateGeneratedInputs(ctx, plan, bundle)
	if err != nil {
		return err
	}

	var document githubWorkflowDocument
	if err := yaml.UnmarshalStrict(workflow, &document); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "GitHub workflow schema is invalid")
	}
	if document.Name != "nkd-ai-factory-"+plan.Name || document.Permissions["contents"] != "read" {
		return e.New(e.CodeInvalidCatalog, "GitHub workflow name/triggers/root permissions are incomplete")
	}
	if _, ok := document.On["workflow_dispatch"]; !ok {
		return e.New(e.CodeInvalidCatalog, "GitHub workflow omits workflow_dispatch")
	}
	if pullRequest, ok := document.On["pull_request"]; !ok || strings.TrimSpace(string(pullRequest)) != "{}" {
		return e.New(e.CodeInvalidCatalog, "GitHub workflow must statically check every pull request for generation drift")
	}
	if !strings.Contains(string(workflow), "# qualification: "+plan.Qualification.String()) ||
		!strings.Contains(string(workflow), "# planDigest: "+bundle.PlanDigest) {
		return e.New(e.CodeInvalidCatalog, "GitHub workflow omits static qualification or plan identity header")
	}

	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	nodeJobs := make(map[string]string, len(plan.Graph.Nodes))
	bootstrap := map[string]string{}
	for _, node := range plan.Graph.Nodes {
		nodeJobs[node.ID] = githubJobID(node.ID)
		bootstrap[node.RunsOn] = "build-runner-" + shortStableName(node.RunsOn)
	}
	expectedJobCount := len(plan.Graph.Nodes) + len(bootstrap)
	if len(plan.LifecycleOutputs) > 0 {
		expectedJobCount++
	}
	if len(document.Jobs) != expectedJobCount {
		return e.New(e.CodeInvalidCatalog, "GitHub workflow has %d jobs; artifact graph requires %d", len(document.Jobs), expectedJobCount)
	}
	signFamily := make(map[string]bool, len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		if signFamilyNode(node.Kind) {
			signFamily[nodeJobs[node.ID]] = true
		}
	}
	for jobID, job := range document.Jobs {
		// An OIDC identity token is the credential a keyless signature is minted from.
		// Only the signing family may request one; anywhere else it is an ambient
		// authority the pipeline never needs and must not be granted.
		if token, ok := job.Permissions["id-token"]; ok {
			if !signFamily[jobID] {
				return e.New(e.CodeEngineCapability,
					"GitHub job %q requests id-token %q but is not a signing job", jobID, token)
			}
			if token != "write" {
				return e.New(e.CodeEngineCapability,
					"GitHub signing job %q requests id-token %q, not write", jobID, token)
			}
		}
		for _, step := range job.Steps {
			if step.Uses == githubCheckoutAction && step.With["persist-credentials"] != "false" {
				return e.New(e.CodeEngineCapability, "GitHub job %q permits checkout to persist ambient credentials", jobID)
			}
			if step.Uses == githubUploadAction && step.With["if-no-files-found"] != "error" {
				return e.New(e.CodeEngineCapability, "GitHub job %q permits a missing declared output", jobID)
			}
		}
	}
	for runner, jobID := range bootstrap {
		job, ok := document.Jobs[jobID]
		if !ok || job.RunsOn != runner || len(job.Steps) != 5 || job.Steps[0].Uses != githubCheckoutAction {
			return e.New(e.CodeInvalidCatalog, "GitHub runner bootstrap %q is incomplete", jobID)
		}
		// The bootstrap must provision the Go toolchain via the pinned setup-go action rather
		// than assume a preinstalled Go, and pin the version to the repo's .go-version so the
		// toolchain and setup-go cannot drift.
		if job.Steps[1].Uses != githubSetupGoAction || job.Steps[1].With["go-version-file"] != ".go-version" {
			return e.New(e.CodeInvalidCatalog, "GitHub runner bootstrap %q does not provision the pinned Go toolchain from .go-version", jobID)
		}
		bootstrapRuns := joinRuns(job.Steps)
		linkerFlags, err := generatorLinkerFlags(plan.GeneratorVersion)
		if err != nil {
			return err
		}
		if !stepUses(job.Steps, githubUploadAction) || !strings.Contains(bootstrapRuns, "go build -trimpath") ||
			!strings.Contains(bootstrapRuns, "-ldflags "+shellQuote(linkerFlags)) ||
			!strings.Contains(bootstrapRuns, generationCommand(plan, EngineGitHub)) {
			return e.New(e.CodeInvalidCatalog, "GitHub runner bootstrap %q does not build and transport the runner", jobID)
		}
	}
	for _, node := range plan.Graph.Nodes {
		jobID := nodeJobs[node.ID]
		job, ok := document.Jobs[jobID]
		if !ok || job.Name != node.ID || job.RunsOn != node.RunsOn {
			return e.New(e.CodeInvalidCatalog, "GitHub workflow job for artifact node %q is missing or misbound", node.ID)
		}
		expectedNeeds := []string{bootstrap[node.RunsOn]}
		for _, input := range node.Inputs {
			expectedNeeds = append(expectedNeeds, nodeJobs[artifacts[input].Producer])
		}
		expectedNeeds = uniqueSorted(expectedNeeds)
		actualNeeds := append([]string(nil), job.Needs...)
		sort.Strings(actualNeeds)
		if strings.Join(actualNeeds, "\x00") != strings.Join(expectedNeeds, "\x00") {
			return e.New(e.CodeInvalidCatalog, "GitHub job %q needs %v; artifact dataflow requires %v", jobID, actualNeeds, expectedNeeds)
		}
		if job.Steps[0].Uses != githubCheckoutAction || !stepUses(job.Steps, githubDownloadAction) {
			return e.New(e.CodeInvalidCatalog, "GitHub job %q omits pinned checkout or input transport", jobID)
		}
		if err := validateHelperPreparedFirst(jobID, job.Steps); err != nil {
			return err
		}
		for _, step := range job.Steps {
			if step.Uses != "" && step.Uses != githubCheckoutAction && step.Uses != githubUploadAction && step.Uses != githubDownloadAction {
				return e.New(e.CodeInvalidCatalog, "GitHub job %q uses unapproved or unpinned action %q", jobID, step.Uses)
			}
		}
		runs := joinRuns(job.Steps)
		if !strings.Contains(runs, "factory execute-node") && !strings.Contains(runs, "exec-node") {
			return e.New(e.CodeInvalidCatalog, "GitHub job %q does not execute an NKD plan node", jobID)
		}
		// ADR-18 §8: an IR-projected node with a pinned container (build toolchain / fetch
		// vendor image) is isolated by nkd-exec ITSELF orchestrating that container (--launch),
		// which reads the container, platform, and network-none posture from the committed
		// per-recipe graph (re-checked by the recipeDigest drift gate). Glue nodes still run
		// through an inline `<runtime> run --network …` wrapper, so they keep the YAML checks.
		if launchedExecNode(node) {
			if !strings.Contains(runs, "exec-node --launch") {
				return e.New(e.CodeEngineCapability, "GitHub isolated job %q does not orchestrate its pinned container via exec-node --launch", jobID)
			}
		} else {
			if node.Network == ir.NetworkNone && !strings.Contains(runs, "--network none") {
				return e.New(e.CodeEngineCapability, "GitHub isolated job %q does not enforce network none", jobID)
			}
			if node.Container != "" &&
				(!strings.Contains(runs, "--platform "+node.Platform) || node.Platform == "") {
				return e.New(e.CodeEngineCapability, "GitHub container job %q does not bind its target platform", jobID)
			}
		}
		if node.Kind == NodeVerify && job.If != "${{ always() }}" {
			return e.New(e.CodeEngineCapability, "GitHub terminal observation job %q is not unconditional", jobID)
		}
		if node.Kind == NodePublish {
			target := plan.Delivery.Targets[node.Target]
			if strings.HasPrefix(target.Ref, "ghcr.io/") && job.Permissions["packages"] != "write" {
				return e.New(e.CodeEngineCapability, "GitHub publish job %q lacks explicit packages:write", jobID)
			}
			if !strings.HasPrefix(target.Ref, "ghcr.io/") && job.Permissions["packages"] != "" {
				return e.New(e.CodeEngineCapability, "GitHub anonymous publish job %q has unnecessary packages permission", jobID)
			}
			if target.CredentialRef != "" && job.Env["NKD_CREDENTIAL"] == "" {
				return e.New(e.CodeEngineCapability, "GitHub publish job %q omits its credential reference", jobID)
			}
		}
		if signFamilyNode(node.Kind) {
			if plan.Delivery.Signing == nil {
				return e.New(e.CodeTrustPolicyMissing,
					"GitHub signing job %q has no delivery signing profile", jobID)
			}
			expected := githubSigningPermissions(node.Kind, plan.Delivery.Signing.Mode)
			if len(job.Permissions) != len(expected) {
				return e.New(e.CodeEngineCapability,
					"GitHub signing job %q holds %d permissions; %s signing requires exactly %v",
					jobID, len(job.Permissions), plan.Delivery.Signing.Mode, expected)
			}
			for name, value := range expected {
				if job.Permissions[name] != value {
					return e.New(e.CodeEngineCapability,
						"GitHub signing job %q has %s: %q, want %q", jobID, name, job.Permissions[name], value)
				}
			}
			target := plan.Delivery.Targets[node.Target]
			if target.CredentialRef != "" && job.Env["NKD_CREDENTIAL"] == "" {
				return e.New(e.CodeEngineCapability, "GitHub signing job %q omits its credential reference", jobID)
			}
		}
		for _, output := range node.Outputs {
			if !strings.Contains(runs, "transport-pack --archive "+carrierPath(output)+" --root . --path "+artifacts[output].Path) ||
				!stepUploads(job.Steps, githubArtifactName(output), carrierPath(output)) {
				return e.New(e.CodeEngineCapability, "GitHub job %q does not transport output %q", jobID, output)
			}
		}
		for _, input := range node.Inputs {
			if !strings.Contains(runs, "transport-unpack --archive "+carrierPath(input)+" --root . --path "+artifacts[input].Path) {
				return e.New(e.CodeEngineCapability, "GitHub job %q does not restore exact input %q", jobID, input)
			}
		}
	}
	return nil
}

// validateHelperPreparedFirst rejects a job that invokes the downloaded helper before
// restoring its execute bit. download-artifact drops the execute bit, so ./nkd-exec is
// not runnable until the dedicated preparation step runs; because the input Restore steps
// are themselves helper invocations, the preparation must come immediately after the
// helper download and strictly before the FIRST ./nkd-exec in the job.
func validateHelperPreparedFirst(jobID string, steps []githubStep) error {
	prepared := -1
	for index, step := range steps {
		if step.Name == prepareHelperStepName && strings.TrimSpace(step.Run) == prepareHelperRun {
			prepared = index
			break
		}
	}
	if prepared < 0 {
		return e.New(e.CodeEngineCapability,
			"GitHub job %q has no %q step; the transported helper arrives without its execute bit", jobID, prepareHelperStepName)
	}
	if prepared == 0 || steps[prepared-1].Uses != githubDownloadAction {
		return e.New(e.CodeEngineCapability,
			"GitHub job %q does not prepare the helper immediately after downloading it", jobID)
	}
	for index, step := range steps {
		if index >= prepared {
			break
		}
		if strings.Contains(step.Run, "./nkd-exec") {
			return e.New(e.CodeEngineCapability,
				"GitHub job %q invokes ./nkd-exec at step %d, before it is made executable at step %d", jobID, index, prepared)
		}
	}
	return nil
}

func stepUses(steps []githubStep, action string) bool {
	for _, step := range steps {
		if step.Uses == action {
			return true
		}
	}
	return false
}

func joinRuns(steps []githubStep) string {
	var runs []string
	for _, step := range steps {
		if step.Run != "" {
			runs = append(runs, step.Run)
		}
	}
	return strings.Join(runs, "\n")
}

func stepUploads(steps []githubStep, name, path string) bool {
	for _, step := range steps {
		if step.Uses == githubUploadAction && step.With["name"] == name && step.With["path"] == path && step.With["if-no-files-found"] == "error" {
			return true
		}
	}
	return false
}

func (document githubWorkflowDocument) String() string {
	return fmt.Sprintf("%s (%d jobs)", document.Name, len(document.Jobs))
}
