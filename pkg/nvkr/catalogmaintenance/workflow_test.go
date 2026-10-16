// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"golang.org/x/sys/unix"
	"sigs.k8s.io/yaml"
)

// enroll installs the fixture's policy where the workflow looks for it.
func enroll(t *testing.T, cfg Config) {
	t.Helper()
	if err := os.MkdirAll(PolicyDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, PolicyDirectory+"/fixture"+PolicySuffix, cfg)
}

func TestWorkflowIsDeterministicAndConfinesWritePermissionToItsOwnJob(t *testing.T) {
	cfg, _ := maintenanceFixture(t)
	enroll(t, cfg)
	options := WorkflowOptions{Schedule: DefaultSchedule}
	first, err := Workflow(t.Context(), options, "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	second, err := Workflow(t.Context(), options, "test (deadbeef)")
	if err != nil || !bytes.Equal(first, second) {
		t.Fatalf("unstable workflow: %v", err)
	}
	var workflow struct {
		Permissions map[string]string         `json:"permissions"`
		Jobs        map[string]map[string]any `json:"jobs"`
	}
	if err := yaml.Unmarshal(first, &workflow); err != nil {
		t.Fatal(err)
	}
	if len(workflow.Jobs) != 4 || workflow.Permissions["contents"] != "read" {
		t.Fatalf("want a read-only workflow with discover, refresh, select and pull-request jobs: %v", workflow)
	}
	text := string(first)
	for _, want := range []string{
		"schedule:", "'0 2 * * *'", "cancel-in-progress: true", "group: catalog-maintenance-${{ github.ref }}",
		"if-no-files-found: error", "fromJSON(needs.discover.outputs.definitions)", "fromJSON(needs.select.outputs.legs)",
		"find inputs/policies -mindepth 1 -maxdepth 1 -name '*-catalog-maintenance.yaml'", "catalog refresh --config", "jq -er --arg definition",
		"catalog-maintenance/${DEFINITION}", "gh pr create", "GH_TOKEN: ${{ steps.app.outputs.token }}",
		"client-id: ${{ secrets.RELEASE_BOT_CLIENT }}", "private-key: ${{ secrets.RELEASE_BOT_SECRET }}",
		"uses: " + appTokenAction + "  # " + appTokenVersion, "permission-contents: write", "permission-pull-requests: write", "permission-workflows: write", "gh pr list --head", "git/commits", "git/refs", "-ldflags '-X main.version=test -X main.commit=deadbeef'",
		"run local", "steps.policy.outputs.validation == 'local-run'",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s", want)
		}
	}
	for _, bad := range []string{"pull_request_target", "id-token:", "--dry-run", "--publish", "self-hosted", "SOURCE_TOKEN", "https://x-access-token", "NVKR_PIN_BOT", "secrets.GITHUB_TOKEN", "git push", "extraheader", "x-access-token", "user.name"} {
		if strings.Contains(text, bad) {
			t.Fatalf("unexpected %s", bad)
		}
	}
	head, tail, _ := strings.Cut(text, "\n  pull-request:\n")
	for _, secret := range []string{"secrets.RELEASE_BOT_CLIENT", "secrets.RELEASE_BOT_SECRET", "steps.app.outputs.token", "git/commits", "gh pr"} {
		if strings.Contains(head, secret) || !strings.Contains(tail, secret) {
			t.Fatalf("%s must appear only in the pull-request job", secret)
		}
	}
	// The workflow token never writes: no job grants a write permission and nothing reads it. The App token
	// reaches gh only as GH_TOKEN in the bot identity, publish and cleanup steps; the action revokes it.
	if strings.Count(text, ": write") != 3 || strings.Contains(text, "github.token") || strings.Count(text, "GH_TOKEN: ${{ steps.app.outputs.token }}") != 3 ||
		strings.Count(text, "steps.app.outputs.token") != 3 {
		t.Fatal("the workflow token must stay read-only and the App token must reach only the bot identity, publish and cleanup steps")
	}
}

