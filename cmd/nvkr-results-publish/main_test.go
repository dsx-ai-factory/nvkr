// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationoutcome"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/resultspublish"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/resultspublish/storetest"
)

const sentinelBucket = "sentinel-results-bucket"

var now = time.Date(2026, 11, 2, 11, 0, 0, 0, time.UTC)

// jobEnvironment is the publish job's GitHub Actions environment.
func jobEnvironment(extra map[string]string) func(string) (string, bool) {
	values := map[string]string{"GITHUB_REPOSITORY": "example/nvkr", "GITHUB_WORKFLOW_REF": "example/nvkr/.github/workflows/nvkr-qualify.yml@refs/heads/main",
		"GITHUB_RUN_ID": "202", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_SHA": strings.Repeat("b", 40), bucketEnv: sentinelBucket}
	for name, value := range extra {
		values[name] = value
	}
	return func(name string) (string, bool) { value, ok := values[name]; return value, ok }
}

// producerEvent writes the workflow_run event of producer Run 101.
func producerEvent(t *testing.T, conclusion, trigger string) string {
	t.Helper()
	repository := map[string]any{"full_name": "example/nvkr", "id": json.Number("7")}
	raw, err := json.Marshal(map[string]any{"action": "completed", "workflow_run": map[string]any{"id": json.Number("101"), "run_attempt": json.Number("1"),
		"head_sha": strings.Repeat("a", 40), "head_branch": "main", "path": ".github/workflows/nvkr-run.yml", "event": trigger,
		"status": "completed", "conclusion": conclusion, "run_started_at": "2026-11-02T09:00:00Z", "updated_at": "2026-11-02T09:40:00Z",
		"repository": repository, "head_repository": repository}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type cliRun struct {
	stdout, stderr string
	status         int
}

func runCLI(t *testing.T, lookup func(string) (string, bool), open storeOpener, args ...string) cliRun {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if open == nil {
		open = func(context.Context, string) (resultspublish.ObjectStore, func() error, error) {
			t.Fatal("the bucket was opened")
			return nil, nil, nil
		}
	}
	status := run(t.Context(), args, &stdout, &stderr, lookup, func() time.Time { return now }, open)
	return cliRun{stdout.String(), stderr.String(), status}
}

// qualifyReport is the qualify job's report as the publish job passes it: the job's
// result, and from its report step the attempt and whether it wrote an outcome.
type qualifyReport struct{ result, attempt, written string }

// skipped is the report of a qualify job that did not run.
var skipped = qualifyReport{result: "skipped"}

// admission is the flags select and publish share.
func admission(outcomes, event string, qualify qualifyReport) []string {
	return []string{"--outcomes", outcomes, "--event", event, "--source-workflow", ".github/workflows/nvkr-run.yml", "--source-branch", "main",
		"--qualify-workflow", ".github/workflows/nvkr-qualify.yml", "--qualify-result", qualify.result, "--qualify-attempt", qualify.attempt,
		"--outcome-written", qualify.written}
}

func selectArgs(outcomes, event string, qualify qualifyReport, write string) []string {
	return append(append([]string{"select"}, admission(outcomes, event, qualify)...), "--write", write)
}

func publishArgs(outcomes, event string, qualify qualifyReport, extra ...string) []string {
	return append(append([]string{"publish"}, admission(outcomes, event, qualify)...), append([]string{"--group", "nvke", "--definition", "nvke-m2"}, extra...)...)
}

// builtOutcome runs select for a failed producer and returns the outcomes
// directory holding the built record.
func builtOutcome(t *testing.T) string {
	t.Helper()
	outcomes := t.TempDir()
	got := runCLI(t, jobEnvironment(nil), nil, selectArgs(outcomes, producerEvent(t, "failure", "push"), skipped, filepath.Join(outcomes, "built", "outcome-1-built.json"))...)
	if got.status != 0 || got.stdout != "built=true\n" {
		t.Fatalf("select: %+v", got)
	}
	return outcomes
}

// failedProducer publishes a failed producer's built record.
func failedProducer(t *testing.T, extra ...string) []string {
	return publishArgs(builtOutcome(t), producerEvent(t, "failure", "push"), skipped, extra...)
}

type failingStore struct{ err error }

func (s failingStore) Create(context.Context, string, []byte) error { return s.err }

func (s failingStore) Read(context.Context, string, int64) ([]byte, error) { return nil, s.err }

func TestResultsPublishCLI(t *testing.T) {
	t.Run("select builds an errored outcome for a failed producer", func(t *testing.T) {
		outcomes := t.TempDir()
		write := filepath.Join(t.TempDir(), "outcome-built.json")
		got := runCLI(t, jobEnvironment(nil), nil, selectArgs(outcomes, producerEvent(t, "failure", "push"), skipped, write)...)
		if got.status != 0 || got.stdout != "built=true\n" || !strings.Contains(got.stderr, "1 outcome(s) to publish") {
			t.Fatalf("select: %+v", got)
		}
		raw, err := os.ReadFile(write)
		if err != nil {
			t.Fatal(err)
		}
		built, err := qualificationoutcome.Decode(raw)
		if err != nil || built.Stage != qualificationoutcome.StageProducer || built.FinishedAt != "2026-11-02T09:40:00Z" {
			t.Fatalf("built %+v, %v", built, err)
		}
		if info, err := os.Stat(write); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("built outcome mode: %v, %v", info, err)
		}
	})
	t.Run("select builds the record of a failed qualify attempt that wrote nothing", func(t *testing.T) {
		write := filepath.Join(t.TempDir(), "outcome-built.json")
		got := runCLI(t, jobEnvironment(nil), nil, selectArgs(t.TempDir(), producerEvent(t, "success", "push"), qualifyReport{"failure", "1", "false"}, write)...)
		if got.status != 0 || got.stdout != "built=true\n" {
			t.Fatalf("select: %+v", got)
		}
		raw, err := os.ReadFile(write)
		if err != nil {
			t.Fatal(err)
		}
		if built, err := qualificationoutcome.Decode(raw); err != nil || built.Stage != qualificationoutcome.StageQualify || built.Error == nil {
			t.Fatalf("built %+v, %v", built, err)
		}
	})
	t.Run("select builds nothing when the Run has an outcome", func(t *testing.T) {
		outcomes := builtOutcome(t)
		got := runCLI(t, jobEnvironment(nil), nil, selectArgs(outcomes, producerEvent(t, "failure", "push"), skipped, filepath.Join(t.TempDir(), "x.json"))...)
		if got.status != 0 || got.stdout != "built=false\n" {
			t.Fatalf("select: %+v", got)
		}
	})
	t.Run("publish --local-dir end to end, twice, then refuses a changed object", func(t *testing.T) {
		local := t.TempDir()
		args := failedProducer(t, "--local-dir", local)
		for range 2 {
			if got := runCLI(t, jobEnvironment(nil), nil, args...); got.status != 0 || got.stdout != "" {
				t.Fatalf("publish: %+v", got)
			}
		}
		finished, err := filepath.Glob(filepath.Join(local, "groups", "nvkr", "nvke", "nvke-m2", "*-101-1-202-1", "finished.json"))
		if err != nil || len(finished) != 1 {
			t.Fatalf("finished.json: %v, %v", finished, err)
		}
		if _, err := os.Stat(filepath.Join(local, "results", "nvke", "nvke-m2", "101-1", "202-1", "outcome.json")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(finished[0], []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := runCLI(t, jobEnvironment(nil), nil, args...); got.status != e.ExitCode(e.New(e.CodeImmutableViolation, "")) {
			t.Fatalf("publish over a changed object: %+v", got)
		}
	})
	t.Run("publish has nothing to publish when the qualify job did not run", func(t *testing.T) {
		got := runCLI(t, jobEnvironment(nil), nil, publishArgs(t.TempDir(), producerEvent(t, "success", "push"), skipped)...)
		if got.status != 0 || got.stdout != "" || !strings.Contains(got.stderr, "nothing to publish") {
			t.Fatalf("publish: %+v", got)
		}
	})
	publishFailed := func(t *testing.T) []string { return failedProducer(t) }
	opened := func(store resultspublish.ObjectStore, err error) storeOpener {
		return func(_ context.Context, bucket string) (resultspublish.ObjectStore, func() error, error) {
			if bucket != sentinelBucket {
				return nil, nil, e.New(e.CodeInternal, "the store was opened with the wrong bucket")
			}
			return store, func() error { return nil }, err
		}
	}
	for _, tc := range []struct {
		name   string
		args   func(t *testing.T) []string
		lookup func(string) (string, bool)
		open   storeOpener
		code   e.Code
	}{
		{name: "no subcommand", args: func(*testing.T) []string { return nil }, code: e.CodeUsage},
		{name: "an unknown subcommand", args: func(*testing.T) []string { return []string{"upload"} }, code: e.CodeUsage},
		{name: "select without --write", code: e.CodeUsage, args: func(t *testing.T) []string {
			return selectArgs(t.TempDir(), producerEvent(t, "failure", "push"), skipped, "")
		}},
		{name: "select with a positional argument", code: e.CodeUsage, args: func(t *testing.T) []string {
			return append(selectArgs(t.TempDir(), producerEvent(t, "failure", "push"), skipped, filepath.Join(t.TempDir(), "x.json")), "extra")
		}},
		{name: "select with an unknown outcome-written", code: e.CodeUsage, args: func(t *testing.T) []string {
			return selectArgs(t.TempDir(), producerEvent(t, "success", "push"), qualifyReport{"failure", "1", "yes"}, filepath.Join(t.TempDir(), "x.json"))
		}},
		{name: "select with an attempt but no written report", code: e.CodeUsage, args: func(t *testing.T) []string {
			return selectArgs(t.TempDir(), producerEvent(t, "success", "push"), qualifyReport{"failure", "1", ""}, filepath.Join(t.TempDir(), "x.json"))
		}},
		{name: "select with a malformed qualify attempt", code: e.CodeUsage, args: func(t *testing.T) []string {
			return selectArgs(t.TempDir(), producerEvent(t, "success", "push"), qualifyReport{"failure", "first", "false"}, filepath.Join(t.TempDir(), "x.json"))
		}},
		{name: "select without a qualify result", code: e.CodeUsage, args: func(t *testing.T) []string {
			return selectArgs(t.TempDir(), producerEvent(t, "failure", "push"), qualifyReport{}, filepath.Join(t.TempDir(), "x.json"))
		}},
		{name: "select with an unknown qualify result", code: e.CodeUsage, args: func(t *testing.T) []string {
			return selectArgs(t.TempDir(), producerEvent(t, "failure", "push"), qualifyReport{result: "neutral"}, filepath.Join(t.TempDir(), "x.json"))
		}},
		{name: "select when a passed qualify job's outcome is missing", code: e.CodeVerifyFailed, args: func(t *testing.T) []string {
			return selectArgs(t.TempDir(), producerEvent(t, "success", "push"), qualifyReport{"success", "1", "false"}, filepath.Join(t.TempDir(), "x.json"))
		}},
		{name: "select in another workflow", code: e.CodeTrustPolicyMissing,
			lookup: jobEnvironment(map[string]string{"GITHUB_WORKFLOW_REF": "example/nvkr/.github/workflows/other.yml@refs/heads/main"}),
			args: func(t *testing.T) []string {
				return selectArgs(t.TempDir(), producerEvent(t, "failure", "push"), skipped, filepath.Join(t.TempDir(), "x.json"))
			}},
		{name: "select for a pull request", code: e.CodeTrustPolicyMissing, args: func(t *testing.T) []string {
			return selectArgs(t.TempDir(), producerEvent(t, "failure", "pull_request"), skipped, filepath.Join(t.TempDir(), "x.json"))
		}},
		{name: "select when a reported outcome is missing", code: e.CodeVerifyFailed, args: func(t *testing.T) []string {
			return selectArgs(t.TempDir(), producerEvent(t, "success", "push"), qualifyReport{"failure", "1", "true"}, filepath.Join(t.TempDir(), "x.json"))
		}},
		{name: "publish without a group", code: e.CodeUsage, args: func(t *testing.T) []string {
			return append(append([]string{"publish"}, admission(t.TempDir(), producerEvent(t, "failure", "push"), skipped)...), "--definition", "nvke-m2")
		}},
		{name: "publish with a malformed group, even with nothing to publish", code: e.CodeUsage, args: func(t *testing.T) []string {
			return append(append([]string{"publish"}, admission(t.TempDir(), producerEvent(t, "success", "push"), skipped)...),
				"--group", "NVKE", "--definition", "nvke-m2")
		}},
		{name: "publish before select's record exists", code: e.CodeVerifyFailed, args: func(t *testing.T) []string {
			return publishArgs(t.TempDir(), producerEvent(t, "failure", "push"), skipped, "--local-dir", t.TempDir())
		}},
		{name: "publish of another Run's outcomes", code: e.CodeVerifyFailed,
			lookup: jobEnvironment(map[string]string{"GITHUB_RUN_ID": "203"}), args: publishFailed},
		{name: "publish without a bucket", code: e.CodeUsage, lookup: jobEnvironment(map[string]string{bucketEnv: ""}), args: publishFailed},
		{name: "publish with a refused identity", code: e.CodeTrustPolicyMissing, args: publishFailed,
			open: opened(failingStore{e.New(e.CodeTrustPolicyMissing, "the publisher identity was refused")}, nil)},
		{name: "publish through an unavailable bucket", code: e.CodeUnavailable, args: publishFailed,
			open: opened(failingStore{e.New(e.CodeUnavailable, "writing failed")}, nil)},
		{name: "publish past its deadline", code: e.CodeTimeout, args: publishFailed,
			open: opened(failingStore{e.New(e.CodeTimeout, "writing was interrupted")}, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := tc.lookup
			if lookup == nil {
				lookup = jobEnvironment(nil)
			}
			got := runCLI(t, lookup, tc.open, tc.args(t)...)
			if got.status != e.ExitCode(e.New(tc.code, "")) || got.stdout != "" {
				t.Fatalf("got %+v, want %s", got, tc.code)
			}
			if !strings.HasSuffix(got.stderr, verdictNote+"\n") || strings.Contains(got.stderr, sentinelBucket) {
				t.Fatalf("stderr %q", got.stderr)
			}
		})
	}
}

func TestResultsPublishLocalStoreConformance(t *testing.T) {
	storetest.Conformance(t, func(t *testing.T) resultspublish.ObjectStore { return localStore{root: t.TempDir()} })
}
