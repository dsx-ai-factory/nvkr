// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// These contracts read the hardware E2E workflow as text. They prove its authored
// boundaries (what can start it, what it may hold, what it runs and what makes it
// fail), not that GitHub accepts it or that the hardware site, Teleport or a registry answer;
// the ADR-18 reconciliation row states what has been exercised.

const hardwareE2EWorkflowFile = ".github/workflows/nvkr-nvke-hardware-e2e.yml"

type hardwareE2EWorkflowStep struct {
	Name             string            `json:"name"`
	ID               string            `json:"id"`
	If               string            `json:"if"`
	Run              string            `json:"run"`
	Uses             string            `json:"uses"`
	WorkingDirectory string            `json:"working-directory"`
	Env              map[string]string `json:"env"`
	With             map[string]any    `json:"with"`
	ContinueOnError  any               `json:"continue-on-error"`
}

type hardwareE2EWorkflowJob struct {
	Name           string            `json:"name"`
	If             string            `json:"if"`
	Needs          json.RawMessage   `json:"needs"`
	RunsOn         string            `json:"runs-on"`
	Environment    string            `json:"environment"`
	TimeoutMinutes any               `json:"timeout-minutes"`
	Permissions    map[string]string `json:"permissions"`
	Env            map[string]string `json:"env"`
	Outputs        map[string]string `json:"outputs"`
	Strategy       struct {
		FailFast    *bool  `json:"fail-fast"`
		MaxParallel int    `json:"max-parallel"`
		Matrix      string `json:"matrix"`
	} `json:"strategy"`
	Steps []hardwareE2EWorkflowStep `json:"steps"`
}

type hardwareE2EWorkflowInput struct {
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Default  any      `json:"default"`
	Options  []string `json:"options"`
}

type hardwareE2EWorkflowDoc struct {
	On          map[string]json.RawMessage `json:"on"`
	Permissions map[string]string          `json:"permissions"`
	Concurrency struct {
		Group            string `json:"group"`
		CancelInProgress *bool  `json:"cancel-in-progress"`
	} `json:"concurrency"`
	Jobs map[string]hardwareE2EWorkflowJob `json:"jobs"`
}

func hardwareE2ELoadWorkflow(t *testing.T) (hardwareE2EWorkflowDoc, string) {
	t.Helper()
	var doc hardwareE2EWorkflowDoc
	// The text first: the return statement would otherwise leave the order of reading doc and filling it open.
	raw := hardwareE2ELoadWorkflowFile(t, hardwareE2EWorkflowFile, &doc)
	return doc, raw
}

// hardwareE2ELoadWorkflowFile parses one workflow into doc and returns its text.
func hardwareE2ELoadWorkflowFile(t *testing.T, file string, doc any) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(hardwareE2ERoot(t), file))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, doc); err != nil {
		t.Fatalf("%s does not parse: %v", file, err)
	}
	return string(raw)
}

func hardwareE2EWorkflowInputs(t *testing.T, doc hardwareE2EWorkflowDoc) map[string]hardwareE2EWorkflowInput {
	t.Helper()
	var dispatch struct {
		Inputs map[string]hardwareE2EWorkflowInput `json:"inputs"`
	}
	if err := json.Unmarshal(doc.On["workflow_dispatch"], &dispatch); err != nil {
		t.Fatalf("workflow_dispatch does not parse: %v", err)
	}
	return dispatch.Inputs
}

func hardwareE2EWorkflowStepNamed(t *testing.T, job hardwareE2EWorkflowJob, name string) (int, hardwareE2EWorkflowStep) {
	t.Helper()
	for index, step := range job.Steps {
		if step.Name == name {
			return index, step
		}
	}
	t.Fatalf("the job has no step named %q", name)
	return -1, hardwareE2EWorkflowStep{}
}

func hardwareE2EWorkflowNeeds(t *testing.T, job hardwareE2EWorkflowJob) []string {
	t.Helper()
	var many []string
	if err := json.Unmarshal(job.Needs, &many); err == nil {
		sort.Strings(many)
		return many
	}
	var one string
	if err := json.Unmarshal(job.Needs, &one); err != nil {
		t.Fatalf("needs is neither a list nor a name: %v", err)
	}
	return []string{one}
}

func TestHardwareE2EWorkflowIsDispatchOnlyAndNeverCancelsALeg(t *testing.T) {
	doc, raw := hardwareE2ELoadWorkflow(t)
	if len(doc.On) != 1 || doc.On["workflow_dispatch"] == nil {
		t.Fatalf("the lane may start only from workflow_dispatch, got triggers %v", doc.On)
	}
	if doc.Permissions == nil || len(doc.Permissions) != 0 {
		t.Fatalf("the workflow must start with no permissions so each job names its own, got %v", doc.Permissions)
	}
	if doc.Concurrency.CancelInProgress == nil || *doc.Concurrency.CancelInProgress {
		t.Fatal("a canceled leg dies before its teardown and leaks its machines; cancel-in-progress must be false")
	}
	for _, want := range []string{"github.ref", "inputs.dry-run"} {
		if !strings.Contains(doc.Concurrency.Group, want) {
			t.Fatalf("the concurrency group must separate refs and dry runs from real runs: missing %q in %q", want, doc.Concurrency.Group)
		}
	}
	leg := doc.Jobs["hardware-e2e"]
	if leg.Strategy.FailFast == nil || *leg.Strategy.FailFast {
		t.Fatal("a failing leg must not cancel its siblings; fail-fast must be false")
	}
	if leg.Strategy.MaxParallel != 1 {
		t.Fatalf("legs share one hardware site that GitHub concurrency cannot count across repositories; max-parallel must be 1, got %d", leg.Strategy.MaxParallel)
	}
	if leg.Strategy.Matrix != "${{ fromJSON(needs.plan.outputs.matrix) }}" {
		t.Fatalf("the matrix must come from the plan job's harness-derived matrix, got %q", leg.Strategy.Matrix)
	}
	for _, forbidden := range []string{"pull_request", "push:", "schedule:", "workflow_run", "secrets: inherit", "cancel-in-progress: true", "self-hosted", "runs-on: ${{"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("the hardware E2E workflow must not contain %q", forbidden)
		}
	}
}