func TestWorkflowSourceCheckoutAndOptionAdmission(t *testing.T) {
	cfg := sourceCheckoutFixture(t)
	enroll(t, cfg)
	raw, err := Workflow(t.Context(), checkoutOptions, "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "schedule:") {
		t.Fatal("an empty schedule must mean manual dispatch only")
	}
	_, tail, _ := strings.Cut(text, "\n  pull-request:\n")
	if !strings.Contains(text, "catalog refresh --config") || !strings.Contains(text, " --source-repository example/nvke --json") {
		t.Fatal("the refresh must be told which repository the workflow transports")
	}
	if strings.Count(text, "secrets.SOURCE_TOKEN") != 1 || strings.Contains(tail, "SOURCE_TOKEN") || !strings.Contains(text, "https://github.com/example/nvke") {
		t.Fatal("the source secret must reach one checkout step, with later steps reading through a credential-free local override")
	}
	if err := yaml.Unmarshal(raw, &map[string]any{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		opts WorkflowOptions
	}{
		{"schedule", WorkflowOptions{Schedule: "* * * * *"}},
		{"secret without repository", WorkflowOptions{SourceSecret: "SOURCE_TOKEN"}},
		{"repository without secret", WorkflowOptions{SourceRepository: "owner/repo"}},
		{"reserved secret", WorkflowOptions{SourceRepository: "owner/repo", SourceSecret: "GITHUB_TOKEN"}},
		{"injected secret", WorkflowOptions{SourceRepository: "owner/repo", SourceSecret: "X }}"}},
		{"repository that transports no selected root", WorkflowOptions{SourceRepository: "owner/repo", SourceSecret: "SOURCE_TOKEN"}},
		{"git suffix", WorkflowOptions{SourceRepository: "owner/repo.git", SourceSecret: "SOURCE_TOKEN"}},
		{"reserved App secret", WorkflowOptions{AppClientSecret: "GITHUB_TOKEN", AppKeySecret: "BOT_KEY"}},
		{"injected App secret", WorkflowOptions{AppClientSecret: "BOT_CLIENT", AppKeySecret: "X }}"}},
		{"one App secret", WorkflowOptions{AppClientSecret: "BOT_CLIENT"}},
		{"same App secrets", WorkflowOptions{AppClientSecret: "BOT_KEY", AppKeySecret: "BOT_KEY"}},
		{"App secret reuses the source secret", WorkflowOptions{SourceRepository: "owner/repo", SourceSecret: "SOURCE_TOKEN", AppClientSecret: "SOURCE_TOKEN", AppKeySecret: "BOT_KEY"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := Workflow(t.Context(), tc.opts, "test (deadbeef)")
			assertCode(t, err, e.CodeUsage)
			if len(out) != 0 {
				t.Fatal("rejected options emitted a workflow")
			}
		})
	}
}

// retireProductionBundle replaces the fixture's committed production bundle with
// the one the rewritten Environment selects, as a real layout change would.
func retireProductionBundle(t *testing.T, cfg Config) {
	t.Helper()
	for _, name := range []string{".github/nvkr-run", ".github/workflows/nvkr-run.yml"} {
		if err := os.RemoveAll(name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := lifecycle.GenerateFiles(t.Context(), cfg.Definition, cfg.Environment, "test (deadbeef)", "github", ".", false); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowPatchSelectsTheGeneratedBundleLayout(t *testing.T) {
	cfg, _ := maintenanceFixture(t)
	baseline := readFixture(t, cfg.Environment)
	for _, test := range []struct {
		name, extra, directory, workflow, excluded string
	}{
		{"production", "", ".github/nvkr-run", ".github/workflows/nvkr-run.yml", "nvkr-dev"},
		{"development", "githubDevelopment: true\n", ".github/nvkr-dev", ".github/workflows/nvkr-dev.yml", "nvkr-run"},
	} {
		t.Run(test.name, func(t *testing.T) {
			writeFixture(t, cfg.Environment, append(append([]byte(nil), baseline...), test.extra...))
			if test.extra != "" {
				retireProductionBundle(t, cfg)
			}
			enroll(t, cfg)
			raw, err := Workflow(t.Context(), WorkflowOptions{}, "test (deadbeef)")
			if err != nil {
				t.Fatal(err)
			}
			if want := `git add -- "$POLICY_CATALOG/pins.lock.yaml" '` + test.directory + "' '" + test.workflow + "'"; !bytes.Contains(raw, []byte(want)) {
				t.Fatalf("review patch omits selected bundle paths: %s", raw)
			}
			if want := `"$catalog/pins.lock.yaml" | '` + test.directory + `'/* | '` + test.workflow + `')`; !bytes.Contains(raw, []byte(want)) {
				t.Fatalf("patch admission omits selected bundle paths: %s", raw)
			}
			if bytes.Contains(raw, []byte(test.excluded)) {
				t.Fatalf("review patch includes a different bundle owner: %s", raw)
			}
		})
	}
}

func TestWorkflowRejectsUnusablePoliciesBeforeOutput(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		t.Chdir(t.TempDir())
		out, err := Workflow(t.Context(), WorkflowOptions{}, "test (deadbeef)")
		assertCode(t, err, e.CodeInvalidCatalog)
		if len(out) != 0 {
			t.Fatal("no policies emitted a workflow")
		}
	})
	t.Run("several policies share one generated root", func(t *testing.T) {
		cfg, _ := maintenanceFixture(t)
		enroll(t, cfg)
		writeJSON(t, PolicyDirectory+"/second"+PolicySuffix, cfg)
		out, err := Workflow(t.Context(), WorkflowOptions{}, "test (deadbeef)")
		assertCode(t, err, e.CodeEngineCapability)
		if len(out) != 0 {
			t.Fatal("overlapping policies emitted a workflow")
		}
	})
	t.Run("unsupported policy name", func(t *testing.T) {
		cfg, _ := maintenanceFixture(t)
		if err := os.MkdirAll(PolicyDirectory, 0o700); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, PolicyDirectory+"/Bad_Name"+PolicySuffix, cfg)
		_, err := Workflow(t.Context(), WorkflowOptions{}, "test (deadbeef)")
		assertCode(t, err, e.CodeUsage)
	})
	// The renderer's glob matches the discover step: nested policies never enroll, a hidden name is unsupported.
	for _, tc := range []struct {
		name   string
		enroll bool
		extra  string
		code   e.Code
	}{
		{"only a nested policy", false, "nested/alpha", e.CodeInvalidCatalog},
		{"one beside a nested policy", true, "nested/beta", ""},
		{"hidden name", false, ".hidden", e.CodeUsage},
		{"one beside a hidden policy", true, ".hidden", e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := maintenanceFixture(t)
			if tc.enroll {
				enroll(t, cfg)
			}
			if err := os.MkdirAll(filepath.Dir(PolicyDirectory+"/"+tc.extra), 0o700); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, PolicyDirectory+"/"+tc.extra+PolicySuffix, cfg)
			out, err := Workflow(t.Context(), WorkflowOptions{}, "test (deadbeef)")
			if tc.code == "" {
				if err != nil || len(out) == 0 {
					t.Fatalf("the top-level policy did not render: %v", err)
				}
				return
			}
			assertCode(t, err, tc.code)
			if len(out) != 0 {
				t.Fatal("a refused policy layout emitted a workflow")
			}
		})
	}
	t.Run("malformed generator", func(t *testing.T) {
		cfg, _ := maintenanceFixture(t)
		enroll(t, cfg)
		for _, version := range []string{"missing stamp", "v1.0.0 (bad;command)", "$(touch marker) (deadbeef)"} {
			out, err := Workflow(t.Context(), WorkflowOptions{}, version)
			assertCode(t, err, e.CodeInvalidCatalog)
			if len(out) != 0 {
				t.Fatal("malformed generator emitted workflow bytes")
			}
		}
	})
	t.Run("invalid policy", func(t *testing.T) {
		cfg, _ := maintenanceFixture(t)
		cfg.Validation = "qualified"
		enroll(t, cfg)
		_, err := Workflow(t.Context(), WorkflowOptions{}, "test (deadbeef)")
		assertCode(t, err, e.CodeUsage)
	})
}

