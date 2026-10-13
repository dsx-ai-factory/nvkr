// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const nightlySHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const nightlyWorkflow = "nvkr-run-oss.yml"

// The fixture records every endpoint and method. It admits no latest-run/list
// endpoint, and serves a child ID distinct from the coordinator's ID.
const fakeGitHub = `#!/usr/bin/env bash
set -euo pipefail
method=GET endpoint='' next_method=false
for argument in "$@"; do
  if [[ $next_method == true ]]; then method=$argument; next_method=false; fi
  if [[ $argument == --method ]]; then next_method=true; fi
  if [[ $argument == repos/* ]]; then endpoint=$argument; fi
done
printf '%s %s\n' "$method" "$endpoint" >>"$FAKE_GH_ROOT/calls"
case "$method $endpoint" in
  'GET repos/example/nvkr') cat "$FAKE_GH_ROOT/repository.json" ;;
  'GET repos/example/nvkr/actions/workflows/nvkr-run-oss.yml') cat "$FAKE_GH_ROOT/workflow.json" ;;
  'POST repos/example/nvkr/actions/workflows/nvkr-run-oss.yml/dispatches')
    if [[ $FAKE_DISPATCH_FAILURE == true ]]; then exit 17; fi
    cat "$FAKE_GH_ROOT/dispatch.json" ;;
  'GET repos/example/nvkr/actions/runs/4242') cat "$FAKE_GH_ROOT/producer.json" ;;
  'POST repos/example/nvkr/actions/runs/4242/cancel') printf '{"acknowledged":true}\n' ;;
  *) printf 'unexpected API call: %s %s\n' "$method" "$endpoint" >&2; exit 99 ;;
esac
`

type nightlyFixture struct {
	root, profiles, out, bin string
	environment              []string
	producer                 map[string]any
}

func fixtureJSON(t *testing.T, filename string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newNightlyFixture(t *testing.T) *nightlyFixture {
	t.Helper()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("nightly contract tests require %s: %v", tool, err)
		}
	}
	timeout, err := exec.LookPath("timeout")
	if err != nil {
		timeout, err = exec.LookPath("gtimeout")
		if err != nil {
			t.Fatal("nightly contract tests require GNU timeout or gtimeout")
		}
	}
	root := t.TempDir()
	f := &nightlyFixture{root: root, profiles: filepath.Join(root, "profiles.json"), out: filepath.Join(root, "output"), bin: filepath.Join(root, "bin")}
	if err := os.Mkdir(f.bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.bin, "gh"), []byte(fakeGitHub), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(timeout, filepath.Join(f.bin, "timeout")); err != nil {
		t.Fatal(err)
	}
	f.environment = []string{
		"PATH=" + f.bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_GH_ROOT=" + root, "FAKE_DISPATCH_FAILURE=false",
		"GITHUB_SERVER_URL=https://github.com", "GITHUB_REF=refs/heads/main",
		"GITHUB_REPOSITORY=example/nvkr", "GITHUB_REPOSITORY_ID=123",
		"GITHUB_SHA=" + nightlySHA, "GITHUB_RUN_ID=101", "GITHUB_RUN_ATTEMPT=3",
	}
	fixtureJSON(t, f.profiles, []map[string]string{{"name": "oss", "bundle": "oss", "build_workflow": nightlyWorkflow, "security_status": "admitted", "blocked_reason": ""}})
	fixtureJSON(t, filepath.Join(root, "repository.json"), map[string]any{"id": 123, "full_name": "example/nvkr", "default_branch": "main"})
	fixtureJSON(t, filepath.Join(root, "workflow.json"), map[string]string{"path": ".github/workflows/" + nightlyWorkflow, "state": "active"})
	fixtureJSON(t, filepath.Join(root, "dispatch.json"), map[string]int{"workflow_run_id": 4242})
	f.producer = map[string]any{"id": 4242, "head_sha": nightlySHA, "head_branch": "main", "event": "workflow_dispatch", "path": ".github/workflows/" + nightlyWorkflow,
		"repository": map[string]string{"full_name": "example/nvkr"}, "run_attempt": 1, "status": "completed", "conclusion": "success", "html_url": "https://github.com/example/nvkr/actions/runs/4242"}
	return f
}