func TestHardwareE2EWorkflowInputsAreExplicit(t *testing.T) {
	doc, raw := hardwareE2ELoadWorkflow(t)
	inputs := hardwareE2EWorkflowInputs(t, doc)
	for name, want := range map[string]hardwareE2EWorkflowInput{
		"artifact-source": {Type: "choice", Required: true, Default: nil, Options: []string{"nvkr-outputs", "build-subject"}},
		"stage":           {Type: "choice", Default: "full", Options: []string{"provision", "bootstrap", "deployment", "full"}},
		"harness-commit":  {Type: "string", Default: ""},
		"outputs-manifest": {
			Type: "string", Default: "",
		},
		"node-user":                     {Type: "string", Default: ""},
		"allow-node-image-version-skew": {Type: "boolean", Default: false},
		"verify-plan":                   {Type: "boolean", Default: false},
		"dry-run":                       {Type: "boolean", Default: false},
		"keep-site":                     {Type: "boolean", Default: false},
	} {
		got, ok := inputs[name]
		if !ok {
			t.Fatalf("input %q is missing", name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("input %q is %+v, want %+v", name, got, want)
		}
	}
	if len(inputs) != 9 {
		t.Fatalf("the workflow takes exactly nine inputs, got %d", len(inputs))
	}
	// A 40-hex literal anywhere authored as a default would pin a commit outside the committed plan.
	if regexp.MustCompile(`default:\s*['"]?[0-9a-f]{40}`).MatchString(raw) {
		t.Fatal("no input may default to a commit; the plan fixture and its provenance own the subject commit")
	}
}

func TestHardwareE2EWorkflowJobsUseHostedRunnersAndNamedPermissions(t *testing.T) {
	doc, _ := hardwareE2ELoadWorkflow(t)
	want := map[string]struct {
		permissions map[string]string
		environment string
		needs       []string
	}{
		"plan":               {permissions: map[string]string{"contents": "read"}},
		"verify-node-images": {permissions: map[string]string{"contents": "read"}, needs: []string{"plan"}},
		"hardware-e2e": {
			permissions: map[string]string{"contents": "read", "packages": "read", "id-token": "write"},
			environment: "nvke-hardware-e2e",
			needs:       []string{"plan", "verify-node-images"},
		},
		"summarize": {permissions: map[string]string{"contents": "read"}, needs: []string{"hardware-e2e", "plan", "verify-node-images"}},
	}
	if len(doc.Jobs) != len(want) {
		t.Fatalf("the workflow has jobs %v, want exactly %d", hardwareE2EJobNames(doc), len(want))
	}
	for name, expected := range want {
		job, ok := doc.Jobs[name]
		if !ok {
			t.Fatalf("job %q is missing", name)
		}
		if job.RunsOn != "ubuntu-24.04" {
			t.Fatalf("job %s must run on the generic hosted runner, got %q", name, job.RunsOn)
		}
		if !reflect.DeepEqual(job.Permissions, expected.permissions) {
			t.Fatalf("job %s permissions are %v, want %v", name, job.Permissions, expected.permissions)
		}
		if job.Environment != expected.environment {
			t.Fatalf("job %s environment is %q, want %q: hardware site credentials are scoped to the protected environment", name, job.Environment, expected.environment)
		}
		if name != "plan" && !reflect.DeepEqual(hardwareE2EWorkflowNeeds(t, job), expected.needs) {
			t.Fatalf("job %s needs %v, want %v", name, hardwareE2EWorkflowNeeds(t, job), expected.needs)
		}
		if job.TimeoutMinutes == nil {
			t.Fatalf("job %s needs a timeout", name)
		}
	}
	if doc.Jobs["plan"].If != "github.repository == 'dsx-ai-factory/nvkr'" {
		t.Fatalf("the plan job must run only in the upstream repository, got %q", doc.Jobs["plan"].If)
	}
	if got := doc.Jobs["hardware-e2e"].TimeoutMinutes; got != "${{ matrix.job_timeout_minutes }}" {
		t.Fatalf("a leg's timeout must be the harness matrix's, got %v", got)
	}
	// verify-node-images is skipped for build-subject, so a skipped result may be accepted only there.
	for _, name := range []string{"hardware-e2e", "summarize"} {
		condition := doc.Jobs[name].If
		for _, part := range []string{
			"!cancelled()", //nolint:misspell // GitHub expression function spelling.
			"needs.plan.result == 'success'",
			"(inputs.artifact-source == 'build-subject' || needs.verify-node-images.result == 'success')",
		} {
			if !strings.Contains(condition, part) {
				t.Fatalf("job %s condition %q lacks %q", name, condition, part)
			}
		}
	}
	if !strings.Contains(doc.Jobs["summarize"].If, "needs.hardware-e2e.result == 'success'") || !strings.Contains(doc.Jobs["summarize"].If, "!inputs.dry-run") {
		t.Fatalf("the summary job must follow a successful real run only, got %q", doc.Jobs["summarize"].If)
	}
	if doc.Jobs["verify-node-images"].If != "inputs.artifact-source == 'nvkr-outputs'" {
		t.Fatalf("node image fetches belong to NVKR outputs only, got %q", doc.Jobs["verify-node-images"].If)
	}
}

func hardwareE2EJobNames(doc hardwareE2EWorkflowDoc) []string {
	names := make([]string, 0, len(doc.Jobs))
	for name := range doc.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func TestHardwareE2EWorkflowStepsArePinnedAndKeepNoCredentials(t *testing.T) {
	doc, _ := hardwareE2ELoadWorkflow(t)
	pinned := regexp.MustCompile(`^[A-Za-z0-9._/-]+@[0-9a-f]{40}$`)
	for _, name := range hardwareE2EJobNames(doc) {
		for _, step := range doc.Jobs[name].Steps {
			label := name + "/" + step.Name
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
					t.Fatalf("%s: setup-go must set cache: false so no cache is restored into a run that holds hardware site credentials", label)
				}
			case strings.HasPrefix(step.Uses, "actions/upload-artifact@"):
				if step.With["retention-days"] == nil || step.With["if-no-files-found"] == nil {
					t.Fatalf("%s: an upload must bound its retention and say what a missing file means", label)
				}
			}
		}
	}
}

