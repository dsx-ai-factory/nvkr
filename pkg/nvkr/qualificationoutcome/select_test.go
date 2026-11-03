// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationoutcome

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func writeOutcome(t *testing.T, dir, name string, outcome Outcome) {
	t.Helper()
	raw, err := outcome.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// attemptOutcome is a passed outcome of the fixture producer Run from qualifier
// attempt n of the fixture qualifier Run.
func attemptOutcome(n string) Outcome {
	outcome := outcomeFixture(StatusPassed)
	outcome.Qualifier.Attempt = n
	return outcome
}

func qualifierAttempts(outcomes []Outcome) []string {
	var attempts []string
	for _, outcome := range outcomes {
		attempts = append(attempts, outcome.Qualifier.Attempt)
	}
	return attempts
}

func fixtureEvent(t *testing.T, change func(document, run map[string]any)) Event {
	t.Helper()
	event, err := ReadEvent(t.Context(), writeEvent(t, eventFixture(t, change)))
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func TestQualificationOutcomeCollect(t *testing.T) {
	t.Run("a missing directory holds no outcome", func(t *testing.T) {
		got, err := Collect(t.Context(), filepath.Join(t.TempDir(), "missing"))
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("an empty directory holds no outcome", func(t *testing.T) {
		got, err := Collect(t.Context(), t.TempDir())
		if err != nil || len(got) != 0 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("one artifact is flat", func(t *testing.T) {
		dir := t.TempDir()
		writeOutcome(t, dir, "outcome-1.json", attemptOutcome("1"))
		got, err := Collect(t.Context(), dir)
		if err != nil || !slices.Equal(qualifierAttempts(got), []string{"1"}) {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	t.Run("several artifacts nest and come back in publish order", func(t *testing.T) {
		dir := t.TempDir()
		writeOutcome(t, dir, "nvkr-qualification-outcome-202-10/outcome-10.json", attemptOutcome("10"))
		writeOutcome(t, dir, "nvkr-qualification-outcome-202-2/outcome-2.json", attemptOutcome("2"))
		writeOutcome(t, dir, "nvkr-qualification-outcome-202-1/outcome-1.json", attemptOutcome("1"))
		got, err := Collect(t.Context(), dir)
		if err != nil || !slices.Equal(qualifierAttempts(got), []string{"1", "2", "10"}) {
			t.Fatalf("got %v, %v", qualifierAttempts(got), err)
		}
	})
	t.Run("identical copies collapse", func(t *testing.T) {
		dir := t.TempDir()
		writeOutcome(t, dir, "a/outcome-1.json", attemptOutcome("1"))
		writeOutcome(t, dir, "b/outcome-1.json", attemptOutcome("1"))
		got, err := Collect(t.Context(), dir)
		if err != nil || len(got) != 1 {
			t.Fatalf("got %v, %v", got, err)
		}
	})
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) string
	}{
		{"an invalid file", func(t *testing.T) string {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "outcome-1.json"), []byte(`{"schemaVersion":"nvkr.dev/qualification-outcome/v0"}`), 0o600); err != nil {
				t.Fatal(err)
			}
			return dir
		}},
		{"two different outcomes for one attempt", func(t *testing.T) string {
			dir := t.TempDir()
			other := attemptOutcome("1")
			other.FinishedAt = "2026-11-02T10:31:00Z"
			writeOutcome(t, dir, "a/outcome-1.json", attemptOutcome("1"))
			writeOutcome(t, dir, "b/outcome-1.json", other)
			return dir
		}},
		{"an entry that is not a regular file", func(t *testing.T) string {
			dir := t.TempDir()
			writeOutcome(t, dir, "real/outcome-1.json", attemptOutcome("1"))
			if err := os.Symlink(filepath.Join(dir, "real", "outcome-1.json"), filepath.Join(dir, "outcome-1.json")); err != nil {
				t.Fatal(err)
			}
			return dir
		}},
		{"a file where the directory should be", func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "outcomes")
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}},
		{"an oversize file", func(t *testing.T) string {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "outcome-1.json"), make([]byte, MaxBytes+1), 0o600); err != nil {
				t.Fatal(err)
			}
			return dir
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Collect(t.Context(), tc.setup(t))
			requireCode(t, err, e.CodeVerifyFailed)
		})
	}
	t.Run("usage", func(t *testing.T) {
		_, err := Collect(t.Context(), " ")
		requireCode(t, err, e.CodeUsage)
	})
}

func TestQualificationOutcomePlan(t *testing.T) {
	now := time.Date(2026, 11, 2, 11, 0, 0, 0, time.UTC)
	success := fixtureEvent(t, func(_, run map[string]any) { run["conclusion"] = "success" })
	failure := fixtureEvent(t, nil)
	canceled := fixtureEvent(t, func(_, run map[string]any) { run["conclusion"] = "cancelled" }) //nolint:misspell // GitHub conclusion spelling.
	attempt := func(n string) Run {
		run := qualifierFixture()
		run.Attempt = n
		return run
	}
	changed := func(change func(*Outcome)) Outcome {
		outcome := attemptOutcome("1")
		change(&outcome)
		return outcome
	}
	producerRecord, err := FromEvent(failure, attempt("1"), StageProducer, failure.RunStartedAt, failure.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	passed := func(attempt string, written bool) QualifyReport {
		return QualifyReport{Result: "success", Attempt: attempt, Written: written}
	}
	failed := func(attempt string, written bool) QualifyReport {
		return QualifyReport{Result: "failure", Attempt: attempt, Written: written}
	}
	skipped := QualifyReport{Result: "skipped"}
	qualifyAt := func(attempt, finished string) *Outcome {
		return &Outcome{Stage: StageQualify, StartedAt: "2026-11-02T09:00:00Z", FinishedAt: finished, Qualifier: Run{Attempt: attempt}}
	}
	for _, tc := range []struct {
		name     string
		event    Event
		outcomes []Outcome
		current  Run
		report   QualifyReport
		now      time.Time
		publish  []string
		built    *Outcome
		code     e.Code
	}{
		{name: "every outcome, in attempt order", event: success, outcomes: []Outcome{attemptOutcome("2"), attemptOutcome("1")}, current: attempt("2"),
			report: passed("2", true), publish: []string{"1", "2"}},
		{name: "a re-run of only the publish job", event: success, outcomes: []Outcome{attemptOutcome("1")}, current: attempt("2"), report: passed("1", true),
			publish: []string{"1"}},
		{name: "a re-run attempt whose outcome is lost", event: success, outcomes: []Outcome{attemptOutcome("1")}, current: attempt("2"),
			report: failed("2", true), code: e.CodeVerifyFailed},
		{name: "a reported outcome that is missing", event: success, current: attempt("1"), report: failed("1", true), code: e.CodeVerifyFailed},
		{name: "a passed qualify job whose outcome was not written", event: success, current: attempt("1"), report: passed("1", false),
			code: e.CodeVerifyFailed},
		{name: "a failed qualify job that wrote nothing", event: success, current: attempt("1"), report: failed("1", false), now: now, publish: []string{"1"},
			built: qualifyAt("1", "2026-11-02T11:00:00Z")},
		{name: "a re-run attempt that wrote nothing", event: success, outcomes: []Outcome{attemptOutcome("1")}, current: attempt("2"),
			report: failed("2", false), now: now, publish: []string{"1", "2"}, built: qualifyAt("2", "2026-11-02T11:00:00Z")},
		{name: "a clock behind the producer's start", event: success, current: attempt("1"), report: failed("1", false), now: now.Add(-3 * time.Hour),
			publish: []string{"1"}, built: qualifyAt("1", "2026-11-02T09:00:00Z")},
		{name: "a failed qualify job with no attempt", event: success, current: attempt("1"), report: failed("", false), code: e.CodeVerifyFailed},
		{name: "a qualify job that did not run", event: success, current: attempt("1"), report: skipped},
		{name: "a canceled qualify job", event: success, outcomes: []Outcome{attemptOutcome("1")}, current: attempt("2"),
			report: QualifyReport{Result: "cancelled", Attempt: "2"}, publish: []string{"1"}}, //nolint:misspell // GitHub's job result spelling.
		{name: "a canceled qualify job whose written outcome is missing", event: success, outcomes: []Outcome{attemptOutcome("1")}, current: attempt("2"),
			report: QualifyReport{Result: "cancelled", Attempt: "2", Written: true}, code: e.CodeVerifyFailed}, //nolint:misspell // GitHub's job result spelling.
		{name: "a producer that failed", event: failure, current: attempt("1"), report: skipped, now: now, publish: []string{"1"},
			built: &Outcome{Stage: StageProducer, StartedAt: "2026-11-02T09:00:00Z", FinishedAt: "2026-11-02T09:40:00Z", Qualifier: Run{Attempt: "1"}}},
		{name: "a failed producer's record from an earlier attempt", event: failure, outcomes: []Outcome{producerRecord}, current: attempt("2"),
			report: skipped, publish: []string{"1"}},
		{name: "an outcome of another producer Run", event: success, outcomes: []Outcome{changed(func(o *Outcome) { o.Source.RunID = "999" })},
			current: attempt("1"), report: passed("1", true), code: e.CodeVerifyFailed},
		{name: "an outcome of another producer attempt", event: success, outcomes: []Outcome{changed(func(o *Outcome) { o.Source.Attempt = "2" })},
			current: attempt("1"), report: passed("1", true), code: e.CodeVerifyFailed},
		{name: "an outcome at another commit", event: success, outcomes: []Outcome{changed(func(o *Outcome) { o.Source.Commit = strings.Repeat("c", 40) })},
			current: attempt("1"), report: passed("1", true), code: e.CodeVerifyFailed},
		{name: "an outcome of another qualifier commit", event: success,
			outcomes: []Outcome{changed(func(o *Outcome) { o.Qualifier.Commit = strings.Repeat("d", 40) })}, current: attempt("1"), report: passed("1", true),
			code: e.CodeVerifyFailed},
		{name: "an outcome of another qualify workflow", event: success,
			outcomes: []Outcome{changed(func(o *Outcome) { o.Qualifier.Workflow = ".github/workflows/nvkr-other.yml" })}, current: attempt("1"),
			report: passed("1", true), code: e.CodeVerifyFailed},
		{name: "an outcome from a later qualifier attempt", event: success, outcomes: []Outcome{attemptOutcome("3")}, current: attempt("2"),
			report: passed("2", true), code: e.CodeVerifyFailed},
		{name: "a qualification outcome for a failed producer", event: failure, outcomes: []Outcome{attemptOutcome("1")}, current: attempt("1"),
			report: skipped, code: e.CodeVerifyFailed},
		{name: "a producer outcome for a producer that succeeded", event: success, outcomes: []Outcome{producerRecord}, current: attempt("1"),
			report: skipped, code: e.CodeVerifyFailed},
		{name: "a qualify job that ran for a failed producer", event: failure, current: attempt("1"), report: failed("1", false), code: e.CodeVerifyFailed},
		{name: "a written outcome without its attempt", event: success, current: attempt("1"), report: failed("", true), code: e.CodeUsage},
		{name: "a qualify attempt after this Run's", event: success, current: attempt("2"), report: failed("3", false), code: e.CodeUsage},
		{name: "a malformed qualify attempt", event: success, current: attempt("2"), report: failed("01", false), code: e.CodeUsage},
		{name: "an unknown qualify result", event: success, current: attempt("1"), report: QualifyReport{Result: "neutral"}, code: e.CodeUsage},
		{name: "no qualify result", event: success, current: attempt("1"), code: e.CodeUsage},
		{name: "a canceled producer", event: canceled, current: attempt("1"), report: skipped, code: e.CodeTrustPolicyMissing},
		{name: "an inexact publishing Run", event: success, current: Run{Repository: "example/nvkr"}, report: skipped, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			publish, built, err := Plan(tc.event, tc.outcomes, tc.current, tc.report, tc.now)
			if tc.code != "" {
				requireCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(qualifierAttempts(publish), tc.publish) {
				t.Fatalf("publish = %v, want %v", qualifierAttempts(publish), tc.publish)
			}
			if (built == nil) != (tc.built == nil) {
				t.Fatalf("built = %v, want %v", built, tc.built)
			}
			if built == nil {
				return
			}
			wantQualifier := tc.current
			wantQualifier.Attempt = tc.built.Qualifier.Attempt
			if built.Status != StatusErrored || built.Stage != tc.built.Stage || built.StartedAt != tc.built.StartedAt || built.FinishedAt != tc.built.FinishedAt ||
				built.Qualifier != wantQualifier || built.Source.RunID != "101" ||
				!slices.ContainsFunc(publish, func(o Outcome) bool { return reflect.DeepEqual(o, *built) }) {
				t.Fatalf("built %+v", built)
			}
			// A built qualify attempt names its cause, so it is a writable record; a
			// failed producer has no qualifier error.
			if (built.Error != nil) != (built.Stage == StageQualify) || (built.Error != nil && built.Error.Code != string(e.CodeVerifyFailed)) {
				t.Fatalf("built error %+v", built.Error)
			}
			if _, err := built.Marshal(); err != nil {
				t.Fatalf("built outcome is not a record: %v", err)
			}
		})
	}
}

func TestQualificationOutcomeAdmitEvent(t *testing.T) {
	expect := Expect{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-run.yml", Branch: "main"}
	for _, tc := range []struct {
		name   string
		change func(document, run map[string]any)
		expect Expect
		code   e.Code
	}{
		{name: "a failed pushed producer", expect: expect},
		{name: "a dispatched producer that succeeded", expect: expect,
			change: func(_, run map[string]any) { run["event"], run["conclusion"] = "workflow_dispatch", "success" }},
		{name: "another workflow", expect: expect, code: e.CodeTrustPolicyMissing,
			change: func(_, run map[string]any) { run["path"] = ".github/workflows/other.yml" }},
		{name: "a pull request", expect: expect, code: e.CodeTrustPolicyMissing,
			change: func(_, run map[string]any) { run["event"] = "pull_request" }},
		{name: "a canceled producer", expect: expect, code: e.CodeTrustPolicyMissing,
			change: func(_, run map[string]any) { run["conclusion"] = "cancelled" }}, //nolint:misspell // GitHub conclusion spelling.
		{name: "a fork's head", expect: expect, code: e.CodeTrustPolicyMissing,
			change: func(_, run map[string]any) {
				run["head_repository"] = map[string]any{"full_name": "fork/nvkr", "id": json.Number("8")}
			}},
		{name: "another repository", expect: Expect{Repository: "example/other", Workflow: expect.Workflow, Branch: "main"}, code: e.CodeTrustPolicyMissing},
		{name: "another branch", expect: expect, code: e.CodeTrustPolicyMissing,
			change: func(_, run map[string]any) { run["head_branch"] = "feature" }},
		{name: "a requested Run", expect: expect, code: e.CodeTrustPolicyMissing,
			change: func(document, _ map[string]any) { document["action"] = "requested" }},
		{name: "no expectation", expect: Expect{Repository: "example/nvkr"}, code: e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := AdmitEvent(fixtureEvent(t, tc.change), tc.expect)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			requireCode(t, err, tc.code)
			if strings.Contains(err.Error(), "fork/nvkr") {
				t.Fatal("refusal echoes event text")
			}
		})
	}
}

func TestQualificationOutcomeSelect(t *testing.T) {
	now := time.Date(2026, 11, 2, 11, 0, 0, 0, time.UTC)
	job := map[string]string{"GITHUB_REPOSITORY": "example/nvkr", "GITHUB_WORKFLOW_REF": "example/nvkr/.github/workflows/nvkr-qualify.yml@refs/heads/main",
		"GITHUB_RUN_ID": "202", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_SHA": strings.Repeat("b", 40)}
	lookup := func(name string) (string, bool) { value, ok := job[name]; return value, ok }
	request := func(change func(*Selection)) Selection {
		selection := Selection{Outcomes: filepath.Join(t.TempDir(), "missing"), EventPath: writeEvent(t, eventFixture(t, nil)),
			SourceWorkflow: ".github/workflows/nvkr-run.yml", SourceBranch: "main", QualifyWorkflow: ".github/workflows/nvkr-qualify.yml",
			Qualify: QualifyReport{Result: "skipped"}}
		if change != nil {
			change(&selection)
		}
		return selection
	}
	t.Run("a failed producer with no outcome", func(t *testing.T) {
		publish, built, err := Select(t.Context(), lookup, request(nil), now)
		if err != nil {
			t.Fatal(err)
		}
		if built == nil || built.Stage != StageProducer || built.Qualifier != qualifierFixture() || !reflect.DeepEqual(publish, []Outcome{*built}) {
			t.Fatalf("publish %+v, built %+v", publish, built)
		}
	})
	t.Run("publishing takes only recorded outcomes", func(t *testing.T) {
		_, err := SelectRecorded(t.Context(), lookup, request(nil), now)
		requireCode(t, err, e.CodeVerifyFailed)
		_, built, err := Select(t.Context(), lookup, request(nil), now)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := built.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		outcomes := t.TempDir()
		if err := os.WriteFile(filepath.Join(outcomes, "outcome-1-built.json"), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		publish, err := SelectRecorded(t.Context(), lookup, request(func(s *Selection) { s.Outcomes = outcomes }), now)
		if err != nil || !reflect.DeepEqual(publish, []Outcome{*built}) {
			t.Fatalf("publish %+v, %v", publish, err)
		}
	})
	for _, tc := range []struct {
		name   string
		lookup func(string) (string, bool)
		change func(*Selection)
		code   e.Code
	}{
		{name: "another qualify workflow", lookup: lookup, change: func(s *Selection) { s.QualifyWorkflow = ".github/workflows/other.yml" }, code: e.CodeTrustPolicyMissing},
		{name: "a producer on another branch", lookup: lookup, change: func(s *Selection) { s.SourceBranch = "release" }, code: e.CodeTrustPolicyMissing},
		{name: "no qualifier environment", lookup: func(string) (string, bool) { return "", false }, code: e.CodeProvenanceMissing},
		{name: "an unreadable outcome", lookup: lookup, code: e.CodeVerifyFailed, change: func(s *Selection) {
			s.Outcomes = t.TempDir()
			if err := os.WriteFile(filepath.Join(s.Outcomes, "outcome-1.json"), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "no qualify workflow", lookup: lookup, change: func(s *Selection) { s.QualifyWorkflow = "" }, code: e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Select(t.Context(), tc.lookup, request(tc.change), now)
			requireCode(t, err, tc.code)
		})
	}
}