func TestLocalValidationRejectsHostedEnvironmentBeforeMutationOrOutput(t *testing.T) {
	cfg, _ := maintenanceFixture(t)
	baseline := readFixture(t, cfg.Environment)
	beforeLock := readFixture(t, "catalog/pins.lock.yaml")
	for _, tc := range []struct{ name, extra string }{
		{"source authentication", "sourceAccess:\n  https://maintenance.example.invalid/source:\n    mirror: https://maintenance.example.invalid/source\n    credentialRef: github:SOURCE_TOKEN\n"},
		{"hosted dispatch", "githubDispatchBranches: [candidate]\n"},
		{"development", "githubDevelopment: true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeFixture(t, cfg.Environment, append(append([]byte(nil), baseline...), tc.extra...))
			planPath := ".github/nvkr-run/plan.json"
			if tc.name == "development" {
				retireProductionBundle(t, cfg)
				planPath = ".github/nvkr-dev/plan.json"
			}
			beforePlan := readFixture(t, planPath)
			cfg.Validation = "static"
			enroll(t, cfg)
			if _, err := Workflow(t.Context(), WorkflowOptions{}, "test (deadbeef)"); err != nil {
				t.Fatalf("static mode should remain supported: %v", err)
			}
			cfg.Validation = "local-run"
			enroll(t, cfg)
			raw, err := Workflow(t.Context(), WorkflowOptions{}, "test (deadbeef)")
			assertCode(t, err, e.CodeEngineCapability)
			if len(raw) != 0 {
				t.Fatal("unsupported local capability emitted a workflow")
			}
			calls := 0
			_, err = refresh(t.Context(), cfg, "test (deadbeef)", false, "", func(context.Context, catalog.SourcePinRequest) (catalog.SourcePinUpdate, error) {
				calls++
				return catalog.SourcePinUpdate{}, nil
			})
			assertCode(t, err, e.CodeEngineCapability)
			if calls != 0 || !bytes.Equal(beforeLock, readFixture(t, "catalog/pins.lock.yaml")) || !bytes.Equal(beforePlan, readFixture(t, planPath)) {
				t.Fatal("unsupported local capability reached mutation")
			}
		})
	}
}

