// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// These contracts read the node image publication workflow as text. They prove its authored
// boundaries (what can start it, what it may hold, what it runs and what it uploads), not that
// GitHub accepts it, that the role trusts it or that an object store answers; the ADR-18
// reconciliation row states what has been exercised.

const nodeImageWorkflowFile = ".github/workflows/nvkr-nvke-node-image-publish.yml"

type nodeImageWorkflowJob struct {
	Name           string            `json:"name"`
	If             string            `json:"if"`
	RunsOn         string            `json:"runs-on"`
	Environment    string            `json:"environment"`
	TimeoutMinutes any               `json:"timeout-minutes"`
	Permissions    map[string]string `json:"permissions"`
	Env            map[string]string `json:"env"`
	Strategy       struct {
		FailFast *bool `json:"fail-fast"`
		Matrix   struct {
			Include []map[string]string `json:"include"`
		} `json:"matrix"`
	} `json:"strategy"`
	Steps []hardwareE2EWorkflowStep `json:"steps"`
}

type nodeImageWorkflowDoc struct {
	On          map[string]json.RawMessage `json:"on"`
	Permissions map[string]string          `json:"permissions"`
	Concurrency struct {
		Group            string `json:"group"`
		CancelInProgress *bool  `json:"cancel-in-progress"`
	} `json:"concurrency"`
	Jobs map[string]nodeImageWorkflowJob `json:"jobs"`
}

func nodeImageLoadWorkflow(t *testing.T) (nodeImageWorkflowDoc, nodeImageWorkflowJob, string) {
	t.Helper()
	var doc nodeImageWorkflowDoc
	raw := hardwareE2ELoadWorkflowFile(t, nodeImageWorkflowFile, &doc)
	if len(doc.Jobs) != 2 {
		t.Fatalf("the workflow has %d jobs, want exactly the dry-run check and the publish job", len(doc.Jobs))
	}
	for _, name := range []string{"check", "publish"} {
		if _, ok := doc.Jobs[name]; !ok {
			t.Fatalf("the workflow has no %s job", name)
		}
	}
	return doc, doc.Jobs["publish"], raw
}

// nodeImageJobs names both jobs, so a rule that holds for each is read for each.
func nodeImageJobs(doc nodeImageWorkflowDoc) map[string]nodeImageWorkflowJob {
	return map[string]nodeImageWorkflowJob{"check": doc.Jobs["check"], "publish": doc.Jobs["publish"]}
}

func nodeImageStep(t *testing.T, job nodeImageWorkflowJob, name string) (int, hardwareE2EWorkflowStep) {
	t.Helper()
	for index, step := range job.Steps {
		if step.Name == name {
			return index, step
		}
	}
	t.Fatalf("the job has no step named %q", name)
	return -1, hardwareE2EWorkflowStep{}
}

func nodeImageStepUsing(t *testing.T, job nodeImageWorkflowJob, action string) (int, hardwareE2EWorkflowStep) {
	t.Helper()
	found, at := 0, -1
	for index, step := range job.Steps {
		if strings.HasPrefix(step.Uses, action+"@") {
			found, at = found+1, index
		}
	}
	if found != 1 {
		t.Fatalf("the job uses %s %d times, want exactly once", action, found)
	}
	return at, job.Steps[at]
}

func TestNodeImageWorkflowIsDispatchOnlyAndNeverCancelsAPublication(t *testing.T) {
	doc, _, raw := nodeImageLoadWorkflow(t)
	if len(doc.On) != 1 || doc.On["workflow_dispatch"] == nil {
		t.Fatalf("publication may start only from workflow_dispatch, got triggers %v", doc.On)
	}
	if doc.Permissions == nil || len(doc.Permissions) != 0 {
		t.Fatalf("the workflow must start with no permissions so each job names its own, got %v", doc.Permissions)
	}
	if doc.Concurrency.CancelInProgress == nil || *doc.Concurrency.CancelInProgress {
		t.Fatal("a canceled upload leaves a half-written object; cancel-in-progress must be false")
	}
	for _, want := range []string{"github.ref", "inputs.dry-run"} {
		if !strings.Contains(doc.Concurrency.Group, want) {
			t.Fatalf("the concurrency group must separate refs and dry runs from real runs: missing %q in %q", want, doc.Concurrency.Group)
		}
	}
	for _, forbidden := range []string{"pull_request", "push:", "schedule:", "workflow_run", "secrets: inherit", "cancel-in-progress: true", "self-hosted", "runs-on: ${{"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("the publication workflow must not contain %q", forbidden)
		}
	}
}