func (f *nightlyFixture) setEnv(key, value string) {
	for i, entry := range f.environment {
		if strings.HasPrefix(entry, key+"=") {
			f.environment[i] = key + "=" + value
			return
		}
	}
	f.environment = append(f.environment, key+"="+value)
}

func (f *nightlyFixture) run(t *testing.T, profile string) (string, int) {
	t.Helper()
	fixtureJSON(t, filepath.Join(f.root, "producer.json"), f.producer)
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository script")
	}
	script := filepath.Join(filepath.Dir(filename), "..", "..", "scripts", "nightly-build.sh")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", script, f.profiles, profile, f.out)
	command.Env = f.environment
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("nightly fixture exceeded test bound: %v\n%s", ctx.Err(), output)
	}
	if err == nil {
		return string(output), 0
	}
	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		t.Fatalf("execute nightly fixture: %v", err)
	}
	return string(output), exited.ExitCode()
}

func (f *nightlyFixture) calls(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, "calls"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func noBuildResult(t *testing.T, f *nightlyFixture) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(f.out, "build.json")); !os.IsNotExist(err) {
		t.Fatalf("rejected producer emitted a build result: %v", err)
	}
}

func TestNightlyBuildRecordsOnlyExactSuccessfulProducer(t *testing.T) {
	f := newNightlyFixture(t)
	output, status := f.run(t, "oss")
	if status != 0 {
		t.Fatalf("nightly build failed: %d\n%s", status, output)
	}
	data, err := os.ReadFile(filepath.Join(f.out, "build.json"))
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		CoordinatorRun, CoordinatorAttempt, SourceCommit, ProducerURL, Trust string
		ProducerRun, ProducerAttempt                                         int
		Profile                                                              map[string]string
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.ProducerRun != 4242 || record.ProducerAttempt != 1 || record.CoordinatorRun != "101" || record.CoordinatorAttempt != "3" || record.SourceCommit != nightlySHA || record.ProducerURL != f.producer["html_url"] || record.Profile["build_workflow"] != nightlyWorkflow || record.Trust != "github-run-facts; publication-not-yet-verified" {
		t.Fatalf("producer record substituted identity or overclaimed authority: %+v", record)
	}
	if calls := f.calls(t); len(calls) != 4 || calls[2] != "POST repos/example/nvkr/actions/workflows/"+nightlyWorkflow+"/dispatches" || calls[3] != "GET repos/example/nvkr/actions/runs/4242" {
		t.Fatalf("did not use exact dispatch response ID: %v", calls)
	}
	var request map[string]string
	data, err = os.ReadFile(filepath.Join(f.out, "request.json"))
	if err != nil || json.Unmarshal(data, &request) != nil || len(request) != 1 || request["ref"] != "main" {
		t.Fatalf("unexpected dispatch request: %s (%v)", data, err)
	}
}

func TestNightlyBuildRejectsSubstitutedProducerAndCancelsOnlyOwnedID(t *testing.T) {
	for _, tc := range []struct {
		field string
		value any
	}{
		{"head_sha", strings.Repeat("b", 40)}, {"run_attempt", 2}, {"path", ".github/workflows/other.yml"}, {"id", 9999},
		{"head_branch", "other"}, {"event", "push"}, {"repository", map[string]string{"full_name": "other/nvkr"}},
	} {
		t.Run(tc.field, func(t *testing.T) {
			f := newNightlyFixture(t)
			f.producer[tc.field] = tc.value
			output, status := f.run(t, "oss")
			if status != 49 || !strings.Contains(output, "NVKR-E-UNPINNED-INPUT") {
				t.Fatalf("substituted producer accepted: %d\n%s", status, output)
			}
			noBuildResult(t, f)
			calls := f.calls(t)
			if len(calls) != 5 || calls[4] != "POST repos/example/nvkr/actions/runs/4242/cancel" {
				t.Fatalf("cancellation escaped owned dispatch ID: %v", calls)
			}
		})
	}
}