func TestWorkflowAdmitsOnlyTheCommittedGeneratorStamp(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version string
		setup   func(*testing.T)
		code    e.Code
	}{
		{"same stamp", "test (deadbeef)", func(*testing.T) {}, ""},
		{"different commit", "test (cafef00d)", func(*testing.T) {}, e.CodePipelineDrift},
		{"different version", "v9.9.9 (deadbeef)", func(*testing.T) {}, e.CodePipelineDrift},
		{"committed Plan missing", "test (deadbeef)", func(t *testing.T) {
			if err := os.Remove(".github/nvkr-run/plan.json"); err != nil {
				t.Fatal(err)
			}
		}, e.CodePipelineDrift},
		{"committed Plan unreadable", "test (deadbeef)", func(t *testing.T) { writeFixture(t, ".github/nvkr-run/plan.json", []byte("not json")) }, e.CodePipelineDrift},
		// The stamp is admitted before any catalog or source resolution, so it needs no network or private access.
		{"different stamp with an unreadable catalog", "test (cafef00d)", func(t *testing.T) {
			if err := os.Remove("catalog/registry.yaml"); err != nil {
				t.Fatal(err)
			}
		}, e.CodePipelineDrift},
		{"committed Plan symlink", "test (deadbeef)", func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "plan.json")
			if err := os.WriteFile(target, readFixture(t, ".github/nvkr-run/plan.json"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(".github/nvkr-run/plan.json"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, ".github/nvkr-run/plan.json"); err != nil {
				t.Fatal(err)
			}
		}, e.CodePipelineDrift},
		{"committed Plan FIFO", "test (deadbeef)", func(t *testing.T) {
			if err := os.Remove(".github/nvkr-run/plan.json"); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(".github/nvkr-run/plan.json", 0o600); err != nil {
				t.Fatal(err)
			}
		}, e.CodePipelineDrift},
		{"committed Plan oversized", "test (deadbeef)", func(t *testing.T) {
			writeFixture(t, ".github/nvkr-run/plan.json", bytes.Repeat([]byte(" "), int(factory.MaxPlanBytes)+1))
		}, e.CodePipelineDrift},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, _ := maintenanceFixture(t)
			enroll(t, cfg)
			tc.setup(t)
			out, err := Workflow(t.Context(), WorkflowOptions{}, tc.version)
			if tc.code == "" {
				if err != nil || len(out) == 0 {
					t.Fatalf("matching stamp rejected: %v", err)
				}
				return
			}
			assertCode(t, err, tc.code)
			if len(out) != 0 {
				t.Fatal("generator drift emitted workflow bytes")
			}
		})
	}
}

