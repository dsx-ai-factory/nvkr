// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationoutcome"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releasemanifest"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/urfave/cli/v2"
)

var qualifyOutSecrets = map[string]string{
	"NVKR_GITHUB_TOKEN":              "sentinel-github-token",
	"NVKR_REGISTRY_PASSWORD":         "sentinel-registry-password",
	"NVKR_CI_STORAGE_TOKEN":          "sentinel-storage-token",
	"NVKR_PIN_SOURCE_TOKEN_0":        "sentinel-source-token",
	"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "sentinel-oidc-request-token",
}

// qualifyOutOIDCToken is in no environment variable, like a token minted while signing.
const qualifyOutOIDCToken = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJyZXBvIn0.c2lnbmF0dXJl"

// qualifyOutLeaks is diagnostic text carrying every sentinel secret.
func qualifyOutLeaks() string {
	values := []string{qualifyOutOIDCToken}
	for _, value := range qualifyOutSecrets {
		values = append(values, value)
	}
	slices.Sort(values)
	return strings.Join(values, " ")
}

// qualifyOutEnvironment seeds the qualify job's GitHub identity, event and
// sentinel secrets.
func qualifyOutEnvironment(t *testing.T) {
	t.Helper()
	repository := map[string]any{"full_name": "example/nvkr", "id": json.Number("7")}
	raw, err := json.Marshal(map[string]any{"action": "completed", "workflow_run": map[string]any{"id": json.Number("101"), "run_attempt": json.Number("1"),
		"head_sha": strings.Repeat("a", 40), "head_branch": "main", "path": ".github/workflows/nvkr-run.yml", "event": "push",
		"status": "completed", "conclusion": "success", "repository": repository, "head_repository": repository}})
	if err != nil {
		t.Fatal(err)
	}
	event := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(event, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"GITHUB_REPOSITORY": "example/nvkr", "GITHUB_WORKFLOW_REF": "example/nvkr/.github/workflows/nvkr-qualify.yml@refs/heads/main",
		"GITHUB_RUN_ID": "202", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_SHA": strings.Repeat("b", 40), "GITHUB_EVENT_PATH": event,
	} {
		t.Setenv(name, value)
	}
	for name, value := range qualifyOutSecrets {
		t.Setenv(name, value)
	}
}

func qualifyOutPublication(successful bool) *qualification.Publication {
	subject := "sha256:" + strings.Repeat("e", 64)
	check := lifecycle.ArtifactCheckResult{Name: "build-signature-and-provenance", Status: "pass"}
	verdict := qualification.Verdict{Output: "tool", Subject: subject, Check: "delivered-tool-version", Status: "pass"}
	publication := &qualification.Publication{Successful: successful, ArtifactChecks: &lifecycle.ArtifactCheckDescription{
		Origin: qualificationorigin.Description{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-run.yml", RunID: "101", Attempt: "1", Commit: strings.Repeat("a", 40)},
	}}
	if successful {
		publication.EvidenceDigest = "sha256:" + strings.Repeat("c", 64)
	} else {
		check = lifecycle.ArtifactCheckResult{Name: "build-signature-and-provenance", Status: "fail", Code: string(e.CodeSignatureInvalid),
			Message: "pull from gs://private-bucket/x refused with " + qualifyOutLeaks()}
		verdict.Status, verdict.Reason = "fail", "smoke used "+qualifyOutLeaks()
	}
	publication.ArtifactChecks.Outputs = []lifecycle.ArtifactCheckOutput{{Name: "tool", Subject: subject, Checks: []lifecycle.ArtifactCheckResult{check}}}
	publication.Verdicts = []qualification.Verdict{verdict}
	return publication
}

func qualifyOutPassed() *releasemanifest.RunResult {
	return &releasemanifest.RunResult{Qualification: qualifyOutPublication(true), ManifestDigest: "sha256:" + strings.Repeat("d", 64), Successful: true,
		ManifestSubject: sign.Subject{Ref: "ghcr.io/example/nvkr/release@sha256:" + strings.Repeat("d", 64), Digest: "sha256:" + strings.Repeat("d", 64)}}
}

type qualifyOutRun struct {
	stdout  []byte
	stderr  string
	err     error
	outcome *qualificationoutcome.Outcome
	raw     []byte
	called  bool
}

// runQualifyOut runs qualify with a clock that advances a minute per reading.
func runQualifyOut(t *testing.T, ctx context.Context, issue qualificationManifestPublisher, start time.Time, args ...string) qualifyOutRun {
	t.Helper()
	calls := 0
	return runQualifyOutWithClock(t, ctx, issue, func() time.Time {
		calls++
		return start.Add(time.Duration(calls) * time.Minute)
	}, args...)
}

func runQualifyOutWithClock(t *testing.T, ctx context.Context, issue qualificationManifestPublisher, clock func() time.Time, args ...string) qualifyOutRun {
	t.Helper()
	var stdout, stderr bytes.Buffer
	run := qualifyOutRun{}
	app := &cli.App{Writer: &stdout, ErrWriter: &stderr, Commands: []*cli.Command{cmdRunQualifyWithClock(
		func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*qualification.Publication, error) {
			t.Fatal("manifest request used the qualification-only publisher")
			return nil, nil
		},
		func(ctx context.Context, path string, options lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error) {
			run.called = true
			return issue(ctx, path, options)
		}, clock)}}
	run.err = app.RunContext(ctx, append([]string{"nvkr", "qualify", "--policy", "trusted.yaml"}, args...))
	run.stdout, run.stderr = stdout.Bytes(), stderr.String()
	for i, arg := range args {
		if arg != "--out" || i+1 == len(args) {
			continue
		}
		raw, err := os.ReadFile(args[i+1])
		if err != nil {
			return run
		}
		outcome, err := qualificationoutcome.Decode(raw)
		if err != nil {
			t.Fatalf("written outcome does not decode: %v", err)
		}
		run.outcome, run.raw = &outcome, raw
	}
	return run
}