func TestNodeImageWorkflowInputsAreExplicit(t *testing.T) {
	doc, _, raw := nodeImageLoadWorkflow(t)
	var dispatch struct {
		Inputs map[string]hardwareE2EWorkflowInput `json:"inputs"`
	}
	if err := json.Unmarshal(doc.On["workflow_dispatch"], &dispatch); err != nil {
		t.Fatal(err)
	}
	want := map[string]hardwareE2EWorkflowInput{
		"release":      {Type: "string", Required: true},
		"amd64-output": {Type: "string", Required: true},
		"arm64-output": {Type: "string", Required: true},
		"nvke-version": {Type: "string", Required: true},
		"dry-run":      {Type: "boolean", Default: false},
	}
	if !reflect.DeepEqual(dispatch.Inputs, want) {
		t.Fatalf("inputs are %+v, want %+v", dispatch.Inputs, want)
	}
	// A pin authored as a default would publish something no reviewer chose for this run.
	if regexp.MustCompile(`default:\s*['"]?(oci://|v[0-9]|sha256:)`).MatchString(raw) {
		t.Fatal("no input may default to a release, a digest or a version")
	}
}

func TestNodeImageWorkflowHasTwoHostedJobsAndOnlyThePublishJobHoldsTheEnvironment(t *testing.T) {
	doc, publish, _ := nodeImageLoadWorkflow(t)
	check := doc.Jobs["check"]
	wantMatrix := []map[string]string{
		{"slot": "os-image-amd64", "output": "${{ inputs.amd64-output }}"},
		{"slot": "os-image-arm64", "output": "${{ inputs.arm64-output }}"},
	}
	for name, job := range nodeImageJobs(doc) {
		if job.RunsOn != "ubuntu-24.04" {
			t.Fatalf("job %s must run on the generic hosted runner, got %q", name, job.RunsOn)
		}
		if job.TimeoutMinutes == nil {
			t.Fatalf("job %s needs a timeout", name)
		}
		if job.Strategy.FailFast == nil || *job.Strategy.FailFast {
			t.Fatalf("job %s: one architecture failing must not cancel the other mid-upload; fail-fast must be false", name)
		}
		if !reflect.DeepEqual(job.Strategy.Matrix.Include, wantMatrix) {
			t.Fatalf("job %s matrix is %v, want one leg per slot with that architecture's output name", name, job.Strategy.Matrix.Include)
		}
	}
	if publish.Environment != "s3-publish" {
		t.Fatalf("the object store variables and the role belong to the s3-publish environment, got %q", publish.Environment)
	}
	wantPermissions := map[string]string{"contents": "read", "packages": "read", "id-token": "write"}
	if !reflect.DeepEqual(publish.Permissions, wantPermissions) {
		t.Fatalf("the publish job permissions are %v, want %v", publish.Permissions, wantPermissions)
	}
	if publish.If != "github.repository == 'dsx-ai-factory/nvkr' && !inputs.dry-run" {
		t.Fatalf("the publish job must run only in the upstream repository and never on a dry run, got %q", publish.If)
	}
	// The dry run is a job of its own because permissions and an environment cannot depend on an input:
	// it must hold neither the environment nor the OIDC permission, or a dry run would wait for approval
	// and be granted a token it never uses.
	if check.Environment != "" {
		t.Fatalf("the dry-run job must not enter an environment, got %q", check.Environment)
	}
	if want := map[string]string{"contents": "read", "packages": "read"}; !reflect.DeepEqual(check.Permissions, want) {
		t.Fatalf("the dry-run job permissions are %v, want %v", check.Permissions, want)
	}
	if check.If != "github.repository == 'dsx-ai-factory/nvkr' && inputs.dry-run" {
		t.Fatalf("the dry-run job must run only in the upstream repository and only on a dry run, got %q", check.If)
	}
}