func TestPlanStampAdmissionObservesCancellation(t *testing.T) {
	cfg, _ := maintenanceFixture(t)
	enroll(t, cfg)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assertCode(t, admitPlanStamp(ctx, ".github/nvkr-run/plan.json", "test (deadbeef)"), e.CodeTimeout)
}

type workflowStep struct {
	Name string            `json:"name"`
	ID   string            `json:"id"`
	If   string            `json:"if"`
	Uses string            `json:"uses"`
	Env  map[string]string `json:"env"`
	With map[string]any    `json:"with"`
	Run  string            `json:"run"`
}

// workflowJob names every key the renderer emits, so UnmarshalStrict fails on a key a test does not know.
type workflowJob struct {
	Needs          string            `json:"needs"`
	If             string            `json:"if"`
	RunsOn         string            `json:"runs-on"`
	TimeoutMinutes int               `json:"timeout-minutes"`
	Outputs        map[string]string `json:"outputs"`
	Strategy       map[string]any    `json:"strategy"`
	Permissions    map[string]string `json:"permissions"`
	Env            map[string]string `json:"env"`
	Steps          []workflowStep    `json:"steps"`
}

type renderedWorkflow struct {
	Name        string                 `json:"name"`
	On          map[string]any         `json:"true"` // YAML 1.1 reads the key `on` as the boolean true
	Permissions map[string]string      `json:"permissions"`
	Concurrency map[string]any         `json:"concurrency"`
	Jobs        map[string]workflowJob `json:"jobs"`
}