func TestNightlyBuildNeverGuessesOrRetriesAmbiguousDispatch(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "response without ID", true: "POST outcome unknown"}[failure], func(t *testing.T) {
			f := newNightlyFixture(t)
			fixtureJSON(t, filepath.Join(f.root, "dispatch.json"), map[string]any{})
			if failure {
				f.setEnv("FAKE_DISPATCH_FAILURE", "true")
			}
			output, status := f.run(t, "oss")
			if status == 0 || !failure && status != 30 {
				t.Fatalf("ambiguous dispatch: %d\n%s", status, output)
			}
			noBuildResult(t, f)
			if calls := f.calls(t); len(calls) != 3 || calls[2] != "POST repos/example/nvkr/actions/workflows/"+nightlyWorkflow+"/dispatches" {
				t.Fatalf("ambiguous dispatch retried, guessed, or canceled unowned run: %v", calls)
			}
		})
	}
}

func TestNightlyBuildFailedTerminalProducerNeedsNoCancellation(t *testing.T) {
	f := newNightlyFixture(t)
	f.producer["conclusion"] = "failure"
	output, status := f.run(t, "oss")
	if status != 21 || !strings.Contains(output, "NVKR-E-BUILD-FAILED") {
		t.Fatalf("failed terminal producer accepted: %d\n%s", status, output)
	}
	noBuildResult(t, f)
	if calls := f.calls(t); len(calls) != 4 {
		t.Fatalf("terminal producer unnecessarily canceled: %v", calls)
	}
}

func TestNightlyBuildRejectsAdmissionWithoutMutation(t *testing.T) {
	for _, scenario := range []string{"unknown profile", "wrong ref", "existing output", "symlink output", "profile workflow drift", "blocked profile", "remaining blocker", "missing admission reason"} {
		t.Run(scenario, func(t *testing.T) {
			f := newNightlyFixture(t)
			profile, wantStatus := "oss", 2
			switch scenario {
			case "unknown profile":
				profile = "unknown"
			case "wrong ref":
				f.setEnv("GITHUB_REF", "refs/heads/feature")
				wantStatus = 29
			case "existing output":
				if err := os.WriteFile(f.out, []byte("operator data"), 0o600); err != nil {
					t.Fatal(err)
				}
				wantStatus = 47
			case "symlink output":
				if err := os.Symlink(filepath.Join(f.root, "absent"), f.out); err != nil {
					t.Fatal(err)
				}
				wantStatus = 47
			case "blocked profile":
				fixtureJSON(t, f.profiles, []map[string]string{{"name": "oss", "bundle": "oss", "build_workflow": nightlyWorkflow, "security_status": "blocked"}})
				wantStatus = 29
			case "remaining blocker":
				fixtureJSON(t, f.profiles, []map[string]string{{"name": "oss", "bundle": "oss", "build_workflow": nightlyWorkflow, "security_status": "admitted", "blocked_reason": "tools not admitted"}})
				wantStatus = 29
			case "missing admission reason":
				fixtureJSON(t, f.profiles, []map[string]string{{"name": "oss", "bundle": "oss", "build_workflow": nightlyWorkflow, "security_status": "admitted"}})
				wantStatus = 29
			case "profile workflow drift":
				fixtureJSON(t, f.profiles, []map[string]string{{"name": "oss", "bundle": "oss", "build_workflow": "unmaintained.yml", "security_status": "admitted"}})
				wantStatus = 51
			}
			output, status := f.run(t, profile)
			if status != wantStatus || len(f.calls(t)) != 0 {
				t.Fatalf("rejected admission did work: %d calls=%v\n%s", status, f.calls(t), output)
			}
			if scenario == "existing output" {
				data, err := os.ReadFile(f.out)
				if err != nil || string(data) != "operator data" {
					t.Fatal("admission changed existing output")
				}
			} else if scenario == "symlink output" {
				if _, err := os.Readlink(f.out); err != nil {
					t.Fatal("admission replaced output symlink")
				}
			} else if _, err := os.Lstat(f.out); !os.IsNotExist(err) {
				t.Fatalf("rejected admission created output: %v", err)
			}
		})
	}
}
