// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

type githubWorkflowDocument struct {
	Name        string                     `json:"name"`
	On          map[string]json.RawMessage `json:"on"`
	Permissions map[string]string          `json:"permissions"`
	Env         map[string]string          `json:"env,omitempty"`
	Jobs        map[string]githubJob       `json:"jobs"`
	Concurrency struct {
		Group            string `json:"group"`
		CancelInProgress bool   `json:"cancel-in-progress"`
	} `json:"concurrency"`
}

type githubJob struct {
	Environment    string                     `json:"environment,omitempty"`
	TimeoutMinutes int                        `json:"timeout-minutes"`
	Name           string                     `json:"name"`
	RunsOn         string                     `json:"runs-on"`
	Container      json.RawMessage            `json:"container,omitempty"`
	Services       map[string]json.RawMessage `json:"services,omitempty"`
	Needs          []string                   `json:"needs,omitempty"`
	If             string                     `json:"if,omitempty"`
	Permissions    map[string]string          `json:"permissions,omitempty"`
	Env            map[string]string          `json:"env,omitempty"`
	Outputs        map[string]string          `json:"outputs,omitempty"`
	Steps          []githubStep               `json:"steps"`
}

type githubStep struct {
	ID              string            `json:"id,omitempty"`
	Env             map[string]string `json:"env,omitempty"`
	Name            string            `json:"name,omitempty"`
	Uses            string            `json:"uses,omitempty"`
	Run             string            `json:"run,omitempty"`
	ContinueOnError bool              `json:"continue-on-error,omitempty"`
	If              string            `json:"if,omitempty"`
	With            map[string]string `json:"with,omitempty"`
	TimeoutMinutes  int               `json:"timeout-minutes,omitempty"`
}

