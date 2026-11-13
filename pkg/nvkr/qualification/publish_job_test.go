// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationoutcome"
	yaml "go.yaml.in/yaml/v3"
)

func setQualificationResults(t *testing.T, plan *factory.Plan, results *definition.QualificationResults) {
	t.Helper()
	plan.Environment.QualificationResults = results
	var err error
	if plan.EnvironmentDigest, err = plan.Environment.Digest(); err != nil {
		t.Fatal(err)
	}
}

func withoutQualificationResults(t *testing.T, plan *factory.Plan) {
	t.Helper()
	setQualificationResults(t, plan, nil)
}

func testQualificationResults() *definition.QualificationResults {
	return &definition.QualificationResults{Mode: definition.QualificationResultsModeTestGridGCS, GitHubEnvironment: "nvkr-results",
		BucketRef: "github:TEST_RESULTS_BUCKET", WorkloadIdentityProviderRef: "github:TEST_RESULTS_PROVIDER", ServiceAccountRef: "github:TEST_RESULTS_ACCOUNT"}
}

// publishWorkflowJob decodes the fields only the publish job sets.
type publishWorkflowJob struct {
	Environment     string `yaml:"environment"`
	ContinueOnError bool   `yaml:"continue-on-error"`
}

func TestQualificationGenerationPublishJob(t *testing.T) {
	plan, policy := generationFixture(t)
	setQualificationResults(t, plan, testQualificationResults())
	bundle, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	again, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
	if err != nil || !reflect.DeepEqual(bundle.Files(), again.Files()) {
		t.Fatalf("nondeterministic generation: %v", err)
	}
	raw := bundle.files[policy.QualificationWorkflow]
	top := decodeQualificationWorkflow(t, raw)
	if len(top.Jobs) != 3 {
		t.Fatalf("jobs are %v", top.Jobs)
	}
	job := top.Jobs["publish"]
	var extra struct {
		Jobs map[string]publishWorkflowJob `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &extra); err != nil {
		t.Fatal(err)
	}
	// Its own Environment and only an OIDC identity: never the qualify job's
	// Environment, which holds the registry write credential.
	if extra.Jobs["publish"].Environment != "nvkr-results" || !extra.Jobs["publish"].ContinueOnError || extra.Jobs["qualify"].ContinueOnError {
		t.Fatalf("publish job Environment or failure policy is %#v", extra.Jobs["publish"])
	}
	if !reflect.DeepEqual(job.Needs, []string{"native-smoke", "qualify"}) || job.RunsOn != "ubuntu-24.04" || job.Timeout != 15 || len(job.Env) != 0 || len(job.Outputs) != 0 ||
		!reflect.DeepEqual(job.Permissions, map[string]string{"contents": "read", "id-token": "write"}) {
		t.Fatalf("publish job is %#v", job)
	}
	names := make([]string, 0, len(job.Steps))
	for _, step := range job.Steps {
		names = append(names, step.Name)
		if strings.Contains(step.Run+strings.Join(mapValues(step.Env), " "), "NVKR_REGISTRY") {
			t.Fatalf("step %q reaches a registry credential", step.Name)
		}
	}
	want := []string{"Admit trusted same-commit workflow", "Check out trusted qualifier commit", "Set up pinned Go", "Build trusted publisher",
		"Download the qualification outcomes", "Select the outcomes to publish", "Upload the built outcome", "Authenticate to the results store", "Publish the outcomes"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("steps are %v", names)
	}
	actions, err := pipeline.ResolveGitHubActions(plan.Actions)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := pipeline.ResolveGitHubAuthAction(plan.Actions)
	if err != nil {
		t.Fatal(err)
	}
	build, download, selectStep, upload, authStep, publish := job.Steps[3], job.Steps[4], job.Steps[5], job.Steps[6], job.Steps[7], job.Steps[8]
	if build.Run != "go build -trimpath -o nvkr-results-publish ./cmd/nvkr-results-publish" || build.Env["GOTOOLCHAIN"] != "local" {
		t.Fatalf("build step is %#v", build)
	}
	const outcomes = "${{ runner.temp }}/nvkr-qualification-outcomes"
	if download.Uses != actions.Download || !reflect.DeepEqual(download.With, map[string]string{"pattern": qualificationoutcome.ArtifactPrefix + "${{ github.run_id }}-*", "merge-multiple": "true", "path": outcomes}) {
		t.Fatalf("download step is %#v", download)
	}
	report := map[string]string{"OUTCOMES": outcomes, "QUALIFY_RESULT": "${{ needs.qualify.result }}",
		"QUALIFY_ATTEMPT": "${{ needs.qualify.outputs.qualify-attempt }}", "OUTCOME_WRITTEN": "${{ needs.qualify.outputs.outcome-written }}"}
	selection := `--outcomes "$OUTCOMES" --event "$GITHUB_EVENT_PATH" --source-workflow '.github/workflows/nvkr-run.yml' --source-branch 'main' --qualify-workflow '` + policy.QualificationWorkflow +
		`' --qualify-result "$QUALIFY_RESULT" --qualify-attempt "$QUALIFY_ATTEMPT" --outcome-written "$OUTCOME_WRITTEN"`
	if selectStep.ID != "select" || !reflect.DeepEqual(selectStep.Env, report) ||
		!strings.HasSuffix(strings.TrimSpace(selectStep.Run), "./nvkr-results-publish select "+selection+` --write "$OUTCOMES/built-$GITHUB_RUN_ATTEMPT.json" >> "$GITHUB_OUTPUT"`) {
		t.Fatalf("select step is %#v", selectStep)
	}
	// The built outcome's artifact must match the download pattern, so a re-run
	// publishes it instead of building another.
	if upload.If != "steps.select.outputs.built == 'true'" || upload.Uses != actions.Upload || upload.ContinueOnError ||
		upload.With["name"] != qualificationoutcome.ArtifactPrefix+"${{ github.run_id }}-${{ github.run_attempt }}-built" || upload.With["if-no-files-found"] != "error" ||
		upload.With["path"] != outcomes+"/built-${{ github.run_attempt }}.json" {
		t.Fatalf("upload step is %#v", upload)
	}
	if authStep.ID != "auth" || authStep.Uses != auth || !reflect.DeepEqual(authStep.With, map[string]string{
		"workload_identity_provider": "${{ secrets.TEST_RESULTS_PROVIDER }}", "service_account": "${{ secrets.TEST_RESULTS_ACCOUNT }}",
		"create_credentials_file": "true", "export_environment_variables": "false"}) {
		t.Fatalf("auth step is %#v", authStep)
	}
	if !bytes.Contains(raw, []byte("uses: "+auth+" # "+plan.Actions["google-auth"].Version+"\n")) {
		t.Fatal("the auth action is not hash-pinned with its version comment")
	}
	publishEnv := map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "${{ steps.auth.outputs.credentials_file_path }}", "NVKR_RESULTS_BUCKET": "${{ secrets.TEST_RESULTS_BUCKET }}"}
	for key, value := range report {
		publishEnv[key] = value
	}
	if !reflect.DeepEqual(publish.Env, publishEnv) ||
		!strings.HasSuffix(strings.TrimSpace(publish.Run), "./nvkr-results-publish publish "+selection+" --group 'nvke' --definition 'nvke-m2'") {
		t.Fatalf("publish step is %#v", publish)
	}
}

func mapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func TestQualificationGenerationPublishCondition(t *testing.T) {
	plan, policy := generationFixture(t)
	setQualificationResults(t, plan, testQualificationResults())
	bundle, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
	if err != nil {
		t.Fatal(err)
	}
	top := decodeQualificationWorkflow(t, bundle.files[policy.QualificationWorkflow])
	trusted := "github.event_name == 'workflow_run' && github.repository == 'dsx-ai-factory/nvkr' && github.event.workflow_run.head_branch == 'main' && " +
		"github.event.workflow_run.repository.full_name == github.repository && github.event.workflow_run.head_repository.full_name == github.repository"
	want := "${{ !cancelled() && " + trusted + //nolint:misspell // GitHub status function uses this exact spelling.
		" && github.event.workflow_run.path == '.github/workflows/nvkr-run.yml'" +
		` && contains(fromJSON('["push","workflow_dispatch"]'), github.event.workflow_run.event)` +
		` && contains(fromJSON('["failure","startup_failure","success","timed_out"]'), github.event.workflow_run.conclusion) }}`
	if top.Jobs["publish"].If != want {
		t.Fatalf("publish condition is\n%s\nwant\n%s", top.Jobs["publish"].If, want)
	}
	// Smoke and qualify still require a producer that succeeded.
	for _, name := range []string{"native-smoke", "qualify"} {
		if !strings.Contains(top.Jobs[name].If, "github.event.workflow_run.conclusion == 'success' && "+strings.TrimPrefix(trusted, "github.event_name == 'workflow_run' && ")) {
			t.Fatalf("%s no longer requires a successful producer: %s", name, top.Jobs[name].If)
		}
	}
}