func parseWorkflow(t *testing.T, raw []byte) renderedWorkflow {
	t.Helper()
	var workflow renderedWorkflow
	if err := yaml.UnmarshalStrict(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	return workflow
}

func (job workflowJob) step(t *testing.T, name string) workflowStep {
	t.Helper()
	for _, step := range job.Steps {
		if step.Name == name {
			return step
		}
	}
	t.Fatalf("no step %q", name)
	return workflowStep{}
}

// The rendered job graph, gates and credential confinement are asserted on the parsed workflow, so a
// moved `if`, a dropped persist-credentials or a secret reaching another step cannot hide in a substring.
func TestWorkflowJobsAreGatedAndConfineCredentials(t *testing.T) {
	cfg := sourceCheckoutFixture(t)
	enroll(t, cfg)
	raw, err := Workflow(t.Context(), WorkflowOptions{Schedule: DefaultSchedule, SourceRepository: "example/nvke", SourceSecret: "SOURCE_TOKEN"}, "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	workflow := parseWorkflow(t, raw)
	if len(workflow.Jobs) != 4 {
		t.Fatalf("want discover, refresh, select and pull-request jobs: %v", workflow.Jobs)
	}
	if got := workflow.Permissions; len(got) != 1 || got["contents"] != "read" {
		t.Fatalf("the workflow must default to read-only contents: %v", got)
	}
	if workflow.Concurrency["group"] != "catalog-maintenance-${{ github.ref }}" || workflow.Concurrency["cancel-in-progress"] != true {
		t.Fatalf("concurrency must be per ref and cancel older runs: %v", workflow.Concurrency)
	}
	for name, want := range map[string]struct{ needs, condition string }{
		"discover":     {"", "github.ref == format('refs/heads/{0}', github.event.repository.default_branch)"},
		"refresh":      {"discover", ""},
		"select":       {"refresh", "${{ !cancelled() && needs.refresh.result != 'skipped' }}"},                                    //nolint:misspell // GitHub status function uses this exact spelling.
		"pull-request": {"select", "${{ !cancelled() && needs.select.result == 'success' && needs.select.outputs.legs != '[]' }}"}, //nolint:misspell // GitHub status function uses this exact spelling.
	} {
		job := workflow.Jobs[name]
		if job.Needs != want.needs || job.If != want.condition {
			t.Fatalf("job %s: needs %q if %q, want needs %q if %q", name, job.Needs, job.If, want.needs, want.condition)
		}
		if name != "pull-request" && job.Permissions != nil {
			t.Fatalf("job %s must inherit the read-only default: %v", name, job.Permissions)
		}
	}
	if got := workflow.Jobs["pull-request"].Permissions; len(got) != 1 || got["contents"] != "read" {
		t.Fatalf("the pull-request job's workflow token must be read-only: %v", got)
	}
	// Exactly one step receives the source secret, and it is the credential-free private checkout. Exactly one
	// step receives the App secrets, as the environment of the token step.
	appSecrets := map[string]string{"client-id": "${{ secrets.RELEASE_BOT_CLIENT }}", "private-key": "${{ secrets.RELEASE_BOT_SECRET }}"}
	var holders []string
	for name, job := range workflow.Jobs {
		for key, value := range job.Env {
			if strings.Contains(value, "secrets.") {
				t.Fatalf("job %s env %s references a secret: %s", name, key, value)
			}
		}
		for _, step := range job.Steps {
			for key, value := range step.Env {
				if !strings.Contains(value, "secrets.") {
					continue
				}
				if name != "pull-request" || step.Name != "Create the App token" || appSecrets[key] != value {
					t.Fatalf("step %q env %s references a secret: %s", step.Name, key, value)
				}
				holders = append(holders, name+"/"+step.Name+"/"+key)
			}
			for key, value := range step.With {
				input, _ := value.(string)
				if !strings.Contains(input, "secrets.") || key == "token" {
					continue
				}
				if name != "pull-request" || step.Name != "Create the App token" || appSecrets[key] != input {
					t.Fatalf("step %q input %s references a secret: %s", step.Name, key, input)
				}
				holders = append(holders, name+"/"+step.Name+"/"+key)
			}
			if token, ok := step.With["token"]; ok {
				if token != "${{ secrets.SOURCE_TOKEN }}" {
					t.Fatalf("step %q passes an unexpected token: %v", step.Name, token)
				}
				holders = append(holders, name+"/"+step.Name)
			}
			if strings.Contains(step.Run, "secrets.") {
				t.Fatalf("step %q reads a secret in its script", step.Name)
			}
		}
	}
	sort.Strings(holders)
	if want := []string{"pull-request/Create the App token/client-id", "pull-request/Create the App token/private-key", "refresh/Fetch the private source"}; !reflect.DeepEqual(holders, want) || strings.Count(string(raw), "secrets.") != 3 {
		t.Fatalf("each secret must reach exactly one step: %v", holders)
	}
	// Every checkout is credential-free; each of the default branch pins the triggering commit.
	checkouts := 0
	for name, job := range workflow.Jobs {
		for _, step := range job.Steps {
			if !strings.HasPrefix(step.Uses, "actions/checkout@") {
				continue
			}
			checkouts++
			if step.With["persist-credentials"] != false {
				t.Fatalf("%s/%s must not persist credentials: %v", name, step.Name, step.With)
			}
			if _, private := step.With["repository"]; !private && step.With["ref"] != "${{ github.sha }}" {
				t.Fatalf("%s/%s must check out the triggering commit: %v", name, step.Name, step.With)
			}
		}
	}
	if checkouts != 4 {
		t.Fatalf("want checkouts in discover, refresh (two) and pull-request, got %d", checkouts)
	}
	// Only the pull-request job's publish and cleanup steps pass the App token to gh, and only as GH_TOKEN.
	for name, job := range workflow.Jobs {
		for _, step := range job.Steps {
			holds := step.Env["GH_TOKEN"] == "${{ steps.app.outputs.token }}"
			publishes := name == "pull-request" && (step.Name == "Resolve the App bot identity" || step.Name == "Open or update the pull request" || step.Name == "Close the stale pull request")
			if holds != publishes {
				t.Fatalf("%s/%s: App token held=%v, want %v", name, step.Name, holds, publishes)
			}
		}
	}
	// The unchanged leg only reaches the cleanup step, and the changed leg never reaches it.
	pullRequest := workflow.Jobs["pull-request"]
	for name, want := range map[string]string{
		"Check the Definition name": "", "Check out the base commit": "${{ matrix.leg.changed }}", "Download the candidate": "${{ matrix.leg.changed }}",
		"Apply and check the candidate patch": "${{ matrix.leg.changed }}", "Open or update the pull request": "${{ matrix.leg.changed }}",
		"Close the stale pull request": "${{ !matrix.leg.changed }}", "Create the App token": "", "Resolve the App bot identity": "",
	} {
		if got := pullRequest.step(t, name).If; got != want {
			t.Fatalf("step %q: if %q, want %q", name, got, want)
		}
	}
}

// The Git URL rewrite is the one admission reasons about, so the rendered environment is exactly transportFor's.
func TestWorkflowRendersTheExactSourceTransport(t *testing.T) {
	cfg := sourceCheckoutFixture(t)
	enroll(t, cfg)
	raw, err := Workflow(t.Context(), checkoutOptions, "test (deadbeef)")
	if err != nil {
		t.Fatal(err)
	}
	transport := transportFor("example/nvke")
	want := map[string]string{"GIT_CONFIG_COUNT": "1", "GIT_CONFIG_KEY_0": transport.Key, "GIT_CONFIG_VALUE_0": transport.Value}
	if want["GIT_CONFIG_KEY_0"] != "url.${{ github.workspace }}/.nvkr-inputs/source/.insteadOf" || want["GIT_CONFIG_VALUE_0"] != "https://github.com/example/nvke" {
		t.Fatalf("the transport changed: %v", want)
	}
	refresh := parseWorkflow(t, raw).Jobs["refresh"]
	for _, name := range []string{"Refresh pins and regenerate the Run", "Build and independently observe every candidate output"} {
		step := refresh.step(t, name)
		for key, value := range want {
			if step.Env[key] != value {
				t.Fatalf("step %q env %s = %q, want %q", name, key, step.Env[key], value)
			}
		}
	}
	// Steps that run no repository code or private source need no rewrite.
	for _, name := range []string{"Set up Go", "Build the nvkr CLI"} {
		if _, ok := refresh.step(t, name).Env["GIT_CONFIG_KEY_0"]; ok {
			t.Fatalf("step %q must not carry the rewrite", name)
		}
	}
}

func TestWorkflowHeaderReproducesTheCommand(t *testing.T) {
	cfg := sourceCheckoutFixture(t)
	enroll(t, cfg)
	for _, tc := range []struct {
		name string
		opts WorkflowOptions
		want string
	}{
		{"defaults", WorkflowOptions{Schedule: DefaultSchedule}, "# Generated by nvkr catalog update-ci — do not edit; regenerate.\n"},
		{"source", WorkflowOptions{Schedule: DefaultSchedule, SourceRepository: "example/nvke", SourceSecret: "SOURCE_TOKEN"},
			"# Generated by nvkr catalog update-ci --source-checkout example/nvke:SOURCE_TOKEN — do not edit; regenerate.\n"},
		{"manual only", WorkflowOptions{}, "# Generated by nvkr catalog update-ci --schedule '' — do not edit; regenerate.\n"},
		{"custom schedule and source", WorkflowOptions{Schedule: "0 6 * * 1", SourceRepository: "example/nvke", SourceSecret: "SOURCE_TOKEN"},
			"# Generated by nvkr catalog update-ci --schedule '0 6 * * 1' --source-checkout example/nvke:SOURCE_TOKEN — do not edit; regenerate.\n"},
		{"App secrets", WorkflowOptions{Schedule: DefaultSchedule, AppClientSecret: "BOT_CLIENT", AppKeySecret: "BOT_KEY"},
			"# Generated by nvkr catalog update-ci --app-secrets BOT_CLIENT:BOT_KEY — do not edit; regenerate.\n"},
		{"default App secrets spelled out", WorkflowOptions{Schedule: DefaultSchedule, AppClientSecret: DefaultAppClientSecret, AppKeySecret: DefaultAppKeySecret},
			"# Generated by nvkr catalog update-ci — do not edit; regenerate.\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := Workflow(t.Context(), tc.opts, "test (deadbeef)")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(string(raw), "# SPDX-FileCopyrightText:") || !strings.Contains(string(raw), "\n"+tc.want) {
				t.Fatalf("header does not reproduce the command:\n%.400s", raw)
			}
		})
	}
}