// The dry-run job is the one a reviewer approves nothing for, so nothing of the store may reach it.
func TestNodeImageWorkflowDryRunJobNeverHoldsTheStoreOrItsRole(t *testing.T) {
	doc, _, _ := nodeImageLoadWorkflow(t)
	serialized, err := json.Marshal(doc.Jobs["check"])
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"id-token", "s3-publish", "S3_PUBLISH", "vars.", "aws-actions", "configure-aws-credentials", "AWS_", "role-to-assume",
		"upload-artifact", "aws ", "--run-url", "RUN_URL", "github.run_attempt",
	} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("the dry-run job must not contain %q: %s", forbidden, serialized)
		}
	}
	secrets := regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`).FindAllStringSubmatch(string(serialized), -1)
	if len(secrets) != 1 || secrets[0][1] != "GITHUB_TOKEN" {
		t.Fatalf("the dry-run job may read only GITHUB_TOKEN, once, got %v", secrets)
	}
	// The store the dry run names is a placeholder that is not an address, and the key is real.
	_, pull := nodeImageStep(t, doc.Jobs["check"], "Pull and check the image")
	wantEnv := map[string]string{
		"SLOT":         "${{ matrix.slot }}",
		"OUTPUT":       "${{ matrix.output }}",
		"RELEASE":      "${{ inputs.release }}",
		"NVKE_VERSION": "${{ inputs.nvke-version }}",
		"S3_URI":       "s3://nvkr-dry-run-placeholder",
		"REGION":       "us-west-2",
		"CDN_DOMAIN":   "dry-run.invalid",
		"PREFIX":       "nvkr/os-image",
	}
	if !reflect.DeepEqual(pull.Env, wantEnv) {
		t.Fatalf("the dry run reads %v, want %v", pull.Env, wantEnv)
	}
	for _, want := range []string{
		"python3 scripts/hardware-e2e-publish-node-image.py --dry-run",
		`--slot "$SLOT"`, `--reference "$RELEASE"`, `--output "$OUTPUT"`, `--version "$NVKE_VERSION"`,
		`--s3-uri "$S3_URI"`, `--prefix "$PREFIX"`, `--cdn-domain "$CDN_DOMAIN"`, `--region "$REGION"`,
		`--nvkr "$RUNNER_TEMP/bin/nvkr"`, `--work-dir "$RUNNER_TEMP/pull"`, `--out "$RUNNER_TEMP/record"`,
	} {
		if !strings.Contains(pull.Run, want) {
			t.Fatalf("the dry run lacks %q", want)
		}
	}
}

func TestNodeImageWorkflowStepsArePinnedAndKeepNoCredentials(t *testing.T) {
	doc, _, _ := nodeImageLoadWorkflow(t)
	pinned := regexp.MustCompile(`^[A-Za-z0-9._/-]+@[0-9a-f]{40}$`)
	for jobName, job := range nodeImageJobs(doc) {
		for _, step := range job.Steps {
			label := jobName + "/" + step.Name
			if step.Uses == "" {
				continue
			}
			if !pinned.MatchString(step.Uses) {
				t.Fatalf("%s: an action must be pinned by a full commit SHA, got %q", label, step.Uses)
			}
			switch {
			case strings.HasPrefix(step.Uses, "actions/checkout@"):
				if step.With["persist-credentials"] != false {
					t.Fatalf("%s: a checkout must not keep credentials", label)
				}
			case strings.HasPrefix(step.Uses, "actions/setup-go@"):
				if step.With["cache"] != false {
					t.Fatalf("%s: setup-go must set cache: false so no cache is restored into a run that can write to the object store", label)
				}
			case strings.HasPrefix(step.Uses, "actions/upload-artifact@"):
				if step.With["retention-days"] == nil || step.With["if-no-files-found"] != "error" {
					t.Fatalf("%s: an upload must bound its retention and fail when the record is missing", label)
				}
			}
		}
	}
}

func TestNodeImageWorkflowKeepsExpressionsOutOfScripts(t *testing.T) {
	doc, _, _ := nodeImageLoadWorkflow(t)
	for jobName, job := range nodeImageJobs(doc) {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "${{") {
				t.Fatalf("%s/%s: a script must read its values from the environment, never from an expression: %q", jobName, step.Name, step.Run)
			}
		}
	}
}

func TestNodeImageWorkflowHoldsCredentialsOnlyWhereTheyAreUsed(t *testing.T) {
	doc, job, raw := nodeImageLoadWorkflow(t)
	// One registry login per job, each with the run's own token.
	secrets := regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`).FindAllStringSubmatch(raw, -1)
	if len(secrets) != 2 {
		t.Fatalf("the workflow reads a secret once per job, got %v", secrets)
	}
	for _, match := range secrets {
		if match[1] != "GITHUB_TOKEN" {
			t.Fatalf("the workflow may read only GITHUB_TOKEN, got %v", secrets)
		}
	}
	for name, each := range nodeImageJobs(doc) {
		_, login := nodeImageStepUsing(t, each, "docker/login-action")
		if login.With["registry"] != "ghcr.io" || login.With["password"] != "${{ secrets.GITHUB_TOKEN }}" {
			t.Fatalf("%s: the registry login must use the run's own token for ghcr.io, got %v", name, login.With)
		}
	}
	// The role is assumed in the publish job only, and always: the dry run is another job.
	_, aws := nodeImageStepUsing(t, job, "aws-actions/configure-aws-credentials")
	if aws.Uses != "aws-actions/configure-aws-credentials@e1253824e5c10ff9df46874f81ed3ec929e19cfd" {
		t.Fatalf("the credential action must be the reviewed pin, got %q", aws.Uses)
	}
	if aws.If != "" {
		t.Fatalf("a publication always takes its credentials; a dry run is a different job, got condition %q", aws.If)
	}
	wantWith := map[string]any{"role-to-assume": "${{ vars.S3_PUBLISH_AWS_ROLE_ARN }}", "aws-region": "${{ vars.S3_PUBLISH_AWS_REGION }}"}
	if !reflect.DeepEqual(aws.With, wantWith) {
		t.Fatalf("the credential step must assume the environment's role and nothing else, got %v", aws.With)
	}
	for _, forbidden := range []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "aws-access-key-id", "aws-secret-access-key", "AWS_SESSION_TOKEN"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("no static credential may appear in the workflow: %q", forbidden)
		}
	}
	for name, each := range nodeImageJobs(doc) {
		for _, step := range each.Steps {
			if strings.Contains(step.Run, "secrets.") || strings.Contains(step.Run, "GITHUB_TOKEN") {
				t.Fatalf("%s/%s: a script must not read a credential", name, step.Name)
			}
		}
	}
	// Only the job's own condition keeps a dry run out. No step of the publish job branches on one, so
	// nothing in it can skip the credential or the upload.
	serialized, err := json.Marshal(job.Steps)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "dry-run") || strings.Contains(string(serialized), "DRY_RUN") {
		t.Fatalf("no step of the publish job may branch on a dry run: %s", serialized)
	}
}