func TestQualificationGenerationNoPublish(t *testing.T) {
	t.Run("no target", func(t *testing.T) {
		plan, policy := generationFixture(t)
		withoutQualificationResults(t, plan)
		bundle, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
		if err != nil {
			t.Fatal(err)
		}
		raw := bundle.files[policy.QualificationWorkflow]
		top := decodeQualificationWorkflow(t, raw)
		if len(top.Jobs) != 2 || bytes.Contains(raw, []byte("qualify-attempt")) || bytes.Contains(raw, []byte("attempt=")) {
			t.Fatal("a qualifier without a results target publishes or reports its attempt")
		}
	})
	t.Run("development", func(t *testing.T) {
		plan, policy := developmentGenerationFixture(t)
		bundle, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := decodeQualificationWorkflow(t, bundle.files[policy.QualificationWorkflow]).Jobs["publish"]; ok {
			t.Fatal("development qualification publishes")
		}
	})
	t.Run("missing auth action", func(t *testing.T) {
		plan, policy := generationFixture(t)
		setQualificationResults(t, plan, testQualificationResults())
		delete(plan.Actions, "google-auth")
		if _, err := RenderWorkflows(t.Context(), plan, policy, "qualification.yaml"); err == nil {
			t.Fatal("a publishing qualifier rendered without its auth action")
		}
	})
}

func TestQualificationResultsCoordinates(t *testing.T) {
	for _, tc := range []struct {
		path, name, group string
		code              e.Code
	}{
		{path: "inputs/definitions/nvke/definition.yaml", name: "nvke-m2", group: "nvke"},
		{path: "inputs/definitions/diagnostics/ubuntu24/definition-github-image-amd64.yaml", name: "image", group: "diagnostics"},
		{path: "inputs/definitions/definition.yaml", name: "nvke-m2", code: e.CodeUsage},
		{path: "examples/nvke/definition.yaml", name: "nvke-m2", code: e.CodeUsage},
		{path: "", name: "nvke-m2", code: e.CodeUsage},
		{path: "inputs/definitions//definition.yaml", name: "nvke-m2", code: e.CodeUsage},
		{path: "inputs/definitions/NVKE/definition.yaml", name: "nvke-m2", code: e.CodeUsage},
		{path: "inputs/definitions/nvke/definition.yaml", name: "Bad Name", code: e.CodeUsage},
	} {
		t.Run(tc.path+"/"+tc.name, func(t *testing.T) {
			group, name, err := resultsCoordinates(&factory.Plan{DefinitionPath: tc.path, Name: tc.name})
			if tc.code == "" {
				if err != nil || group != tc.group || name != tc.name {
					t.Fatalf("got %q %q %v", group, name, err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("got %v; want %s", err, tc.code)
			}
		})
	}
}