func TestHardwareE2EWorkflowKeepsExpressionsOutOfScripts(t *testing.T) {
	doc, _ := hardwareE2ELoadWorkflow(t)
	for _, name := range hardwareE2EJobNames(doc) {
		for _, step := range doc.Jobs[name].Steps {
			if strings.Contains(step.Run, "${{") {
				t.Fatalf("%s/%s: a script must read its values from the environment, never from an expression: %q", name, step.Name, step.Run)
			}
		}
	}
}

func TestHardwareE2EWorkflowHoldsCredentialsOnlyWhereTheyAreUsed(t *testing.T) {
	doc, raw := hardwareE2ELoadWorkflow(t)
	secretRef := regexp.MustCompile(`secrets\.([A-Z0-9_]+)`)
	// Integration boundary: the FORGE_* environment names are the pinned NVKE harness's own input
	// names; the repository secrets and variables they are read from are NVKR's HARDWARE_E2E_* names.
	allowed := map[string]bool{
		"GITHUB_TOKEN":                  true,
		"HARDWARE_E2E_API_KEY":          true,
		"HARDWARE_E2E_JUMP_HOST":        true,
		"HARDWARE_E2E_SITE_2_JUMP_HOST": true,
		"NGC_API_KEY":                   true,
	}
	if strings.Contains(raw, "vars.FORGE_") || strings.Contains(raw, "secrets.FORGE_") {
		t.Fatal("repository variables and secrets are NVKR's and use HARDWARE_E2E_* names, not the harness's FORGE_* spelling")
	}
	for _, match := range secretRef.FindAllStringSubmatch(raw, -1) {
		if !allowed[match[1]] {
			t.Fatalf("the workflow reads secret %q, which the lane has not admitted", match[1])
		}
	}
	for name, job := range doc.Jobs {
		if name == "hardware-e2e" {
			continue
		}
		serialized, err := json.Marshal(job)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(serialized), "secrets.") || strings.Contains(string(serialized), "vars.HARDWARE_E2E_") {
			t.Fatalf("job %s must not hold hardware site credentials or secrets; they belong to the protected-environment job", name)
		}
	}
	leg := doc.Jobs["hardware-e2e"]
	// The hardware site's API key reaches the suite's own process and nothing else.
	if _, ok := leg.Env["FORGE_API_KEY"]; ok {
		t.Fatal("FORGE_API_KEY must not be job-level")
	}
	for _, step := range leg.Steps {
		_, has := step.Env["FORGE_API_KEY"]
		if has != strings.HasPrefix(step.Name, "Run the e2e suite") {
			t.Fatalf("FORGE_API_KEY must reach only the suite's run step, found at %q", step.Name)
		}
	}
	for name, secret := range map[string]string{
		"FORGE_JUMP_HOST":        "HARDWARE_E2E_JUMP_HOST",
		"FORGE_SITE_2_JUMP_HOST": "HARDWARE_E2E_SITE_2_JUMP_HOST",
	} {
		if leg.Env[name] != "${{ secrets."+secret+" }}" {
			t.Fatalf("%s must be a job-level secret so the log masks it, got %q", name, leg.Env[name])
		}
	}
	for _, step := range leg.Steps {
		if strings.Contains(step.Run, "secrets.") || strings.Contains(step.Run, "FORGE_API_KEY") {
			t.Fatalf("step %q must not read a secret in its script", step.Name)
		}
	}
}

