// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cicontract

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"golang.org/x/sys/unix"
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
block_request() {
  trap '' INT TERM
  "$FAKE_REAL_SLEEP" 120 &
  printf '%s\n' "$!" >"$FAKE_GH_ROOT/api-descendant-pid"
  printf '%s\n' "$$" >"$FAKE_GH_ROOT/api-pid"
  printf '%s\n' "$PPID" >"$FAKE_GH_ROOT/timeout-pid"
  printf '%s\n' ready >"$FAKE_GH_ROOT/api-ready"
  wait "$!"
}
case "$method $endpoint" in
  'GET repos/example/nvkr') cat "$FAKE_GH_ROOT/repository.json" ;;
  'GET repos/example/nvkr/actions/workflows/nvkr-run-oss.yml') cat "$FAKE_GH_ROOT/workflow.json" ;;
  'POST repos/example/nvkr/actions/workflows/nvkr-run-oss.yml/dispatches')
    if [[ ${FAKE_GH_BLOCK:-} == dispatch ]]; then block_request; fi
    if [[ $FAKE_DISPATCH_FAILURE == true ]]; then exit 17; fi
    cat "$FAKE_GH_ROOT/dispatch.json" ;;
  'GET repos/example/nvkr/actions/runs/4242')
    if [[ ${FAKE_GH_BLOCK:-} == producer ]]; then block_request; fi
    cat "$FAKE_GH_ROOT/producer.json" ;;
  'POST repos/example/nvkr/actions/runs/4242/cancel')
    if [[ ${FAKE_GH_BLOCK:-} == cancel ]]; then block_request; fi
    printf '{"acknowledged":true}\n' ;;
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
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	f.setEnv("FAKE_REAL_SLEEP", sleep)
	fixtureJSON(t, f.profiles, []map[string]string{{"name": "oss", "bundle": "oss", "build_workflow": nightlyWorkflow}})
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
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := f.command(t, ctx, profile)
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

func (f *nightlyFixture) command(t *testing.T, ctx context.Context, profile string) *exec.Cmd {
	t.Helper()
	fixtureJSON(t, filepath.Join(f.root, "producer.json"), f.producer)
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate repository script")
	}
	script := filepath.Join(filepath.Dir(filename), "..", "..", "scripts", "nightly-build.sh")
	command := exec.CommandContext(ctx, "bash", script, f.profiles, profile, f.out)
	command.Env = f.environment
	return command
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
	for _, scenario := range []string{"unknown profile", "wrong ref", "existing output", "symlink output", "profile workflow drift"} {
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
			case "profile workflow drift":
				fixtureJSON(t, f.profiles, []map[string]string{{"name": "oss", "bundle": "oss", "build_workflow": "unmaintained.yml"}})
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

func waitNightlyMarker(t *testing.T, ctx context.Context, root, name string) {
	t.Helper()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatalf("nightly fixture never reached %s: %v", name, ctx.Err())
		case <-tick.C:
		}
	}
}

// Keep the fixture's foreground pipe open until both its API process and the
// inherited-pipe descendant terminate. A prompt cmd.Wait therefore proves join,
// without depending on whether the platform has already reaped orphan zombies.
func TestNightlyBuildSignalsStopActiveWorkAndCancelOnlyOwnedRun(t *testing.T) {
	for _, phase := range []string{"producer", "poll", "dispatch", "cancel"} {
		for _, signal := range []unix.Signal{unix.SIGINT, unix.SIGTERM} {
			t.Run(phase+"/"+signal.String(), func(t *testing.T) {
				f := newNightlyFixture(t)
				f.setEnv("FAKE_GH_BLOCK", phase)
				marker := "api-ready"
				if phase == "poll" || phase == "cancel" {
					f.producer["status"] = "in_progress"
					marker = "sleep-ready"
					pollSleep := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$PPID" >"$FAKE_GH_ROOT/timeout-pid"
printf '%s\n' ready >"$FAKE_GH_ROOT/sleep-ready"
exec "$FAKE_REAL_SLEEP" "$@"
`
					if err := os.WriteFile(filepath.Join(f.bin, "sleep"), []byte(pollSleep), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				command := f.command(t, ctx, "oss")
				if err := ownedcmd.ConfigureCancellation(command, time.Second); err != nil {
					t.Fatal(err)
				}
				// This contract observes pipe closure by the owned descendants;
				// WaitDelay must not hide a leaked child by closing those pipes.
				command.WaitDelay = 0
				var output bytes.Buffer
				command.Stdout, command.Stderr = &output, &output
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- command.Wait() }()
				joined := false
				t.Cleanup(func() {
					if joined {
						return
					}
					// These are exclusively this fixture's child groups. Failure
					// cleanup must not leave the deliberately stubborn mock alive.
					if raw, err := os.ReadFile(filepath.Join(f.root, "timeout-pid")); err == nil {
						if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil && pid > 0 {
							_ = unix.Kill(-pid, unix.SIGKILL)
						}
					}
					_ = ownedcmd.KillDescendants(command)
					_ = command.Process.Kill()
					select {
					case <-done:
					case <-time.After(2 * time.Second):
						t.Error("nightly fixture failure cleanup did not join")
					}
				})
				waitNightlyMarker(t, ctx, f.root, marker)
				if err := command.Process.Signal(signal); err != nil {
					t.Fatal(err)
				}
				bound := 4 * time.Second
				if phase == "cancel" {
					bound = 8 * time.Second
					waitNightlyMarker(t, ctx, f.root, "api-ready")
					// The runner's second signal must not interrupt the bounded
					// owned-run cancel attempt after active work was stopped.
					if err := command.Process.Signal(unix.SIGTERM); err != nil {
						t.Fatal(err)
					}
				}
				select {
				case err := <-done:
					joined = true
					var exited *exec.ExitError
					if !errors.As(err, &exited) || exited.ExitCode() != 31 {
						t.Fatalf("signal did not preserve timeout status: %v\n%s", err, output.String())
					}
				case <-time.After(bound):
					t.Fatalf("signal did not promptly stop and join active %s work", phase)
				}
				noBuildResult(t, f)
				calls := f.calls(t)
				want := 5
				if phase == "dispatch" {
					want = 3
				}
				if len(calls) != want || phase != "dispatch" && calls[len(calls)-1] != "POST repos/example/nvkr/actions/runs/4242/cancel" {
					t.Fatalf("signal guessed/retried dispatch or canceled an unowned ID: %v", calls)
				}
				if phase == "cancel" && !strings.Contains(output.String(), "not acknowledged") {
					t.Fatalf("expired cancel falsely claimed acknowledgment: %s", output.String())
				}
			})
		}
	}
}