func TestWorkflowRejectsSymlinkedPolicyLocations(t *testing.T) {
	for _, tc := range []string{"directory", "file"} {
		t.Run(tc, func(t *testing.T) {
			cfg, _ := maintenanceFixture(t)
			elsewhere := t.TempDir()
			writeJSON(t, filepath.Join(elsewhere, "linked"+PolicySuffix), cfg)
			if err := os.MkdirAll(filepath.Dir(PolicyDirectory), 0o700); err != nil {
				t.Fatal(err)
			}
			switch tc {
			case "directory":
				if err := os.Symlink(elsewhere, PolicyDirectory); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.MkdirAll(PolicyDirectory, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(elsewhere, "linked"+PolicySuffix), PolicyDirectory+"/linked"+PolicySuffix); err != nil {
					t.Fatal(err)
				}
			}
			out, err := Workflow(t.Context(), WorkflowOptions{}, "test (deadbeef)")
			assertCode(t, err, e.CodeInvalidCatalog)
			if len(out) != 0 || !strings.Contains(err.Error(), "cannot be symlinks") {
				t.Fatalf("a symlinked policy location emitted a workflow or an unrelated error: %v", err)
			}
		})
	}
}

func TestCommittedPoliciesLoad(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var policies []string
	for _, pattern := range []string{PolicyGlob, "docs/examples/catalog-maintenance/*.yaml"} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		policies = append(policies, matches...)
	}
	if len(policies) < 2 {
		t.Fatalf("expected the maintained policy and the documented example, found %v", policies)
	}
	for _, policy := range policies {
		t.Run(policy, func(t *testing.T) {
			if _, err := Load(t.Context(), policy); err != nil {
				t.Fatalf("a committed policy no longer loads, which would fail the nightly run: %v", err)
			}
		})
	}
}

// The App setup guide lists exactly the permissions the token step requests and the default secrets, so an owner
// who follows it can mint the token.
func TestAppSetupGuideListsTheRequestedPermissions(t *testing.T) {
	guide, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "catalog-maintenance.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, section, _ := strings.Cut(string(guide), "### Create the publishing GitHub App\n")
	section, _, _ = strings.Cut(section, "\n## ")
	permissions := AppPermissions()
	if len(permissions) == 0 || strings.Count(section, "| Read and write |") != len(permissions) {
		t.Fatalf("the guide must list exactly the %d requested permissions", len(permissions))
	}
	for _, permission := range permissions {
		if !strings.Contains(section, "| "+permission.Name+" | Read and write |") {
			t.Fatalf("the guide omits the %s permission", permission.Name)
		}
	}
	for _, secret := range []string{DefaultAppClientSecret, DefaultAppKeySecret} {
		if !strings.Contains(section, "gh secret set "+secret) {
			t.Fatalf("the guide omits the %s secret", secret)
		}
	}
}