func TestHardwareE2EWorkflowUploadsNothingThatCarriesRemoteOutput(t *testing.T) {
	doc, _ := hardwareE2ELoadWorkflow(t)
	for _, name := range hardwareE2EJobNames(doc) {
		for _, step := range doc.Jobs[name].Steps {
			if !strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
				continue
			}
			path, _ := step.With["path"].(string)
			for _, forbidden := range []string{"report.json", "e2e-config.yaml", "identity", "tbot.yaml"} {
				if strings.Contains(path, forbidden) {
					t.Fatalf("%s/%s uploads %q, which carries remote command output or a credential", name, step.Name, forbidden)
				}
			}
		}
	}
	// withhold runs after the suite whatever its outcome, before anything is uploaded.
	leg := doc.Jobs["hardware-e2e"]
	firstUpload := len(leg.Steps)
	for index, step := range leg.Steps {
		if strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
			firstUpload = index
			break
		}
	}
	index, withhold := hardwareE2EWorkflowStepNamed(t, leg, "Withhold credentials from the run files")
	runIndex, _ := hardwareE2EWorkflowStepNamed(t, leg, "Run the e2e suite${{ inputs.dry-run && ' (dry run)' || '' }}")
	if withhold.If != "always()" || index <= runIndex || index >= firstUpload || withhold.Run != "nvke/release/hack/qualify-e2e.sh withhold" {
		t.Fatal("the run files must be redacted, even after a failure, after the suite and before the first upload")
	}
	// Redaction that fails must not be followed by an upload of what it failed to redact, and no other
	// upload may carry the suite's files around that gate.
	if withhold.ID != "withhold" {
		t.Fatalf("the redaction step must be addressable by id, got %q", withhold.ID)
	}
	const gated = "always() && steps.withhold.outcome == 'success'"
	for _, job := range hardwareE2EJobNames(doc) {
		for _, step := range doc.Jobs[job].Steps {
			if !strings.HasPrefix(step.Uses, "actions/upload-artifact@") {
				continue
			}
			path, _ := step.With["path"].(string)
			if strings.Contains(path, "controller/test/e2e/") && step.If != gated {
				t.Fatalf("%s/%s uploads the suite's files and runs when %q, want %q", job, step.Name, step.If, gated)
			}
		}
	}
	_, upload := hardwareE2EWorkflowStepNamed(t, leg, "Upload the run files")
	if upload.If != gated || !strings.Contains(upload.With["path"].(string), "controller/test/e2e/qualify-e2e.junit.xml") {
		t.Fatalf("the run files upload runs when %q and uploads %v", upload.If, upload.With["path"])
	}
	// Teardown never waits on the redaction: the daemon and its identity are removed whatever happened.
	for _, name := range []string{"Stop the Teleport identity daemon", "Remove the Teleport identity"} {
		if _, step := hardwareE2EWorkflowStepNamed(t, leg, name); step.If != "always()" {
			t.Fatalf("%q must run always, got %q", name, step.If)
		}
	}
}

// A step allowed to fail would let the coverage, summary or redaction checks pass by being ignored.
// Teardown alone may tolerate a missing process, and says so in one named step.
func TestHardwareE2EWorkflowNoStepMayFailQuietly(t *testing.T) {
	doc, _ := hardwareE2ELoadWorkflow(t)
	const tolerant = "Stop the Teleport identity daemon"
	masking := regexp.MustCompile(`\|\|\s*(true|:)\b|;\s*(true|:)\s*$|\bset \+e\b`)
	for _, name := range hardwareE2EJobNames(doc) {
		for _, step := range doc.Jobs[name].Steps {
			if step.ContinueOnError != nil {
				t.Fatalf("%s/%s sets continue-on-error (%v); a failing step must fail the job", name, step.Name, step.ContinueOnError)
			}
			if masking.MatchString(step.Run) && step.Name != tolerant {
				t.Fatalf("%s/%s masks a failing command; only %q may tolerate one: %q", name, step.Name, tolerant, step.Run)
			}
		}
	}
	// The evidence checks run under a shell that stops at the first failure.
	leg := doc.Jobs["hardware-e2e"]
	for _, check := range []string{"Check the selected specs fit the stage", "Check the run covered what it selected"} {
		if _, step := hardwareE2EWorkflowStepNamed(t, leg, check); !strings.Contains(step.Run, "set -euo pipefail") {
			t.Fatalf("%q must stop at the first failure", check)
		}
	}
}

// The summary job has no environment, so it cannot see the variable the legs read; it must take the
// account from the legs themselves, and the legs must publish only a plain account name.
func TestHardwareE2EWorkflowSummaryRecordsTheNodeAccountTheLegsUsed(t *testing.T) {
	doc, _ := hardwareE2ELoadWorkflow(t)
	leg, summary := doc.Jobs["hardware-e2e"], doc.Jobs["summarize"]
	if leg.Outputs["node-user"] != "${{ steps.node-user.outputs.value }}" {
		t.Fatalf("the leg job must publish the account its steps used, got %v", leg.Outputs)
	}
	if summary.Env["NODE_USER"] != "${{ needs.hardware-e2e.outputs.node-user }}" {
		t.Fatalf("the summary must record the account the legs used, got %q", summary.Env["NODE_USER"])
	}
	serialized, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(serialized), "vars.") || strings.Contains(string(serialized), "inputs.node-user") {
		t.Fatalf("the summary job must not read the node account itself: %s", serialized)
	}
	if leg.Env["NODE_USER"] != "${{ inputs.node-user || vars.QUALIFY_E2E_NODE_USER }}" {
		t.Fatalf("the legs' account is the input, then the environment's variable, got %q", leg.Env["NODE_USER"])
	}
	publishIndex, publish := hardwareE2EWorkflowStepNamed(t, leg, "Publish the node account")
	configIndex, _ := hardwareE2EWorkflowStepNamed(t, leg, "Write the e2e config")
	if publish.ID != "node-user" || publishIndex >= configIndex || publish.If != "" {
		t.Fatalf("the account is published, unconditionally, before the config uses it: id %q at %d, config at %d, if %q", publish.ID, publishIndex, configIndex, publish.If)
	}
	for _, want := range []string{`^[A-Za-z0-9._-]*$`, "exit 1", `printf 'value=%s\n' "$NODE_USER" >> "$GITHUB_OUTPUT"`} {
		if !strings.Contains(publish.Run, want) {
			t.Fatalf("the account step lacks %q", want)
		}
	}
}