// ValidateGitHubBundle performs local schema and semantic analysis only. It does not
// contact GitHub. The checks tie every job dependency back to an artifact producer,
// enforce pinned actions and offline build commands, and verify all generated inputs.
func ValidateGitHubBundle(ctx context.Context, plan *Plan, bundle *GeneratedBundle) error {
	workflow, err := validateGeneratedInputs(ctx, plan, bundle)
	if err != nil {
		return err
	}

	if err := validateGitHubWorkflowSize(workflow); err != nil {
		return err
	}
	expected, err := renderGitHub(ctx, plan, false)
	if err != nil {
		return err
	}
	expanded, err := expandGitHubActions(plan, workflow, bundle.Files)
	if err != nil {
		return err
	}
	if err := admitGitHubPlatforms(plan); err != nil {
		return err
	}
	actions, err := resolveGitHubActions(plan.Actions)
	if err != nil {
		return err
	}
	var document githubWorkflowDocument
	if err := yaml.UnmarshalStrict(expanded, &document); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "GitHub workflow schema is invalid")
	}
	if err := validateGitHubLocatorBudget(document, actions); err != nil {
		return err
	}
	if err := admitGitHubNeeds(ctx, plan, &document); err != nil {
		return err
	}
	if err := validateGitHubScriptLimits(document); err != nil {
		return err
	}
	if document.Name != githubWorkflowName(plan) || document.Permissions["contents"] != "read" || len(document.Permissions) != 1 {
		return e.New(e.CodeInvalidCatalog, "GitHub workflow name/triggers/root permissions are incomplete")
	}
	if _, ok := document.On["workflow_dispatch"]; !ok {
		return e.New(e.CodeInvalidCatalog, "GitHub workflow omits workflow_dispatch")
	}
	if len(document.On) != 1 || document.Concurrency.Group != "nvkr-run-${{ github.workflow }}-${{ github.ref }}" || document.Concurrency.CancelInProgress != githubCancelInProgress(plan) {
		return e.New(e.CodeEngineCapability, "GitHub Run requires manual dispatch only and concurrency matching its development policy")
	}
	if !strings.Contains(string(workflow), "# qualification: "+plan.Qualification.String()) ||
		!strings.Contains(string(workflow), "# plan: "+bundle.PlanPath) ||
		!strings.Contains(string(workflow), "# manifest: "+bundle.ManifestPath) {
		return e.New(e.CodeInvalidCatalog, "GitHub workflow omits static qualification or generated bundle references")
	}

	if len(plan.Environment.GitHubRunners) > 0 {
		for id, job := range document.Jobs {
			want, err := githubPlatformCheck(plan, job.RunsOn)
			if err != nil {
				return e.Wrap(e.CodeEngineCapability, err, "GitHub job %q has an unsupported native runner", id)
			}
			if want == "" || len(job.Steps) == 0 || job.Steps[0].Name != githubPlatformCheckName || strings.TrimSpace(job.Steps[0].Run) != want || job.Steps[0].Uses != "" || len(job.Steps[0].Env) != 0 {
				return e.New(e.CodeEngineCapability, "GitHub job %q omits its exact native-platform preflight", id)
			}
			job.Steps = job.Steps[1:]
			document.Jobs[id] = job
		}
	}
	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	stages, nodeJobs, err := githubStages(plan)
	if err != nil {
		return err
	}
	for _, stage := range stages {
		if stage.DiskOutput == "" {
			continue
		}
		job := document.Jobs[stage.ID]
		if len(job.Steps) == 0 || !reflect.DeepEqual(job.Steps[0], githubKVMAccessCheck()) {
			return e.New(e.CodeEngineCapability, "GitHub disk job %q must prepare KVM access before checkout or input transport", stage.ID)
		}
		job.Steps = job.Steps[1:]
		document.Jobs[stage.ID] = job
	}
	if err := validateGitHubCarrierUploads(plan, document, stages, artifacts, nodeJobs, actions); err != nil {
		return err
	}
	byNode := make(map[string]githubStage, len(plan.Graph.Nodes))
	for _, stage := range stages {
		for _, node := range stage.Nodes {
			byNode[node.ID] = stage
		}
	}
	bootstrap := map[string]string{}
	for _, node := range plan.Graph.Nodes {
		node, err := plan.GitHubExecutionFor(node)
		if err != nil {
			return err
		}
		bootstrap[node.RunsOn] = "build-runner-" + shortStableName(node.RunsOn)
	}
	// Summaries are exact, optional diagnostics outside execution and credential
	// admission. Remove only the two admitted steps before validating job work.
	startID := bootstrap[sortedKeys(bootstrap)[0]]
	start := document.Jobs[startID]
	if len(start.Steps) == 0 || !reflect.DeepEqual(start.Steps[0], githubRunStartSummary(plan)) {
		return e.New(e.CodeEngineCapability, "GitHub Run opening summary must precede bootstrap work")
	}
	start.Steps = start.Steps[1:]
	document.Jobs[startID] = start
	if len(plan.LifecycleOutputs) > 0 {
		finalizer := document.Jobs["nvkr-lifecycle-run"]
		if len(finalizer.Steps) == 0 || !reflect.DeepEqual(finalizer.Steps[len(finalizer.Steps)-1], githubRunEndSummary(len(stages)+len(bootstrap))) {
			return e.New(e.CodeEngineCapability, "GitHub Run closing summary must follow finalization and upload even after failure")
		}
		finalizer.Steps = finalizer.Steps[:len(finalizer.Steps)-1]
		document.Jobs["nvkr-lifecycle-run"] = finalizer
	}
	expectedTimeouts := make(map[string]int, len(stages)+len(bootstrap)+1)
	for _, stage := range stages {
		timeoutMinutes, err := githubStageTimeoutMinutes(stage)
		if err != nil {
			return err
		}
		expectedTimeouts[stage.ID] = timeoutMinutes
	}
	for _, jobID := range bootstrap {
		expectedTimeouts[jobID] = githubSupportJobTimeoutMinutes
	}
	if len(plan.LifecycleOutputs) > 0 {
		expectedTimeouts["nvkr-lifecycle-run"] = githubFinalizerTimeoutMinutes(plan)
	}
	expectedJobCount := len(stages) + len(bootstrap)
	if len(plan.LifecycleOutputs) > 0 {
		expectedJobCount++
	}
	if len(document.Jobs) != expectedJobCount {
		return e.New(e.CodeInvalidCatalog, "GitHub workflow has %d jobs; artifact graph requires %d", len(document.Jobs), expectedJobCount)
	}
	sourceEnvironments := make(map[string]map[string]string)
	for _, node := range plan.Graph.Nodes {
		node, err := plan.GitHubExecutionFor(node)
		if err != nil {
			return err
		}
		sourceEnvironments[nodeJobs[node.ID]] = githubSourceEnvironment(plan, node)
	}
	signJobs := make(map[string]bool, len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		node, err := plan.GitHubExecutionFor(node)
		if err != nil {
			return err
		}
		if node.Kind == NodeSign || node.Kind == NodeAttestEvidence {
			signJobs[nodeJobs[node.ID]] = true
		}
	}
	catalogEnv := githubCatalogSourceEnvironment(plan)
	for jobID, job := range document.Jobs {
		if plan.Environment.GitHubDevelopment && job.Permissions["packages"] == "write" {
			return e.New(e.CodeEngineCapability, "development job %q cannot grant workflow-token package writes", jobID)
		}
		for key := range job.Env {
			if strings.HasPrefix(key, "NVKR_PIN_SOURCE_TOKEN_") {
				return e.New(e.CodeEngineCapability, "catalog credential cannot be job-scoped")
			}
		}
		if _, present := job.Env[sourceTokenEnv]; present {
			return e.New(e.CodeEngineCapability, "GitHub source credential must be scoped to its fetch step")
		}
		if _, present := job.Env[githubImageToken]; present {
			return e.New(e.CodeEngineCapability, "private image credential cannot be job-scoped")
		}
		wantSourceEnv := sourceEnvironments[jobID]
		foundSource := false
		for _, step := range job.Steps {
			if len(step.Env) == 0 {
				continue
			}
			if stage, ok := byNodeForJob(stages, jobID); ok && len(githubStagePrivateImages(plan, stage)) > 0 &&
				strings.TrimSpace(step.Run) == githubRunnerCommand(plan, "prepare-images", "stage", stage.ID) {
				if !reflect.DeepEqual(step.Env, githubImageEnvironment()) || step.If != "" || step.ContinueOnError || step.Uses != "" {
					return e.New(e.CodeEngineCapability, "private image preparation requires only its exact read credential")
				}
				continue
			}
			if jobID == "nvkr-lifecycle-run" && step.ID == "recover-artifacts" {
				if !reflect.DeepEqual(step.Env, map[string]string{"NVKR_JOB_RESULTS": "${{ toJSON(needs) }}"}) || strings.TrimSpace(step.Run) != githubRunnerCommand(plan, "recover", "", "") || step.ContinueOnError || step.If != "" || step.Uses != "" {
					return e.New(e.CodeEngineCapability, "GitHub recovery must receive only exact provider job results")
				}
				continue
			}
			if len(catalogEnv) > 0 && ((strings.HasPrefix(jobID, "build-runner-") && step.ID == "validate-catalog") || (jobID == "nvkr-lifecycle-run" && step.ID == "finalize-catalog")) {
				if !reflect.DeepEqual(step.Env, catalogEnv) {
					return e.New(e.CodeEngineCapability, "catalog source environment differs")
				}
				for key := range catalogEnv {
					if strings.HasPrefix(key, "NVKR_PIN_SOURCE_TOKEN_") && !strings.Contains(step.Run, strings.ReplaceAll(requireSourceToken, sourceTokenEnv, key)) {
						return e.New(e.CodeEngineCapability, "catalog validation must reject an empty source credential")
					}
				}
				continue
			}
			sourceStage, sourceJob := byNodeForJob(stages, jobID)
			if len(wantSourceEnv) == 0 || !sourceJob || len(sourceStage.Nodes) != 1 ||
				step.ID != githubJobID(sourceStage.Nodes[0].ID) || len(step.Env) != len(wantSourceEnv) {
				return e.New(e.CodeEngineCapability, "GitHub job %q has unexpected step credentials or environment", jobID)
			}
			for key, value := range wantSourceEnv {
				if got, exists := step.Env[key]; !exists || got != value {
					return e.New(e.CodeEngineCapability, "GitHub job %q source environment differs at %s", jobID, key)
				}
			}
			if strings.TrimSpace(step.Run) != githubRunnerCommand(plan, "run-node", "node", sourceStage.Nodes[0].ID) {
				return e.New(e.CodeEngineCapability, "GitHub job %q must refuse an empty source credential", jobID)
			}
			foundSource = true
		}
		if len(wantSourceEnv) > 0 && !foundSource {
			return e.New(e.CodeEngineCapability, "GitHub job %q omits source authentication", jobID)
		}
		wantTimeout, ok := expectedTimeouts[jobID]
		if !ok || job.TimeoutMinutes != wantTimeout {
			return e.New(e.CodeEngineCapability,
				"GitHub job %q timeout is %d minutes; expected %d",
				jobID, job.TimeoutMinutes, wantTimeout)
		}
		if jobID == "nvkr-lifecycle-run" && job.If != "${{ always() && "+githubDispatchCondition(plan)+" }}" {
			return e.New(e.CodeEngineCapability, "GitHub Run finalization must follow the admitted execution event even after failure")
		}
		// An OIDC identity token is the credential a keyless signature is minted from.
		// Only the signing job may request one; anywhere else it is an ambient
		// authority the pipeline never needs and must not be granted.
		if token, ok := job.Permissions["id-token"]; ok {
			if !signJobs[jobID] {
				return e.New(e.CodeEngineCapability,
					"GitHub job %q requests id-token %q but is not a signing job", jobID, token)
			}
			if token != "write" {
				return e.New(e.CodeEngineCapability,
					"GitHub signing job %q requests id-token %q, not write", jobID, token)
			}
		}
		for _, step := range job.Steps {
			if step.Uses == actions.Checkout && step.With["persist-credentials"] != "false" {
				return e.New(e.CodeEngineCapability, "GitHub job %q permits checkout to persist ambient credentials", jobID)
			}
			if step.Uses == actions.Upload && step.With["if-no-files-found"] != "error" {
				if stage, ok := byNodeForJob(stages, jobID); ok && stage.DiskOutput != "" && githubDiskInspectionRequired(plan) &&
					reflect.DeepEqual(step, githubInspectionDiagnosticStep(actions, stage.DiskOutput)) {
					continue
				}
				return e.New(e.CodeEngineCapability, "GitHub job %q permits a missing declared output", jobID)
			}
		}
	}
	for runner, jobID := range bootstrap {
		job, ok := document.Jobs[jobID]
		wantSteps := 5
		command := generationCommand(plan, EngineGitHub)
		if plan.Environment.GitHubRuntimePlan {
			wantSteps = 6
			if runner == sortedKeys(bootstrap)[0] {
				command = githubRuntimeGenerationCommand(plan)
			} else {
				wantSteps = 7
				if !strings.Contains(joinRuns(job.Steps), githubRuntimeInstallCommand(plan)) {
					return e.New(e.CodeInvalidCatalog, "secondary GitHub bootstrap omits runtime input installation")
				}
			}
		}
		wantCondition := "${{ " + githubDispatchCondition(plan) + " }}"
		if !ok || job.RunsOn != githubBootstrapRunner(runner) || job.If != wantCondition || len(job.Steps) != wantSteps || job.Steps[0].Uses != actions.Checkout {
			return e.New(e.CodeInvalidCatalog, "GitHub runner bootstrap %q is incomplete", jobID)
		}
		// The bootstrap must provision the Go toolchain via the pinned setup-go action rather
		// than assume a preinstalled Go, and pin the version to the repo's .go-version so the
		// toolchain and setup-go cannot drift.
		if job.Steps[1].Uses != actions.SetupGo || job.Steps[1].With["go-version-file"] != ".go-version" ||
			job.Steps[1].With["cache"] != "false" {
			return e.New(e.CodeInvalidCatalog, "GitHub runner bootstrap %q does not provision the pinned Go toolchain from .go-version", jobID)
		}
		bootstrapRuns := joinRuns(job.Steps)
		linkerFlags, err := generatorLinkerFlags(plan.GeneratorVersion)
		if err != nil {
			return err
		}
		if !stepUses(job.Steps, actions.Upload) || !strings.Contains(bootstrapRuns, "go build -trimpath") ||
			!strings.Contains(bootstrapRuns, "-ldflags "+shellQuote(linkerFlags)) ||
			!strings.Contains(bootstrapRuns, command) {
			return e.New(e.CodeInvalidCatalog, "GitHub runner bootstrap %q does not build and transport the runner", jobID)
		}
	}
	for _, node := range plan.Graph.Nodes {
		node, err := plan.GitHubExecutionFor(node)
		if err != nil {
			return err
		}
		stage := byNode[node.ID]
		jobID := stage.ID
		job, ok := document.Jobs[jobID]
		wantEnvironment := ""
		if stage.DiskOutput != "" {
			wantEnvironment = githubARCEnvironmentFor(plan)
		}
		if githubProtectedPublication(plan, node) {
			wantEnvironment = githubReleaseEnvironmentFor(plan)
		}
		if job.Environment != wantEnvironment {
			return e.New(e.CodeEngineCapability, "GitHub job %q deployment environment must be %q", jobID, wantEnvironment)
		}
		if !ok || job.Name != stage.Name || job.RunsOn != node.RunsOn {
			return e.New(e.CodeInvalidCatalog, "GitHub workflow job for artifact node %q is missing or misbound", node.ID)
		}
		expectedNeeds := stage.needs(artifacts, nodeJobs, bootstrap[node.RunsOn])
		actualNeeds := append([]string(nil), job.Needs...)
		sort.Strings(actualNeeds)
		if strings.Join(actualNeeds, "\x00") != strings.Join(expectedNeeds, "\x00") {
			return e.New(e.CodeInvalidCatalog, "GitHub job %q needs %v; artifact dataflow requires %v", jobID, actualNeeds, expectedNeeds)
		}
		if len(job.Steps) == 0 || job.Steps[0].Uses != actions.Checkout || !stepUses(job.Steps, actions.Download) {
			return e.New(e.CodeInvalidCatalog, "GitHub job %q omits pinned checkout or input transport", jobID)
		}
		if err := actions.validateHelperPreparedFirst(jobID, job.Steps, plan); err != nil {
			return err
		}
		for _, step := range job.Steps {
			if step.Uses != "" && step.Uses != actions.Checkout && step.Uses != actions.Upload && step.Uses != actions.Download {
				return e.New(e.CodeInvalidCatalog, "GitHub job %q uses unapproved or unpinned action %q", jobID, step.Uses)
			}
		}
		if err := validateGitHubStageExecution(plan, stage, job.Steps); err != nil {
			return err
		}
		if len(githubStagePrivateImages(plan, stage)) > 0 && !reflect.DeepEqual(job.Permissions, map[string]string{"contents": "read"}) {
			return e.New(e.CodeEngineCapability, "private toolchain jobs require exactly contents:read")
		}
		wantCondition := stage.condition(plan)
		if job.If != wantCondition {
			return e.New(e.CodeEngineCapability, "GitHub execution job %q must require dispatch on an admitted branch", jobID)
		}
		repositoryToken, repositoryTokenPresent := job.Env[githubRepositoryTokenEnv]
		if want := githubPublicationRepositoryToken(plan, node); repositoryToken != want || repositoryTokenPresent != (want != "") {
			return e.New(e.CodeEngineCapability, "GitHub job %q repository metadata credential differs from its publication scope", jobID)
		}
		if githubScopedPublication(plan) {
			if err := validateGitHubRegistryEnvironment(plan, node.Kind, node.Target, jobID, job); err != nil {
				return err
			}
		}
		if node.Kind == NodePublish {
			target := plan.Targets[node.Target]
			if !githubScopedPublication(plan) && strings.HasPrefix(target.Ref, "ghcr.io/") && job.Permissions["packages"] != "write" {
				return e.New(e.CodeEngineCapability, "GitHub publish job %q lacks explicit packages:write", jobID)
			}
			if !strings.HasPrefix(target.Ref, "ghcr.io/") && job.Permissions["packages"] != "" {
				return e.New(e.CodeEngineCapability, "GitHub anonymous publish job %q has unnecessary packages permission", jobID)
			}
			if target.CredentialRef != "" && node.Kind != NodeAttestEvidence && job.Env["NVKR_CREDENTIAL"] == "" {
				return e.New(e.CodeEngineCapability, "GitHub publish job %q omits its credential reference", jobID)
			}
		}
		if signFamilyNode(node.Kind) {
			if plan.Environment.Signing == nil {
				return e.New(e.CodeTrustPolicyMissing,
					"GitHub signing job %q has no delivery signing profile", jobID)
			}
			if node.Kind == NodeAttestEvidence && (job.Env["NVKR_CREDENTIAL"] != "" || job.Env["NVKR_REGISTRY_USER"] != "") {
				return e.New(e.CodeEngineCapability, "SBOM attestation job must not receive registry credentials")
			}
			expected := githubSigningPermissionsFor(plan, node.Kind)
			if len(job.Permissions) != len(expected) {
				return e.New(e.CodeEngineCapability,
					"GitHub signing job %q holds %d permissions; %s signing requires exactly %v",
					jobID, len(job.Permissions), plan.Environment.Signing.Mode, expected)
			}
			for name, value := range expected {
				if job.Permissions[name] != value {
					return e.New(e.CodeEngineCapability,
						"GitHub signing job %q has %s: %q, want %q", jobID, name, job.Permissions[name], value)
				}
			}
			target := plan.Targets[node.Target]
			if target.CredentialRef != "" && node.Kind != NodeAttestEvidence && job.Env["NVKR_CREDENTIAL"] == "" {
				return e.New(e.CodeEngineCapability, "GitHub signing job %q omits its credential reference", jobID)
			}
		}
		for _, output := range githubCarrierOutputs(plan, node, artifacts, nodeJobs) {
			if !hasExactGitHubRun(job.Steps, githubRunnerCommand(plan, "pack-outputs", "node", node.ID)) ||
				!actions.stepUploads(job.Steps, githubArtifactName(output), carrierPath(output)) {
				return e.New(e.CodeEngineCapability, "GitHub job %q does not transport output %q", jobID, output)
			}
		}
		carrierInputs, err := stage.carrierInputs(plan, artifacts, nodeJobs)
		if err != nil {
			return err
		}
		for _, input := range carrierInputs {
			if !hasExactGitHubRun(job.Steps, githubRunnerCommand(plan, "restore-inputs", "stage", stage.ID)) {
				return e.New(e.CodeEngineCapability, "GitHub job %q does not restore exact input %q", jobID, input)
			}
		}
	}
	if len(plan.LifecycleOutputs) > 0 {
		job := document.Jobs["nvkr-lifecycle-run"]
		if PerOutputPublication(plan) {
			permissions := map[string]string{"contents": "read"}
			if !githubScopedPublication(plan) {
				permissions["packages"] = "read"
			}
			if job.Environment != githubReleaseEnvironmentFor(plan) || !reflect.DeepEqual(job.Permissions, permissions) {
				return e.New(e.CodeEngineCapability, "GitHub finalizer requires its exact protected read-only publication scope")
			}
			if err := validateGitHubRegistryEnvironment(plan, NodeVerifyPublication, "release", "nvkr-lifecycle-run", job); err != nil {
				return err
			}
		}
		var expected bytes.Buffer
		if err := writeGitHubFinalizerRestoreSteps(&expected, plan, nodeJobs); err != nil {
			return err
		}
		var restore struct {
			Steps []githubStep `json:"steps"`
		}
		if err := yaml.UnmarshalStrict([]byte("steps:\n"+expected.String()), &restore); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "GitHub finalizer restore schema")
		}
		expectedSteps := 3 + len(restore.Steps) + 2
		if githubQualificationHandoffEnabled(plan) {
			expectedSteps++
		}
		if len(job.Steps) != expectedSteps || !reflect.DeepEqual(job.Steps[3:3+len(restore.Steps)], restore.Steps) {
			return e.New(e.CodeEngineCapability, "GitHub finalizer must restore every exact planned carrier and record its producer outcome")
		}
		if githubQualificationHandoffEnabled(plan) && !reflect.DeepEqual(job.Steps[len(job.Steps)-1], githubQualificationHandoffStep(actions)) {
			return e.New(e.CodeEngineCapability, "GitHub finalizer must upload only the complete handoff for this successful exact attempt")
		}
		if err := actions.validateHelperPreparedFirst("nvkr-lifecycle-run", job.Steps, plan); err != nil {
			return err
		}
		var needs []string
		for _, id := range nodeJobs {
			needs = append(needs, id)
		}
		for _, id := range bootstrap {
			needs = append(needs, id)
		}
		if !reflect.DeepEqual(uniqueSorted(job.Needs), uniqueSorted(needs)) {
			return e.New(e.CodeEngineCapability, "GitHub finalizer must await every producer stage")
		}
	}
	// Semantic checks above produce useful coded diagnostics. Exact regeneration
	// additionally rejects unrecognized steps, appended shell commands and ambient
	// authority on any job; substring checks alone cannot authorize an entire job.
	if len(bundle.Files) != len(expected.Files) {
		return e.New(e.CodePipelineDrift, "GitHub bundle member set differs from its admitted rendering")
	}
	for name := range bundle.Files {
		if _, ok := expected.Files[name]; !ok {
			return e.New(e.CodePipelineDrift, "GitHub bundle contains unexpected member %q", name)
		}
	}
	for _, name := range sortedFileNames(expected.Files) {
		if !bytes.Equal(bundle.Files[name], expected.Files[name]) {
			return e.New(e.CodeEngineCapability, "GitHub member %q differs from its exact admitted rendering", name)
		}
	}
	return nil
}

