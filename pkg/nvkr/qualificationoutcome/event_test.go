// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationoutcome

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func eventFixture(t *testing.T, change func(document, run map[string]any)) []byte {
	t.Helper()
	repository := map[string]any{"full_name": "example/nvkr", "id": json.Number("7")}
	run := map[string]any{"id": json.Number("101"), "run_attempt": json.Number("1"), "head_sha": strings.Repeat("a", 40), "head_branch": "main",
		"path": ".github/workflows/nvkr-run.yml", "event": "push", "status": "completed", "conclusion": "failure",
		"run_started_at": "2026-11-02T09:00:00Z", "updated_at": "2026-11-02T09:40:00Z", "repository": repository, "head_repository": repository}
	document := map[string]any{"action": "completed", "workflow_run": run}
	if change != nil {
		change(document, run)
	}
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeEvent(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "event.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func qualifierFixture() Run {
	return Run{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-qualify.yml", RunID: "202", Attempt: "1", Commit: strings.Repeat("b", 40)}
}

func TestQualificationOutcomeEvent(t *testing.T) {
	started, finished := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 2, 10, 5, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		path   func(*testing.T) string
		stage  Stage
		code   e.Code
		source Source
	}{
		{"failed producer", func(t *testing.T) string { return writeEvent(t, eventFixture(t, nil)) }, StageProducer, "",
			Source{Run: Run{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-run.yml", RunID: "101", Attempt: "1", Commit: strings.Repeat("a", 40)}, Event: "push", Conclusion: "failure"}},
		{"timed out dispatched producer", func(t *testing.T) string {
			return writeEvent(t, eventFixture(t, func(_, run map[string]any) { run["event"], run["conclusion"] = "workflow_dispatch", "timed_out" }))
		}, StageProducer, "",
			Source{Run: Run{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-run.yml", RunID: "101", Attempt: "1", Commit: strings.Repeat("a", 40)}, Event: "workflow_dispatch", Conclusion: "timed_out"}},
		{"qualifier without a result", func(t *testing.T) string {
			return writeEvent(t, eventFixture(t, func(_, run map[string]any) { run["conclusion"] = "success" }))
		}, StageQualify, "",
			Source{Run: Run{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-run.yml", RunID: "101", Attempt: "1", Commit: strings.Repeat("a", 40)}, Event: "push", Conclusion: "success"}},
		{"producer stage for a successful producer", func(t *testing.T) string {
			return writeEvent(t, eventFixture(t, func(_, run map[string]any) { run["conclusion"] = "success" }))
		}, StageProducer, e.CodeVerifyFailed, Source{}},
		{"canceled producer", func(t *testing.T) string {
			return writeEvent(t, eventFixture(t, func(_, run map[string]any) { run["conclusion"] = "cancelled" })) //nolint:misspell // GitHub conclusion spelling.
		}, StageProducer, e.CodeVerifyFailed, Source{}},
		{"pull request producer", func(t *testing.T) string {
			return writeEvent(t, eventFixture(t, func(_, run map[string]any) { run["event"] = "pull_request" }))
		}, StageProducer, e.CodeVerifyFailed, Source{}},
		{"fork head", func(t *testing.T) string {
			return writeEvent(t, eventFixture(t, func(_, run map[string]any) {
				run["head_repository"] = map[string]any{"full_name": "fork/nvkr", "id": json.Number("8")}
			}))
		}, StageProducer, e.CodeVerifyFailed, Source{}},
		{"foreign repository", func(t *testing.T) string {
			return writeEvent(t, eventFixture(t, func(_, run map[string]any) {
				other := map[string]any{"full_name": "other/nvkr", "id": json.Number("9")}
				run["repository"], run["head_repository"] = other, other
			}))
		}, StageProducer, e.CodeVerifyFailed, Source{}},
		{"non-decimal Run", func(t *testing.T) string {
			return writeEvent(t, eventFixture(t, func(_, run map[string]any) { run["id"] = "12a" }))
		}, StageProducer, e.CodeVerifyFailed, Source{}},
		{"not a workflow_run event", func(t *testing.T) string { return writeEvent(t, []byte(`{"ref":"refs/heads/main"}`)) }, StageProducer, e.CodeVerifyFailed, Source{}},
		{"malformed time", func(t *testing.T) string {
			return writeEvent(t, eventFixture(t, func(_, run map[string]any) { run["updated_at"] = "yesterday" }))
		}, StageProducer, e.CodeVerifyFailed, Source{}},
		{"invalid JSON", func(t *testing.T) string { return writeEvent(t, []byte(`{"workflow_run":`)) }, StageProducer, e.CodeVerifyFailed, Source{}},
		{"duplicate name", func(t *testing.T) string { return writeEvent(t, []byte(`{"action":"completed","action":"requested"}`)) }, StageProducer, e.CodeVerifyFailed, Source{}},
		{"oversize", func(t *testing.T) string {
			return writeEvent(t, []byte(`{"pad":"`+strings.Repeat("x", maxEventBytes)+`"}`))
		}, StageProducer, e.CodeVerifyFailed, Source{}},
		{"missing file", func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.json") }, StageProducer, e.CodeProvenanceMissing, Source{}},
		{"no event path", func(*testing.T) string { return " " }, StageProducer, e.CodeProvenanceMissing, Source{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event, err := ReadEvent(t.Context(), tc.path(t))
			var outcome Outcome
			if err == nil {
				outcome, err = FromEvent(event, qualifierFixture(), tc.stage, started, finished)
			}
			if tc.code != "" {
				requireCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Status != StatusErrored || outcome.Stage != tc.stage || outcome.SourceIdentity != SourceUnauthenticatedEvent || outcome.Source != tc.source {
				t.Fatalf("event outcome is %#v", outcome)
			}
			if outcome.StartedAt != "2026-11-02T10:00:00Z" || outcome.FinishedAt != "2026-11-02T10:05:00Z" || outcome.Qualifier != qualifierFixture() {
				t.Fatal("event outcome lost the qualifier attempt")
			}
			if !event.RunStartedAt.Equal(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)) || event.Action != "completed" || event.HeadBranch != "main" {
				t.Fatal("event description lost its Run fields")
			}
			raw, err := outcome.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Decode(raw); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A canceled read is retryable, not a verification failure.
func TestQualificationOutcomeEventCanceled(t *testing.T) {
	path := writeEvent(t, eventFixture(t, nil))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := ReadEvent(ctx, path)
	requireCode(t, err, e.CodeTimeout)
}

func TestQualificationOutcomeQualifierFromEnv(t *testing.T) {
	complete := map[string]string{
		"GITHUB_REPOSITORY":   "example/nvkr",
		"GITHUB_WORKFLOW_REF": "example/nvkr/.github/workflows/nvkr-qualify.yml@refs/heads/main",
		"GITHUB_RUN_ID":       "202",
		"GITHUB_RUN_ATTEMPT":  "1",
		"GITHUB_SHA":          strings.Repeat("b", 40),
	}
	with := func(name, value string) map[string]string {
		env := map[string]string{}
		for key, existing := range complete {
			env[key] = existing
		}
		if value == "" {
			delete(env, name)
		} else {
			env[name] = value
		}
		return env
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		code e.Code
	}{
		{"complete", complete, ""},
		{"no repository", with("GITHUB_REPOSITORY", ""), e.CodeProvenanceMissing},
		{"no workflow", with("GITHUB_WORKFLOW_REF", ""), e.CodeProvenanceMissing},
		{"no Run", with("GITHUB_RUN_ID", ""), e.CodeProvenanceMissing},
		{"no attempt", with("GITHUB_RUN_ATTEMPT", ""), e.CodeProvenanceMissing},
		{"no commit", with("GITHUB_SHA", ""), e.CodeProvenanceMissing},
		{"foreign workflow", with("GITHUB_WORKFLOW_REF", "other/nvkr/.github/workflows/nvkr-qualify.yml@refs/heads/main"), e.CodeVerifyFailed},
		{"workflow without ref", with("GITHUB_WORKFLOW_REF", "example/nvkr/.github/workflows/nvkr-qualify.yml"), e.CodeVerifyFailed},
		{"zero attempt", with("GITHUB_RUN_ATTEMPT", "0"), e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run, err := QualifierFromEnv(func(name string) (string, bool) {
				value, ok := tc.env[name]
				return value, ok
			})
			if tc.code != "" {
				requireCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if run != (Run{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-qualify.yml", RunID: "202", Attempt: "1", Commit: strings.Repeat("b", 40)}) {
				t.Fatalf("got %#v", run)
			}
		})
	}
}