// The account pattern is the only thing between an input and the job's output file, so run it: a
// newline must not slip past the anchors and start a second output line.
func TestHardwareE2EWorkflowNodeAccountPatternRefusesWhatCouldForgeAnOutput(t *testing.T) {
	doc, _ := hardwareE2ELoadWorkflow(t)
	_, publish := hardwareE2EWorkflowStepNamed(t, doc.Jobs["hardware-e2e"], "Publish the node account")
	for value, admitted := range map[string]bool{
		"":                    true,
		"ubuntu":              true,
		"node.user_1-a":       true,
		"ubuntu\nmatrix=evil": false,
		"ubuntu\n":            false,
		"a b":                 false,
		"a=b":                 false,
		"$(id)":               false,
		"a;b":                 false,
	} {
		cmd := exec.Command("bash", "-c", "set -euo pipefail\n"+
			publish.Run[strings.Index(publish.Run, "if [["):strings.Index(publish.Run, "printf")]+"echo admitted")
		cmd.Env = append(os.Environ(), "NODE_USER="+value)
		out, err := cmd.CombinedOutput()
		if got := err == nil && strings.Contains(string(out), "admitted"); got != admitted {
			t.Errorf("NODE_USER %q admitted = %v, want %v:\n%s", value, got, admitted, out)
		}
	}
}

func TestHardwareE2EWorkflowAdmitsOnlyARealRunFromMainAndItsFirstAttempt(t *testing.T) {
	doc, _ := hardwareE2ELoadWorkflow(t)
	plan := doc.Jobs["plan"]
	_, admit := hardwareE2EWorkflowStepNamed(t, plan, "Admit the dispatch")
	if plan.Steps[0].Name != "Admit the dispatch" {
		t.Fatal("everything that can be refused before a checkout or a machine is refused first")
	}
	for _, want := range []string{
		`nvkr-outputs | build-subject) ;;`,
		`[ "$REF" = refs/heads/main ]`,
		`[ "$RUN_ATTEMPT" = 1 ]`,
		`if [ "$DRY_RUN" != true ]; then`,
		`^inputs/qualification/[A-Za-z0-9._-]+\.json$`,
		`[ -z "$MANIFEST" ]`,
		`[ "$SKEW" != true ]`,
	} {
		if !strings.Contains(admit.Run, want) {
			t.Fatalf("the admission step lacks %q", want)
		}
	}
	if admit.Env["REF"] != "${{ github.ref }}" || admit.Env["RUN_ATTEMPT"] != "${{ github.run_attempt }}" {
		t.Fatalf("the admission step must read the ref and attempt from the run, got %v", admit.Env)
	}
	// The harness refuses a real run from any other ref; the workflow refuses before the harness does.
	_, commits := hardwareE2EWorkflowStepNamed(t, plan, "Resolve the subject and harness commits")
	for _, want := range []string{
		`.source.commit | select(test("^[0-9a-f]{40}$"))`,
		`[ "$SOURCE" = build-subject ] && [ "$harness" != "$subject" ]`,
	} {
		if !strings.Contains(commits.Run, want) {
			t.Fatalf("the commit step lacks %q", want)
		}
	}
	if plan.Env["PLAN_FIXTURE"] != "nvkr/"+hardwareE2EPlanFile || plan.Env["PLAN_PROVENANCE"] != "nvkr/"+hardwareE2EProvenanceFile {
		t.Fatalf("the workflow must name the committed plan fixture and its provenance, got %q and %q", plan.Env["PLAN_FIXTURE"], plan.Env["PLAN_PROVENANCE"])
	}
	_, nvke := hardwareE2EWorkflowStepNamed(t, plan, "Check out the NVKE harness")
	if nvke.With["repository"] != "dsx-ai-factory/nvke" || nvke.With["ref"] != "${{ steps.commits.outputs.harness }}" ||
		nvke.With["fetch-depth"] != float64(0) || nvke.With["fetch-tags"] != true {
		t.Fatalf("the harness checkout must be NVKE at the resolved commit with its whole history and tags, got %v", nvke.With)
	}
}

func TestHardwareE2EWorkflowReferencesScriptsThatExist(t *testing.T) {
	_, raw := hardwareE2ELoadWorkflow(t)
	root := hardwareE2ERoot(t)
	// The two checks are not scripts: they are subcommands of one Go binary, pinned by
	// TestHardwareE2EWorkflowBuildsItsChecksWithNVKRsOwnToolchain.
	reference := regexp.MustCompile(`scripts/(hardware-e2e-[a-z-]+\.sh)`)
	seen := map[string]bool{}
	for _, match := range reference.FindAllStringSubmatch(raw, -1) {
		seen[match[1]] = true
	}
	for _, want := range []string{
		"hardware-e2e-substitute-plan.sh", "hardware-e2e-matrix.sh", "hardware-e2e-stage-inputs.sh",
		"hardware-e2e-build-subject.sh", "hardware-e2e-summary.sh",
	} {
		if !seen[want] {
			t.Fatalf("the workflow does not use %s", want)
		}
	}
	for script := range seen {
		info, err := os.Stat(filepath.Join(root, "scripts", script))
		if err != nil {
			t.Fatalf("the workflow names a script that does not exist: %v", err)
		}
		if strings.HasSuffix(script, ".sh") && info.Mode()&0o111 == 0 {
			t.Fatalf("scripts/%s is run directly and must be executable", script)
		}
	}
}