func TestNodeImageWorkflowAdmitsARealRunFromMainOnly(t *testing.T) {
	_, job, _ := nodeImageLoadWorkflow(t)
	if job.Steps[0].Name != "Admit the dispatch" {
		t.Fatal("everything that can be refused before a checkout is refused first")
	}
	step := job.Steps[0]
	for _, want := range []string{`if [ "$REF" != refs/heads/main ]; then`, "exit 1"} {
		if !strings.Contains(step.Run, want) {
			t.Fatalf("the admission step lacks %q", want)
		}
	}
	if len(step.Env) != 1 || step.Env["REF"] != "${{ github.ref }}" {
		t.Fatalf("the admission step must read only the ref from the run, got %v", step.Env)
	}
	// The key holds the digest, so a later attempt rewrites identical bytes: refusing one would only cost a dispatch.
	if strings.Contains(step.Run, "RUN_ATTEMPT") {
		t.Fatal("a re-run is safe for a content-addressed key and must not be refused")
	}
}

func TestNodeImageWorkflowRefusesAnUnconfiguredEnvironmentBeforeAnyWork(t *testing.T) {
	_, job, _ := nodeImageLoadWorkflow(t)
	configIndex, config := nodeImageStep(t, job, "Check the publication is configured")
	checkoutIndex, _ := nodeImageStepUsing(t, job, "actions/checkout")
	if configIndex != 1 || configIndex >= checkoutIndex {
		t.Fatalf("a missing variable must be refused as the second step, before the checkout, not at step %d", configIndex)
	}
	wantEnv := map[string]string{
		"S3_URI":     "${{ vars.S3_PUBLISH_S3_URI }}",
		"ROLE_ARN":   "${{ vars.S3_PUBLISH_AWS_ROLE_ARN }}",
		"REGION":     "${{ vars.S3_PUBLISH_AWS_REGION }}",
		"CDN_DOMAIN": "${{ vars.S3_PUBLISH_CLOUDFRONT_DOMAIN }}",
	}
	if !reflect.DeepEqual(config.Env, wantEnv) {
		t.Fatalf("the configuration check reads %v, want %v", config.Env, wantEnv)
	}
	for _, want := range []string{
		"required=(S3_URI ROLE_ARN REGION CDN_DOMAIN)",
		`[ -n "${!name}" ] || missing+=("$name")`,
		`if [ "${#missing[@]}" -gt 0 ]; then`,
		"exit 1",
	} {
		if !strings.Contains(config.Run, want) {
			t.Fatalf("the configuration check lacks %q", want)
		}
	}
}