func TestQualificationCLIQualifyOut(t *testing.T) {
	gate := e.New(e.CodeGateUnmet, "qualification refused for %s", qualifyOutLeaks())
	for _, tc := range []struct {
		name   string
		result func() *releasemanifest.RunResult
		err    error
		status qualificationoutcome.Status
	}{
		{"passed", qualifyOutPassed, nil, qualificationoutcome.StatusPassed},
		{"failed", func() *releasemanifest.RunResult {
			return &releasemanifest.RunResult{Qualification: qualifyOutPublication(false)}
		}, gate, qualificationoutcome.StatusFailed},
		{"errored", func() *releasemanifest.RunResult { return nil }, gate, qualificationoutcome.StatusErrored},
	} {
		t.Run(tc.name, func(t *testing.T) {
			qualifyOutEnvironment(t)
			issue := func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error) {
				return tc.result(), tc.err
			}
			start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
			without := runQualifyOut(t, t.Context(), issue, start, "--issue-manifest")
			path := filepath.Join(t.TempDir(), qualificationoutcome.Directory, qualificationoutcome.FileName("1"))
			with := runQualifyOut(t, t.Context(), issue, start, "--issue-manifest", "--out", path)
			if !bytes.Equal(with.stdout, without.stdout) || with.err != without.err || e.ExitCode(with.err) != e.ExitCode(without.err) || with.stderr != "" {
				t.Fatalf("--out changed stdout, stderr or the exit: %v vs %v; stderr %q", with.err, without.err, with.stderr)
			}
			if tc.err != nil && !errors.Is(with.err, tc.err) {
				t.Fatalf("lost the qualification error: %v", with.err)
			}
			if with.outcome == nil || with.outcome.Status != tc.status || with.outcome.Qualifier.RunID != "202" || with.outcome.StartedAt != "2026-11-02T10:01:00Z" || with.outcome.FinishedAt != "2026-11-02T10:02:00Z" {
				t.Fatalf("wrote %#v", with.outcome)
			}
			for name, secret := range qualifyOutSecrets {
				if bytes.Contains(with.raw, []byte(secret)) {
					t.Fatalf("the outcome exposes %s", name)
				}
			}
			if bytes.Contains(with.raw, []byte("private-bucket")) || bytes.Contains(with.raw, []byte(qualifyOutOIDCToken)) {
				t.Fatal("the outcome exposes a bucket name or an OIDC token")
			}
		})
	}

	t.Run("clock stays outside the result", func(t *testing.T) {
		qualifyOutEnvironment(t)
		issue := func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error) {
			return qualifyOutPassed(), nil
		}
		dir := t.TempDir()
		first := runQualifyOut(t, t.Context(), issue, time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), "--issue-manifest", "--out", filepath.Join(dir, "first.json"))
		second := runQualifyOut(t, t.Context(), issue, time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC), "--issue-manifest", "--out", filepath.Join(dir, "second.json"))
		if first.outcome == nil || second.outcome == nil || !bytes.Equal(first.outcome.Result, second.outcome.Result) ||
			first.outcome.EvidenceDigest != second.outcome.EvidenceDigest || first.outcome.StartedAt == second.outcome.StartedAt {
			t.Fatal("the attempt clock reached the result or was not recorded")
		}
	})

	// The record grants no authority: when it cannot be written after the work,
	// the qualification's exit stands and stderr says what was lost.
	t.Run("canceled after qualification writes nothing and keeps the verdict", func(t *testing.T) {
		qualifyOutEnvironment(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		issue := func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error) {
			cancel()
			return qualifyOutPassed(), nil
		}
		path := filepath.Join(t.TempDir(), "outcome-1.json")
		run := runQualifyOut(t, ctx, issue, time.Now(), "--issue-manifest", "--out", path)
		if run.err != nil || run.outcome != nil || !run.called {
			t.Fatalf("got err=%v outcome=%v", run.err, run.outcome)
		}
		if !strings.Contains(run.stderr, "recording the qualification outcome; the qualification result stands") || !strings.Contains(run.stderr, string(e.CodeTimeout)) {
			t.Fatalf("stderr does not explain the missing outcome: %q", run.stderr)
		}
	})

	// A reused --out path keeps an earlier attempt's complete record when a later
	// attempt writes none; the record's qualifier attempt tells them apart.
	t.Run("reused path keeps the earlier record when a later attempt writes none", func(t *testing.T) {
		qualifyOutEnvironment(t)
		issue := func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error) {
			return qualifyOutPassed(), nil
		}
		path := filepath.Join(t.TempDir(), "outcome.json")
		first := runQualifyOut(t, t.Context(), issue, time.Now(), "--issue-manifest", "--out", path)
		if first.err != nil || first.outcome == nil || first.outcome.Qualifier.Attempt != "1" {
			t.Fatalf("first attempt: err=%v outcome=%v", first.err, first.outcome)
		}
		t.Setenv("GITHUB_RUN_ATTEMPT", "2")
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		canceled := func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error) {
			cancel()
			return qualifyOutPassed(), nil
		}
		second := runQualifyOut(t, ctx, canceled, time.Now(), "--issue-manifest", "--out", path)
		if second.err != nil || !strings.Contains(second.stderr, string(e.CodeTimeout)) || !bytes.Equal(second.raw, first.raw) {
			t.Fatalf("second attempt replaced or lost the first record: err=%v stderr=%q", second.err, second.stderr)
		}
	})

	// The destination can change after admission, for example when another
	// process removes it: the exit and stdout stand, stderr says what was lost,
	// and nothing is written.
	for _, tc := range []struct {
		name   string
		result func() *releasemanifest.RunResult
		err    error
	}{
		{"passed", qualifyOutPassed, nil},
		{"failed", func() *releasemanifest.RunResult {
			return &releasemanifest.RunResult{Qualification: qualifyOutPublication(false)}
		}, gate},
	} {
		t.Run("destination replaced after admission keeps the "+tc.name+" verdict", func(t *testing.T) {
			qualifyOutEnvironment(t)
			start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
			plain := func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error) {
				return tc.result(), tc.err
			}
			without := runQualifyOut(t, t.Context(), plain, start, "--issue-manifest")
			dir := filepath.Join(t.TempDir(), qualificationoutcome.Directory)
			replaced := func(ctx context.Context, policy string, options lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error) {
				// Admission created the directory; a file now takes its place.
				if err := os.RemoveAll(dir); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(dir, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return plain(ctx, policy, options)
			}
			run := runQualifyOut(t, t.Context(), replaced, start, "--issue-manifest", "--out", filepath.Join(dir, qualificationoutcome.FileName("1")))
			if !run.called || run.outcome != nil || !bytes.Equal(run.stdout, without.stdout) || run.err != without.err || e.ExitCode(run.err) != e.ExitCode(without.err) {
				t.Fatalf("got called=%v outcome=%v err=%v, want err=%v", run.called, run.outcome, run.err, without.err)
			}
			if !strings.Contains(run.stderr, "recording the qualification outcome; the qualification result stands") || !strings.Contains(run.stderr, string(e.CodeInternal)) {
				t.Fatalf("stderr does not explain the missing outcome: %q", run.stderr)
			}
		})
	}

	// A clock that steps back cannot put the finish before the start.
	t.Run("a clock stepping back records a zero-length attempt", func(t *testing.T) {
		qualifyOutEnvironment(t)
		issue := func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error) {
			return qualifyOutPassed(), nil
		}
		start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
		readings := []time.Time{start, start.Add(-time.Hour)}
		clock := func() time.Time {
			reading := readings[0]
			if len(readings) > 1 {
				readings = readings[1:]
			}
			return reading
		}
		dir := t.TempDir()
		back := runQualifyOutWithClock(t, t.Context(), issue, clock, "--issue-manifest", "--out", filepath.Join(dir, "back.json"))
		forward := runQualifyOut(t, t.Context(), issue, start, "--issue-manifest", "--out", filepath.Join(dir, "forward.json"))
		if back.err != nil || back.outcome == nil || back.outcome.StartedAt != "2026-11-02T10:00:00Z" || back.outcome.FinishedAt != back.outcome.StartedAt {
			t.Fatalf("got err=%v outcome=%#v", back.err, back.outcome)
		}
		if forward.outcome == nil || !bytes.Equal(back.outcome.Result, forward.outcome.Result) ||
			back.outcome.EvidenceDigest != forward.outcome.EvidenceDigest || back.outcome.ManifestDigest != forward.outcome.ManifestDigest {
			t.Fatal("the clock reached the result or a digest")
		}
	})

	// The record is owner-only even in a caller's world-readable directory.
	t.Run("record is owner-only", func(t *testing.T) {
		qualifyOutEnvironment(t)
		issue := func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error) {
			return qualifyOutPassed(), nil
		}
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "outcome-1.json")
		run := runQualifyOut(t, t.Context(), issue, time.Now(), "--issue-manifest", "--out", path)
		info, err := os.Stat(path)
		if run.err != nil || err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("err=%v stat=%v info=%v", run.err, err, info)
		}
	})

	malformed := filepath.Join(t.TempDir(), "malformed.json")
	if err := os.WriteFile(malformed, []byte(`{"workflow_run":`), 0o600); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// Well-formed events that cannot describe an errored qualify attempt.
	event := func(change func(run map[string]any)) string {
		repository := map[string]any{"full_name": "example/nvkr", "id": json.Number("7")}
		run := map[string]any{"id": json.Number("101"), "run_attempt": json.Number("1"), "head_sha": strings.Repeat("a", 40), "head_branch": "main",
			"path": ".github/workflows/nvkr-run.yml", "event": "push", "status": "completed", "conclusion": "success", "repository": repository, "head_repository": repository}
		change(run)
		raw, err := json.Marshal(map[string]any{"action": "completed", "workflow_run": run})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "event.json")
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	forkHead := event(func(run map[string]any) {
		run["head_repository"] = map[string]any{"full_name": "fork/nvkr", "id": json.Number("8")}
	})
	failedProducer := event(func(run map[string]any) { run["conclusion"] = "failure" })
	directory := t.TempDir()
	// Each record input is admitted before qualification does any work.
	for _, tc := range []struct {
		name, env, value string
		args             []string
		code             e.Code
	}{
		{"out without issue-manifest", "", "", []string{"--out", "outcome.json"}, e.CodeUsage},
		{"empty out", "", "", []string{"--issue-manifest", "--out", " "}, e.CodeUsage},
		{"no qualifier identity", "GITHUB_RUN_ID", "", nil, e.CodeProvenanceMissing},
		{"no event", "GITHUB_EVENT_PATH", "", nil, e.CodeProvenanceMissing},
		{"malformed event", "GITHUB_EVENT_PATH", malformed, nil, e.CodeVerifyFailed},
		{"event from a fork head", "GITHUB_EVENT_PATH", forkHead, nil, e.CodeVerifyFailed},
		{"event for a failed producer", "GITHUB_EVENT_PATH", failedProducer, nil, e.CodeVerifyFailed},
		{"secret too short to redact", "NVKR_REGISTRY_PASSWORD", "abc", nil, e.CodeUsage},
		{"multi-line secret with a short line", "NVKR_REGISTRY_PASSWORD", "sentinel-line\nab", nil, e.CodeUsage},
		{"out names a directory", "", "", []string{"--issue-manifest", "--out", directory}, e.CodeUsage},
		{"unwritable directory", "", "", []string{"--issue-manifest", "--out", filepath.Join(blocker, "outcome-1.json")}, e.CodeInternal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			qualifyOutEnvironment(t)
			if tc.env != "" {
				t.Setenv(tc.env, tc.value)
			}
			args := tc.args
			if args == nil {
				args = []string{"--issue-manifest", "--out", filepath.Join(t.TempDir(), "outcome-1.json")}
			}
			issue := func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error) {
				return qualifyOutPassed(), nil
			}
			run := runQualifyOut(t, t.Context(), issue, time.Now(), args...)
			var coded *e.Error
			if run.called || !errors.As(run.err, &coded) || coded.Code != tc.code {
				t.Fatalf("invalid --out reached qualification or lost its code: called=%v err=%v", run.called, run.err)
			}
		})
	}
}