// The node image check and the JUnit check are NVKR's own Go, one binary built from the checkout with its
// pinned toolchain. A leg builds it before the NVKE harness's Go takes the path, so neither an interpreter
// nor another project's toolchain stands between a job and what it checks.
func TestHardwareE2EWorkflowBuildsItsChecksWithNVKRsOwnToolchain(t *testing.T) {
	doc, raw := hardwareE2ELoadWorkflow(t)
	if strings.Contains(raw, "python") {
		t.Fatal("the workflow runs no interpreter: its checks are one Go binary")
	}
	if _, err := os.Stat(filepath.Join(hardwareE2ERoot(t), "internal", "hwe2e", "main.go")); err != nil {
		t.Fatalf("the workflow builds a package that does not exist: %v", err)
	}
	const binary = `"$GITHUB_WORKSPACE/hwe2e-bin/hwe2e"`
	for _, tc := range []struct{ job, use string }{
		{"verify-node-images", binary + " check-node-image"},
		{"hardware-e2e", binary + " check-junit"},
	} {
		job := doc.Jobs[tc.job]
		setupAt, setup := hardwareE2EWorkflowStepNamed(t, job, "Set up Go for the NVKR checks")
		if !strings.HasPrefix(setup.Uses, "actions/setup-go@") || setup.With["go-version-file"] != "nvkr/.go-version" || setup.With["cache"] != false {
			t.Fatalf("%s: Go for the checks must come from NVKR's pin, uncached, got %q %v", tc.job, setup.Uses, setup.With)
		}
		buildAt, build := hardwareE2EWorkflowStepNamed(t, job, "Build the NVKR checks")
		if build.WorkingDirectory != "nvkr" || build.Env["GOTOOLCHAIN"] != "local" || build.Env["CGO_ENABLED"] != "0" {
			t.Fatalf("%s: the checks must build in the NVKR checkout with the pinned local toolchain and no cgo, got %q %v", tc.job, build.WorkingDirectory, build.Env)
		}
		if !strings.Contains(build.Run, `go build -trimpath -o "$GITHUB_WORKSPACE/hwe2e-bin/hwe2e" ./internal/hwe2e`) {
			t.Fatalf("%s: the build must write the binary the checks name, got %q", tc.job, build.Run)
		}
		if setupAt >= buildAt {
			t.Fatalf("%s: Go is set up at step %d, after the build at step %d", tc.job, setupAt, buildAt)
		}
		used := false
		for index, step := range job.Steps {
			if !strings.Contains(step.Run, tc.use) {
				continue
			}
			used = true
			if index <= buildAt {
				t.Fatalf("%s: step %q runs the checks at step %d, before they are built at step %d", tc.job, step.Name, index, buildAt)
			}
		}
		if !used {
			t.Fatalf("%s never runs %s", tc.job, tc.use)
		}
	}
	// A leg's checks are built before NVKE's Go replaces the toolchain they were built with.
	leg := doc.Jobs["hardware-e2e"]
	buildAt, _ := hardwareE2EWorkflowStepNamed(t, leg, "Build the NVKR checks")
	if nvkeGoAt, _ := hardwareE2EWorkflowStepNamed(t, leg, "Set up Go"); buildAt >= nvkeGoAt {
		t.Fatalf("a leg builds its checks at step %d, after the NVKE harness's Go at step %d", buildAt, nvkeGoAt)
	}
}

func TestHardwareE2EWorkflowCallsOnlyTheHarnessStepsItIsMeantTo(t *testing.T) {
	doc, _ := hardwareE2ELoadWorkflow(t)
	call := regexp.MustCompile(`qualify-e2e\.sh\s+([a-z-]+)`)
	// matrix, controller-image, site-inputs and artifacts are NVKE's: the first is replaced by
	// hardware-e2e-matrix.sh and the others would build or pull what this lane stages itself.
	allowed := map[string]bool{"stream-env": true, "resolve": true, "config": true, "run": true, "withhold": true}
	used := map[string]bool{}
	for _, name := range hardwareE2EJobNames(doc) {
		for _, step := range doc.Jobs[name].Steps {
			for _, match := range call.FindAllStringSubmatch(step.Run, -1) {
				if !allowed[match[1]] {
					t.Fatalf("%s/%s calls the harness step %q, which this lane does not use", name, step.Name, match[1])
				}
				used[match[1]] = true
			}
		}
	}
	for step := range allowed {
		if !used[step] {
			t.Fatalf("the lane never calls the harness step %q it needs", step)
		}
	}
}