func TestNodeImageWorkflowOrdersCredentialsAfterTheBuildAndBeforeThePublish(t *testing.T) {
	doc, job, _ := nodeImageLoadWorkflow(t)
	for name, order := range map[string][]string{
		"publish": {
			"Admit the dispatch", "Check the publication is configured", "Check out NVKR", "Set up Go", "Build nvkr",
			"Log in to GHCR", "Configure AWS credentials", "Publish the image and read it back",
			"Show the outputs manifest entry", "Upload the publication record",
		},
		"check": {"Check out NVKR", "Set up Go", "Build nvkr", "Log in to GHCR", "Pull and check the image"},
	} {
		steps := doc.Jobs[name].Steps
		if name == "publish" {
			steps = job.Steps
		}
		if len(steps) != len(order) {
			t.Fatalf("job %s has %d steps, want %d: %v", name, len(steps), len(order), order)
		}
		for index, want := range order {
			if steps[index].Name != want {
				t.Fatalf("job %s step %d is %q, want %q: the build runs before any credential exists, and the publish after", name, index, steps[index].Name, want)
			}
		}
	}
}

func TestNodeImageWorkflowBuildsNvkrOnlyToPullTheRelease(t *testing.T) {
	doc, _, _ := nodeImageLoadWorkflow(t)
	for name, job := range nodeImageJobs(doc) {
		_, setup := nodeImageStepUsing(t, job, "actions/setup-go")
		if setup.With["go-version-file"] != ".go-version" {
			t.Fatalf("%s: Go must come from the repository's pin, got %v", name, setup.With)
		}
		_, build := nodeImageStep(t, job, "Build nvkr")
		if build.Env["GOTOOLCHAIN"] != "local" || build.Env["CGO_ENABLED"] != "0" {
			t.Fatalf("%s: the build must use the pinned local toolchain without cgo, got %v", name, build.Env)
		}
		if !strings.Contains(build.Run, `-o "$RUNNER_TEMP/bin/nvkr" ./cmd/nvkr`) {
			t.Fatalf("%s: the build must write the binary the next step names, got %q", name, build.Run)
		}
	}
}

func TestNodeImageWorkflowPublishesOnlyThroughTheScript(t *testing.T) {
	_, job, raw := nodeImageLoadWorkflow(t)
	_, publish := nodeImageStep(t, job, "Publish the image and read it back")
	wantEnv := map[string]string{
		"SLOT":         "${{ matrix.slot }}",
		"OUTPUT":       "${{ matrix.output }}",
		"RELEASE":      "${{ inputs.release }}",
		"NVKE_VERSION": "${{ inputs.nvke-version }}",
		"S3_URI":       "${{ vars.S3_PUBLISH_S3_URI }}",
		"REGION":       "${{ vars.S3_PUBLISH_AWS_REGION }}",
		"CDN_DOMAIN":   "${{ vars.S3_PUBLISH_CLOUDFRONT_DOMAIN }}",
		"PREFIX":       "nvkr/os-image",
		"RUN_URL":      "${{ github.server_url }}/${{ github.repository }}/actions/runs/${{ github.run_id }}/attempts/${{ github.run_attempt }}",
	}
	if !reflect.DeepEqual(publish.Env, wantEnv) {
		t.Fatalf("the publish step reads %v, want %v", publish.Env, wantEnv)
	}
	for _, want := range []string{
		"python3 scripts/hardware-e2e-publish-node-image.py",
		`--slot "$SLOT"`, `--reference "$RELEASE"`, `--output "$OUTPUT"`, `--version "$NVKE_VERSION"`,
		`--s3-uri "$S3_URI"`, `--prefix "$PREFIX"`, `--cdn-domain "$CDN_DOMAIN"`, `--region "$REGION"`,
		`--run-url "$RUN_URL"`, `--nvkr "$RUNNER_TEMP/bin/nvkr"`,
		`--work-dir "$RUNNER_TEMP/pull"`, `--out "$RUNNER_TEMP/record"`,
	} {
		if !strings.Contains(publish.Run, want) {
			t.Fatalf("the publish step lacks %q", want)
		}
	}
	if strings.Contains(publish.Run, "--dry-run") {
		t.Fatal("the publish step is never a dry run; the dry run is its own job")
	}
	if _, err := os.Stat(filepath.Join(hardwareE2ERoot(t), "scripts", "hardware-e2e-publish-node-image.py")); err != nil {
		t.Fatalf("the publish step names a script that does not exist: %v", err)
	}
	// Every object store call and every pull belongs to the script. The workflow never fetches the
	// HTTPS front end: it can sit behind an address allowlist that runners are not on, so a request
	// from here would prove nothing about the imager's path.
	for _, forbidden := range []string{"curl", "wget", "aws s3", "aws s3api", "oras ", "nvkr release pull", "http://"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("the workflow must not contain %q: the script owns every pull and every object store call", forbidden)
		}
	}
}