func validateGitHubRegistryEnvironment(plan *Plan, kind, target, jobID string, job githubJob) error {
	expected, err := githubRegistryEnvironment(plan, kind, target)
	if err != nil {
		return err
	}
	for _, key := range []string{"NVKR_CREDENTIAL", "NVKR_REGISTRY_USER"} {
		want, wanted := expected[key]
		got, present := job.Env[key]
		if got != want || wanted != present {
			return e.New(e.CodeEngineCapability, "GitHub job %q registry credential scope differs at %s", jobID, key)
		}
	}
	return nil
}

// validateHelperPreparedFirst rejects a job that invokes the downloaded helper before
// restoring its execute bit. download-artifact drops the execute bit, so ./nvkr-exec is
// not runnable until the dedicated preparation step runs; because the input Restore steps
// are themselves helper invocations, the preparation must come immediately after the
// helper download and strictly before the FIRST ./nvkr-exec in the job.
func (actions githubActionRefs) validateHelperPreparedFirst(jobID string, steps []githubStep, plans ...*Plan) error {
	wantRun := prepareHelperRun
	if len(plans) != 0 {
		wantRun = prepareHelperRunFor(plans[0])
	}
	prepared := -1
	for index, step := range steps {
		if step.Name == prepareHelperStepName && strings.TrimSpace(step.Run) == wantRun {
			prepared = index
			break
		}
	}
	if prepared < 0 {
		return e.New(e.CodeEngineCapability,
			"GitHub job %q has no %q step; the transported helper arrives without its execute bit", jobID, prepareHelperStepName)
	}
	if prepared == 0 || steps[prepared-1].Uses != actions.Download {
		return e.New(e.CodeEngineCapability,
			"GitHub job %q does not prepare the helper immediately after downloading it", jobID)
	}
	for index, step := range steps {
		if index >= prepared {
			break
		}
		if strings.Contains(step.Run, "./nvkr-exec") {
			return e.New(e.CodeEngineCapability,
				"GitHub job %q invokes ./nvkr-exec at step %d, before it is made executable at step %d", jobID, index, prepared)
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

// Validate transport against the admitted graph, not by parsing provider pattern
// syntax. Every restored carrier has exactly one uploader in its producer job.
// Raw disk subjects are deliberately absent: their selected wrapper and producer
// record are mandatory carrier inputs at the shared projection boundary.
func validateGitHubCarrierUploads(plan *Plan, document githubWorkflowDocument, stages []githubStage,
	artifacts map[string]Artifact, nodeJobs map[string]string, actions githubActionRefs) error {
	uploads := map[string][]string{}
	patterns := map[string]string{}
	for jobID, job := range document.Jobs {
		for _, step := range job.Steps {
			if step.Uses == actions.Upload {
				uploads[step.With["name"]] = append(uploads[step.With["name"]], jobID)
			}
		}
	}
	require := func(id string) error {
		jobs := uploads[githubArtifactName(id)]
		if len(jobs) != 1 || jobs[0] != nodeJobs[artifacts[id].Producer] {
			return e.New(e.CodeEngineCapability, "GitHub restored carrier %q requires exactly one uploader in its producer job", id)
		}
		return nil
	}
	for _, stage := range stages {
		inputs, err := stage.carrierInputs(plan, artifacts, nodeJobs)
		if err != nil {
			return err
		}
		for _, id := range inputs {
			if err := require(id); err != nil {
				return err
			}
		}
		if len(inputs) > 0 {
			selected := make([]Artifact, len(inputs))
			for index, id := range inputs {
				selected[index] = artifacts[id]
			}
			patterns[stage.ID], err = githubCarrierPattern(selected)
			if err != nil {
				return err
			}
		}
	}
	if len(plan.LifecycleOutputs) > 0 {
		var carriers []Artifact
		for _, artifact := range plan.Graph.Artifacts {
			if finalizerArtifact(plan, artifact) {
				carriers = append(carriers, artifact)
			}
			if _, raw := diskPublicationRequirement(plan, artifact); raw {
				continue
			}
			if !githubArtifactNeedsCarrier(plan, artifact, nodeJobs) {
				continue
			}
			if err := require(artifact.ID); err != nil {
				return err
			}
		}
		var err error
		patterns["nvkr-lifecycle-run"], err = githubCarrierPattern(carriers)
		if err != nil {
			return err
		}
	}
	for jobID, job := range document.Jobs {
		for _, step := range job.Steps {
			if step.Uses != actions.Download {
				continue
			}
			if pattern := step.With["pattern"]; pattern != "" {
				if step.With["name"] != "" || pattern != patterns[jobID] {
					return e.New(e.CodeEngineCapability, "GitHub job %q downloads a carrier pattern outside its admitted graph", jobID)
				}
				continue
			}
			if step.With["name"] == "" {
				continue
			}
			if len(uploads[step.With["name"]]) != 1 {
				return e.New(e.CodeEngineCapability, "GitHub job %q downloads %q without exactly one uploader", jobID, step.With["name"])
			}
		}
	}
	return nil
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

func (actions githubActionRefs) stepUploads(steps []githubStep, name, path string) bool {
	for _, step := range steps {
		if step.Uses == actions.Upload && step.With["name"] == name && step.With["path"] == path && step.With["if-no-files-found"] == "error" {
			return true
		}
	}
	return false
}

func (document githubWorkflowDocument) String() string {
	return fmt.Sprintf("%s (%d jobs)", document.Name, len(document.Jobs))
}

// Every logical node retains one exact, unconditional execution step in graph
// order even when scheduling shares a runner. Another node's command cannot
// satisfy this node's isolation or identity checks.
func validateGitHubStageExecution(plan *Plan, stage githubStage, steps []githubStep) error {
	if len(githubStagePrivateImages(plan, stage)) > 0 {
		prepared := false
		for _, step := range steps {
			if strings.TrimSpace(step.Run) == githubRunnerCommand(plan, "prepare-images", "stage", stage.ID) {
				if prepared || !reflect.DeepEqual(step.Env, githubImageEnvironment()) {
					return e.New(e.CodeEngineCapability, "private toolchain preparation must occur exactly once with scoped credentials")
				}
				prepared = true
			}
			if (strings.Contains(step.Run, "factory github run-node ") || strings.Contains(step.Run, "factory github run-disk ")) && !prepared {
				return e.New(e.CodeEngineCapability, "private toolchain preparation must precede producer execution")
			}
		}
		if !prepared {
			return e.New(e.CodeEngineCapability, "private toolchain preparation is missing")
		}
	}
	if stage.DiskOutput != "" {
		executors, diagnostics := 0, 0
		actions, err := resolveGitHubActions(plan.Actions)
		if err != nil {
			return err
		}
		var expected bytes.Buffer
		writeGitHubPackerDiagnosticStep(&expected, stage, actions)
		var diagnostic struct {
			Steps []githubStep `json:"steps"`
		}
		if err := yaml.UnmarshalStrict([]byte("steps:\n"+expected.String()), &diagnostic); err != nil {
			return e.Wrap(e.CodeInternal, err, "decode fixed Packer diagnostic transport")
		}
		for i, step := range steps {
			if step.ID == "run-disk" {
				if executors != 0 || step.Run != githubRunnerCommand(plan, "run-disk", "output", stage.DiskOutput) || step.If != "" || step.Uses != "" || step.ContinueOnError || len(step.Env) != 0 || step.TimeoutMinutes != githubDiskStepTimeoutMinutes(stage) {
					return e.New(e.CodeEngineCapability, "disk stage requires its bounded unconditional live executor")
				}
				executors++
			}
			if step.Name == "Upload failed Packer diagnostics" {
				if diagnostics != 0 || i == 0 || steps[i-1].ID != "run-disk" || !reflect.DeepEqual(step, diagnostic.Steps[0]) {
					return e.New(e.CodeEngineCapability, "disk diagnostics require the exact failure-only sealed route transport")
				}
				diagnostics++
			}
		}
		if executors != 1 || diagnostics != 1 {
			return e.New(e.CodeEngineCapability, "disk stage requires one live executor and one failure diagnostic transport")
		}
		return nil
	}
	next := 0
	for _, step := range steps {
		if step.ID == "" {
			continue
		}
		if next >= len(stage.Nodes) {
			return e.New(e.CodeEngineCapability, "GitHub stage %q has an unexpected execution step", stage.ID)
		}
		node := stage.Nodes[next]
		commands := githubNodeCommands(node, plan)
		if step.ID != githubJobID(node.ID) || step.Name != githubNodeLabel(plan, node) || step.ContinueOnError || step.If != "" || step.Uses != "" ||
			strings.TrimSpace(step.Run) != strings.Join(commands, "\n") {
			return e.New(e.CodeEngineCapability, "GitHub stage %q must execute exact node %q in graph order without skipping failures", stage.ID, node.ID)
		}
		next++
	}
	if next != len(stage.Nodes) {
		return e.New(e.CodeEngineCapability, "GitHub stage %q omits a logical node execution", stage.ID)
	}
	return nil
}

// GitHub limits each run value to 21,000 characters. Bound UTF-8 bytes
// conservatively, including its final newline, before any bundle is returned.
// https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#jobsjob_idstepsrun
const githubMaximumRunBytes = 21000

func validateGitHubScriptLimits(document githubWorkflowDocument) error {
	for _, id := range sortedKeys(document.Jobs) {
		for _, step := range document.Jobs[id].Steps {
			if len(step.Run) > githubMaximumRunBytes {
				return e.New(e.CodeEngineCapability, "GitHub job %q step %q exceeds the %d-byte run-script limit", id, step.Name, githubMaximumRunBytes)
			}
		}
	}
	return nil
}

func byNodeForJob(stages []githubStage, id string) (githubStage, bool) {
	for _, stage := range stages {
		if stage.ID == id {
			return stage, true
		}
	}
	return githubStage{}, false
}

func hasExactGitHubRun(steps []githubStep, command string) bool {
	for _, step := range steps {
		if strings.TrimSpace(step.Run) == command && step.Uses == "" && step.If == "" && !step.ContinueOnError && len(step.Env) == 0 {
			return true
		}
	}
	return false
}