func TestHardwareE2EWorkflowSelectsItsArtifactSourceBeforeAnyMachine(t *testing.T) {
	doc, _ := hardwareE2ELoadWorkflow(t)
	leg := doc.Jobs["hardware-e2e"]
	// Every step is resolved by name first, so a renamed or removed step fails here and cannot be read as
	// position zero and pass an order check by accident.
	at := func(name string) int {
		t.Helper()
		index, _ := hardwareE2EWorkflowStepNamed(t, leg, name)
		return index
	}
	want := map[string]string{
		"Stage the NVKR-built inputs":              "inputs.artifact-source == 'nvkr-outputs'",
		"Build the inputs from the subject commit": "inputs.artifact-source == 'build-subject'",
		"Log in to GHCR":                           "inputs.artifact-source == 'nvkr-outputs'",
		"Set up ORAS":                              "inputs.artifact-source == 'nvkr-outputs'",
		"Read the pinned ORAS version":             "inputs.artifact-source == 'nvkr-outputs'",
		"Install tsh and tbot":                     "env.DRY_RUN != 'true'",
		"Start the Teleport identity daemon":       "env.DRY_RUN != 'true'",
		"Check the run covered what it selected":   "always() && env.DRY_RUN != 'true'",
		"Record the qualification result":          "always() && env.DRY_RUN != 'true'",
		"Check every artifact the plan names":      "inputs.verify-plan",
		"Check the selected specs fit the stage":   "",
	}
	for name, condition := range want {
		_, step := hardwareE2EWorkflowStepNamed(t, leg, name)
		if step.If != condition {
			t.Fatalf("step %q runs when %q, want %q", name, step.If, condition)
		}
	}
	run := at("Run the e2e suite${{ inputs.dry-run && ' (dry run)' || '' }}")
	for _, name := range []string{
		"Stage the NVKR-built inputs", "Build the inputs from the subject commit", "Start the Teleport identity daemon",
		"Write the e2e config", "List the specs the run selects", "Check the selected specs fit the stage",
		"Resolve the stream", "Check the leg is what the plan job prepared",
	} {
		if at(name) >= run {
			t.Fatalf("step %q must run before the suite takes a machine", name)
		}
	}
	// The stamp precedes every step that takes time, so the recorded start belongs to the leg.
	if at("Admit a GitHub-hosted AMD64 runner and the first attempt") != 0 || at("Check out NVKR") != 1 || at("Stamp the leg") != 2 {
		t.Fatal("a leg must admit its runner, fetch NVKR's scripts and stamp its start before it does any other work")
	}
	// "Re-run failed jobs" does not run the plan job again, so the plan job's own attempt check cannot
	// stop a leg from running a second time beside the first attempt's legs.
	_, admit := hardwareE2EWorkflowStepNamed(t, leg, "Admit a GitHub-hosted AMD64 runner and the first attempt")
	for _, want := range []string{
		`test "$RUNNER_ENVIRONMENT" = github-hosted`,
		`test "$(uname -s)/$(uname -m)" = Linux/x86_64`,
		`if [ "$DRY_RUN" != true ] && [ "$GITHUB_RUN_ATTEMPT" != 1 ]; then`,
		"exit 1",
	} {
		if !strings.Contains(admit.Run, want) {
			t.Fatalf("a leg's admission lacks %q", want)
		}
	}
	// Both staging steps run for the versions the stream resolved, and the config names what they staged.
	for _, staging := range []string{"Stage the NVKR-built inputs", "Build the inputs from the subject commit"} {
		if at(staging) <= at("Resolve the stream") {
			t.Fatalf("%q stages for the versions the stream resolved, so it follows the resolution", staging)
		}
		if at("Write the e2e config") <= at(staging) {
			t.Fatalf("the e2e config names the staged images, so it is written after %q", staging)
		}
	}
	_, list := hardwareE2EWorkflowStepNamed(t, leg, "List the specs the run selects")
	const filter = `"site-controller && !${SITE_TENANT_EXCLUDE}"`
	for _, flag := range []string{
		"-ginkgo.dry-run", "-ginkgo.label-filter=" + filter,
		`-ginkgo.junit-report="$GITHUB_WORKSPACE/leg/dry-run.junit.xml"`, `-e2e.site-stage="${SITE_STAGE}"`,
		"-e2e.capabilities=",
	} {
		if !strings.Contains(list.Run, flag) {
			t.Fatalf("the spec list must use the real run's selection; it lacks %q", flag)
		}
	}
	// The dry report is validated before any machine, against the stage and the filter this lane expects;
	// the real run's check repeats the filter, so a run cannot be judged against a different selection.
	if at("Check the selected specs fit the stage") <= at("List the specs the run selects") {
		t.Fatal("the dry report is validated after it is written")
	}
	_, dry := hardwareE2EWorkflowStepNamed(t, leg, "Check the selected specs fit the stage")
	_, covered := hardwareE2EWorkflowStepNamed(t, leg, "Check the run covered what it selected")
	for name, step := range map[string]hardwareE2EWorkflowStep{"dry validation": dry, "coverage check": covered} {
		for _, flag := range []string{`"$GITHUB_WORKSPACE/hwe2e-bin/hwe2e" check-junit`, "--dry leg/dry-run.junit.xml", `--stage "$SITE_STAGE"`, "--label-filter " + filter} {
			if !strings.Contains(step.Run, flag) {
				t.Fatalf("the %s lacks %q", name, flag)
			}
		}
	}
	if !strings.Contains(dry.Run, "--dry-only") || strings.Contains(dry.Run, "--real") {
		t.Fatalf("the pre-run validation reads the dry report only, got %q", dry.Run)
	}
	if !strings.Contains(covered.Run, "--real nvke/controller/test/e2e/qualify-e2e.junit.xml") || strings.Contains(covered.Run, "--dry-only") {
		t.Fatalf("the post-run check reads the real report, got %q", covered.Run)
	}
}

func TestHardwareE2EWorkflowSummaryIsWrittenAndCheckedBeforeTheRunPasses(t *testing.T) {
	doc, _ := hardwareE2ELoadWorkflow(t)
	job := doc.Jobs["summarize"]
	write, writeStep := hardwareE2EWorkflowStepNamed(t, job, "Write the run summary")
	check, checkStep := hardwareE2EWorkflowStepNamed(t, job, "Check the run summary")
	upload, uploadStep := hardwareE2EWorkflowStepNamed(t, job, "Upload the run summary")
	if !(write < check && check < upload) {
		t.Fatal("the summary is written, then checked from its bytes, then published")
	}
	for _, want := range []string{
		`--artifact-source "$SOURCE"`, `--stage "$SITE_STAGE"`, `--harness-commit "$HARNESS_COMMIT"`,
		`--orchestrator-commit "$ORCHESTRATOR_COMMIT"`, `--orchestrator-run "$ORCHESTRATOR_RUN"`,
		"--plan plan-out/plan.json", "--substitutions plan-out/substitutions.json", "--matrix matrix.json",
		`--node-image-check "$check"`, `--leg "${leg%/}"`, `scripts/hardware-e2e-summary.sh "${args[@]}"`,
	} {
		if !strings.Contains(writeStep.Run, want) {
			t.Fatalf("the summary write lacks %q", want)
		}
	}
	// Node image checks belong to NVKR outputs only; a build from the subject commit passes none.
	if !strings.Contains(writeStep.Run, `if [ "$SOURCE" = nvkr-outputs ]; then`) {
		t.Fatal("node image checks must be passed only for NVKR outputs")
	}
	if checkStep.Run != "scripts/hardware-e2e-summary.sh assert run-summary.json" {
		t.Fatalf("the check must run the contract over the file on disk, got %q", checkStep.Run)
	}
	if uploadStep.With["path"] != "run-summary.json" || uploadStep.With["if-no-files-found"] != "error" {
		t.Fatal("the summary upload must require the summary")
	}
	// A failed check must fail the run: nothing after it may mask the result.
	for _, step := range job.Steps[check+1:] {
		if step.If != "" || strings.Contains(step.Run, "|| true") {
			t.Fatalf("step %q after the check must not mask its result", step.Name)
		}
	}
	if checkStep.If != "" || writeStep.If != "" {
		t.Fatal("the summary write and check run unconditionally once the job runs")
	}
	if job.Env["SOURCE"] != "${{ inputs.artifact-source }}" || job.Env["SITE_STAGE"] != "${{ inputs.stage }}" {
		t.Fatalf("the summary reads the dispatched source and stage, got %v", job.Env)
	}
}