func TestNodeImageWorkflowUploadsOnlyTheRecordAndOnlyFromARealRun(t *testing.T) {
	doc, job, _ := nodeImageLoadWorkflow(t)
	_, upload := nodeImageStepUsing(t, job, "actions/upload-artifact")
	if upload.If != "" {
		t.Fatalf("the publish job is a real run, so its upload is unconditional, got condition %q", upload.If)
	}
	// The attempt is in the name: a re-run is allowed, and an artifact name cannot repeat within a run.
	want := map[string]any{
		"name":              "nvkr-hardware-e2e-node-image-publication-${{ matrix.slot }}-${{ github.run_id }}-${{ github.run_attempt }}",
		"path":              "${{ runner.temp }}/record",
		"if-no-files-found": "error",
		"retention-days":    float64(90),
	}
	if !reflect.DeepEqual(upload.With, want) {
		t.Fatalf("the upload is %v, want %v: only the record, never the pulled image", upload.With, want)
	}
	for _, step := range doc.Jobs["check"].Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			t.Fatalf("the dry run writes no record, so it uploads nothing: %q", step.Name)
		}
	}
	_, show := nodeImageStep(t, job, "Show the outputs manifest entry")
	if show.If != "" || show.Env["SLOT"] != "${{ matrix.slot }}" ||
		!strings.Contains(show.Run, `jq .manifestEntry "$RUNNER_TEMP/record/$SLOT.json"`) {
		t.Fatalf("the manifest entry is shown from the record of a real run, got %+v", show)
	}
}

func TestNodeImageWorkflowFailsClosed(t *testing.T) {
	doc, _, raw := nodeImageLoadWorkflow(t)
	for _, forbidden := range []string{"continue-on-error", "|| true", "always()", "failure()", "cancelled()", "set +e"} { //nolint:misspell // GitHub expression function spelling.
		if strings.Contains(raw, forbidden) {
			t.Fatalf("a failed check, pull, upload or read-back must fail the job; the workflow must not contain %q", forbidden)
		}
	}
	for name, job := range nodeImageJobs(doc) {
		for _, step := range job.Steps {
			if step.Run != "" && !strings.Contains(step.Run, "set -euo pipefail") {
				t.Fatalf("%s/%s: every script stops at its first failure", name, step.Name)
			}
		}
	}
}

func TestNodeImageWorkflowNamesNoQualificationOrReleaseAuthority(t *testing.T) {
	_, _, raw := nodeImageLoadWorkflow(t)
	for _, forbidden := range []string{
		"cosign", "docker push", "oras push", "gh release", "git tag", "git push", "qualify publish", "qualify gate",
		"release publish", "release verify", "--publish", "pipeline-attested",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("the publication workflow must not contain %q: it signs, verifies and gates nothing", forbidden)
		}
	}
	for _, want := range []string{`qualificationAuthority "none"`, "stand-in"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("the workflow header must say %q", want)
		}
	}
}