func TestHardwareE2EWorkflowChecksEachNodeImageByTheMethodItsOutputNamed(t *testing.T) {
	doc, raw := hardwareE2ELoadWorkflow(t)
	job := doc.Jobs["verify-node-images"]
	var check *hardwareE2EWorkflowStep
	for index := range job.Steps {
		if job.Steps[index].Name == "Check each node image" {
			check = &job.Steps[index]
		}
	}
	if check == nil {
		t.Fatal("verify-node-images has no step that checks each node image")
	}
	for _, want := range []string{
		"set -euo pipefail",
		"for slot in os-image-amd64 os-image-arm64; do",
		`"$GITHUB_WORKSPACE/hwe2e-bin/hwe2e" check-node-image --slot "$slot" --substitutions plan-out/substitutions.json --out "checks/$slot.json"`,
	} {
		if !strings.Contains(check.Run, want) {
			t.Fatalf("the node image check lacks %q", want)
		}
	}
	// The method and the bytes come from the substituted plan's record, which the outputs manifest
	// wrote. A URL or a digest assembled here would let the workflow pick a method for itself.
	for _, forbidden := range []string{"--url", "--sha256", "curl", "wget", "jq "} {
		if strings.Contains(check.Run, forbidden) {
			t.Fatalf("the node image check must take everything from the substitution record, not %q", forbidden)
		}
	}
	// The lane never holds an object store credential: publishing and checking are separate runs.
	for _, forbidden := range []string{"configure-aws-credentials", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "role-to-assume", "S3_PUBLISH", "aws s3"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("the hardware E2E workflow must not contain %q: the publication workflow owns the object store", forbidden)
		}
	}
}

// The checks only count if the summary job receives exactly what the check job wrote, and the check
// job reads the substitution record the plan job wrote. A renamed or moved artifact would leave a
// run green with the checks missing or the wrong plan checked.
func TestHardwareE2EWorkflowHandsTheNodeImageChecksToTheSummary(t *testing.T) {
	doc, _ := hardwareE2ELoadWorkflow(t)
	const (
		plan   = "nvkr-hardware-e2e-plan-${{ github.run_id }}"
		checks = "nvkr-hardware-e2e-node-image-checks-${{ github.run_id }}"
	)
	only := func(job, uses, name string) hardwareE2EWorkflowStep {
		t.Helper()
		var found []hardwareE2EWorkflowStep
		for _, step := range doc.Jobs[job].Steps {
			if strings.HasPrefix(step.Uses, uses+"@") && step.With["name"] == name {
				found = append(found, step)
			}
		}
		if len(found) != 1 {
			t.Fatalf("job %s has %d steps using %s for artifact %q, want exactly one", job, len(found), uses, name)
		}
		return found[0]
	}
	download := only("verify-node-images", "actions/download-artifact", plan)
	if download.With["path"] != "plan-out" {
		t.Fatalf("the check job must download the plan into plan-out, where the check reads substitutions.json, got %v", download.With["path"])
	}
	upload := only("verify-node-images", "actions/upload-artifact", checks)
	if upload.With["path"] != "checks" || upload.With["if-no-files-found"] != "error" {
		t.Fatalf("the check job must upload checks and fail when it is empty, got path %v and if-no-files-found %v", upload.With["path"], upload.With["if-no-files-found"])
	}
	summary := only("summarize", "actions/download-artifact", checks)
	if summary.With["path"] != "checks" || summary.If != "inputs.artifact-source == 'nvkr-outputs'" {
		t.Fatalf("the summary job must download the checks into checks for NVKR outputs only, got path %v if %q", summary.With["path"], summary.If)
	}
}

func TestHardwareE2EWorkflowNamesNoQualificationOrReleaseAuthority(t *testing.T) {
	_, raw := hardwareE2ELoadWorkflow(t)
	// The lane informs and gates nothing: no step signs, pushes, publishes or tags, and the
	// qualify tool's result stays in the run's own artifacts.
	for _, forbidden := range []string{
		"cosign", "docker push", "oras push", "gh release", "git tag", "git push", "testgrid", "TESTGRID",
		"qualify publish", "qualify gate", "release publish", "--publish",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("the hardware E2E workflow must not contain %q: it publishes and gates nothing", forbidden)
		}
	}
	// The authority field is the summary script's contract, covered by its own tests; the workflow's
	// part is to record the tool's result per leg and to run that contract at the end.
	if want := `"$GITHUB_WORKSPACE/qualify-bin/qualify" result`; !strings.Contains(raw, want) {
		t.Fatalf("the workflow lacks %q", want)
	}
}
