// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releasemanifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationoutcome"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

const outcomeSecret = "sentinel-registry-password"

// outcomePublication lists outputs and checks out of order so the mapper's sort
// is observable. The failing check and verdict carry the sentinel secret.
// Qualification reports each output's digest as its subject.
func outcomePublication(successful bool) *qualification.Publication {
	toolSubject := "sha256:" + strings.Repeat("e", 64)
	imageSubject := "sha256:" + strings.Repeat("f", 64)
	signature := lifecycle.ArtifactCheckResult{Name: "build-signature-and-provenance", Status: "pass"}
	smoke := qualification.Verdict{Output: "tool", Subject: toolSubject, Check: "delivered-tool-version", Status: "pass"}
	publication := &qualification.Publication{Successful: successful,
		ArtifactChecks: &lifecycle.ArtifactCheckDescription{
			Origin: qualificationorigin.Description{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-run.yml", RunID: "101", Attempt: "1",
				Commit: strings.Repeat("a", 40), DefaultBranch: "main"},
			Outputs: []lifecycle.ArtifactCheckOutput{
				{Name: "tool", Subject: toolSubject, Checks: []lifecycle.ArtifactCheckResult{{Name: "signed-evidence", Status: "pass"}, signature}},
				{Name: "image", Subject: imageSubject, Checks: []lifecycle.ArtifactCheckResult{{Name: "payload-pins", Status: "not-applicable"}, signature}},
			}},
		Verdicts: []qualification.Verdict{smoke, {Output: "image", Subject: imageSubject, Check: "delivered-tool-version", Status: "not-applicable"}},
	}
	if successful {
		publication.EvidenceDigest = "sha256:" + strings.Repeat("c", 64)
	} else {
		publication.ArtifactChecks.Outputs[0].Checks[1] = lifecycle.ArtifactCheckResult{Name: "build-signature-and-provenance", Status: "fail",
			Code: string(e.CodeSignatureInvalid), Message: "registry login with " + outcomeSecret + " refused"}
		publication.Verdicts[0] = qualification.Verdict{Output: "tool", Subject: toolSubject, Check: "delivered-tool-version", Status: "fail", Reason: "token " + outcomeSecret}
	}
	return publication
}

func outcomeManifest(result *RunResult) *RunResult {
	result.ManifestDigest = "sha256:" + strings.Repeat("d", 64)
	result.ManifestSubject = sign.Subject{Ref: "ghcr.io/example/nvkr/release@sha256:" + strings.Repeat("d", 64), Digest: "sha256:" + strings.Repeat("d", 64)}
	return result
}

func outcomeEvent() qualificationoutcome.Event {
	repository := qualificationoutcome.Repository{FullName: "example/nvkr", ID: "7"}
	return qualificationoutcome.Event{Action: "completed", RunID: "101", Attempt: "1", HeadSHA: strings.Repeat("a", 40), HeadBranch: "main",
		Path: ".github/workflows/nvkr-run.yml", Event: "workflow_dispatch", Status: "completed", Conclusion: "success", Repository: repository, HeadRepository: repository}
}

func outcomeInputs() OutcomeInputs {
	return OutcomeInputs{
		Qualifier: qualificationoutcome.Run{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-qualify.yml", RunID: "202", Attempt: "1", Commit: strings.Repeat("b", 40)},
		Event:     outcomeEvent(), StartedAt: time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC), FinishedAt: time.Date(2026, 11, 2, 10, 30, 0, 0, time.UTC),
		Secrets: []string{outcomeSecret},
	}
}

func TestReleaseManifestOutcomeClassifies(t *testing.T) {
	gate := e.New(e.CodeGateUnmet, "qualification refused with "+outcomeSecret)
	for _, tc := range []struct {
		name          string
		result        *RunResult
		err           error
		status        qualificationoutcome.Status
		stage         qualificationoutcome.Stage
		identity      qualificationoutcome.SourceIdentity
		manifest      bool
		code          e.Code
		checks, smoke int
	}{
		{"passed", outcomeManifest(&RunResult{Qualification: outcomePublication(true), Successful: true}), nil,
			qualificationoutcome.StatusPassed, "", qualificationoutcome.SourceEvidence, true, "", 6, 2},
		{"failed qualification", &RunResult{Qualification: outcomePublication(false)}, gate,
			qualificationoutcome.StatusFailed, qualificationoutcome.StageQualify, qualificationoutcome.SourceEvidence, false, e.CodeGateUnmet, 6, 2},
		{"failed manifest", &RunResult{Qualification: outcomePublication(true)}, e.New(e.CodeSignatureInvalid, "manifest read-back refused"),
			qualificationoutcome.StatusFailed, qualificationoutcome.StageManifest, qualificationoutcome.SourceEvidence, false, e.CodeSignatureInvalid, 6, 2},
		{"manifest issued but close failed", outcomeManifest(&RunResult{Qualification: outcomePublication(true)}),
			errors.Join(nil, e.Wrap(e.CodeGateUnmet, errors.New("lease lost"), "closing release manifest qualification operation")),
			qualificationoutcome.StatusFailed, qualificationoutcome.StageManifest, qualificationoutcome.SourceEvidence, true, e.CodeGateUnmet, 6, 2},
		{"errored without a result", nil, e.New(e.CodeTrustPolicyMissing, "policy refused"),
			qualificationoutcome.StatusErrored, qualificationoutcome.StageQualify, qualificationoutcome.SourceUnauthenticatedEvent, false, e.CodeTrustPolicyMissing, 0, 0},
		{"errored without qualification", &RunResult{}, e.New(e.CodeEngineCapability, "Linux signer required"),
			qualificationoutcome.StatusErrored, qualificationoutcome.StageQualify, qualificationoutcome.SourceUnauthenticatedEvent, false, e.CodeEngineCapability, 0, 0},
		{"errored before artifact checks", &RunResult{Qualification: &qualification.Publication{}}, errors.New("uncoded source failure"),
			qualificationoutcome.StatusErrored, qualificationoutcome.StageQualify, qualificationoutcome.SourceUnauthenticatedEvent, false, e.CodeInternal, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome, err := NewOutcome(tc.result, tc.err, outcomeInputs())
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Status != tc.status || outcome.Stage != tc.stage || outcome.SourceIdentity != tc.identity {
				t.Fatalf("classified as %s/%s from %s", outcome.Status, outcome.Stage, outcome.SourceIdentity)
			}
			want := qualificationoutcome.Run{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-run.yml", RunID: "101", Attempt: "1", Commit: strings.Repeat("a", 40)}
			if outcome.Source.Run != want || outcome.StartedAt != "2026-11-02T10:00:00Z" || outcome.FinishedAt != "2026-11-02T10:30:00Z" {
				t.Fatalf("lost the source Run or attempt times: %#v", outcome)
			}
			if (outcome.ManifestDigest != "") != tc.manifest || (outcome.ManifestSubject != "") != tc.manifest {
				t.Fatal("manifest digest and subject do not match the result")
			}
			if (tc.code == "") != (outcome.Error == nil) || (outcome.Error != nil && outcome.Error.Code != string(tc.code)) {
				t.Fatalf("error is %#v, want %s", outcome.Error, tc.code)
			}
			if outcome.Error != nil && strings.HasPrefix(outcome.Error.Message, "[NVKR-E-") {
				t.Fatalf("message repeats its code: %q", outcome.Error.Message)
			}
			smoke := 0
			for i, check := range outcome.Checks {
				if check.Check == "delivered-tool-version" {
					smoke++
				}
				if i > 0 && (outcome.Checks[i-1].Output > check.Output || (outcome.Checks[i-1].Output == check.Output && outcome.Checks[i-1].Check >= check.Check)) {
					t.Fatal("checks are not sorted")
				}
			}
			if len(outcome.Checks) != tc.checks || smoke != tc.smoke {
				t.Fatalf("got %d checks with %d smoke verdicts", len(outcome.Checks), smoke)
			}
			if (tc.result == nil) != (len(outcome.Result) == 0) || outcome.ResultOmitted {
				t.Fatal("result is present without a RunResult, missing with one, or omitted")
			}
			raw, err := outcome.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(raw, []byte(outcomeSecret)) {
				t.Fatal("the outcome exposes a secret value")
			}
			decoded, err := qualificationoutcome.Decode(raw)
			if err != nil || !reflect.DeepEqual(decoded, outcome) {
				t.Fatalf("outcome does not round trip: %v", err)
			}
		})
	}
}

// Times are outside the RunResult, so two clocks give the same result bytes.
func TestReleaseManifestOutcomeTimesStayOutsideResult(t *testing.T) {
	result := outcomeManifest(&RunResult{Qualification: outcomePublication(true), Successful: true})
	first := outcomeInputs()
	second := first
	second.StartedAt, second.FinishedAt = first.StartedAt.Add(time.Hour), first.FinishedAt.Add(2*time.Hour)
	a, err := NewOutcome(result, nil, first)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewOutcome(result, nil, second)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Result, b.Result) || !bytes.Equal(a.Result, direct) || a.EvidenceDigest != b.EvidenceDigest || a.ManifestDigest != b.ManifestDigest {
		t.Fatal("the attempt clock changed the result or its digests")
	}
	if a.StartedAt == b.StartedAt || a.FinishedAt == b.FinishedAt {
		t.Fatal("the attempt clock was not recorded")
	}
}

// Cosign stderr cut mid-character reaches check messages; the record keeps it as
// U+FFFD instead of failing.
func TestReleaseManifestOutcomeKeepsInvalidUTF8Diagnostics(t *testing.T) {
	publication := outcomePublication(false)
	publication.ArtifactChecks.Outputs[0].Checks[1].Message = "cosign verify failed: \xe2\x82"
	outcome, err := NewOutcome(&RunResult{Qualification: publication}, e.New(e.CodeSignatureInvalid, "refused"), outcomeInputs())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := outcome.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := qualificationoutcome.Decode(raw); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(outcome.Result, []byte("�")) && !bytes.Contains(outcome.Result, []byte(`�`)) {
		t.Fatal("the invalid bytes were not replaced in the result")
	}
}

// Shared error text repeats per output, so a badly failing attempt drops its
// full result rather than lose its record.
func TestReleaseManifestOutcomeDropsOversizedResult(t *testing.T) {
	publication := outcomePublication(false)
	stderr := strings.Repeat("cosign: registry refused the request; ", 3500)
	publication.ArtifactChecks.Outputs, publication.Verdicts = nil, nil
	for i := range 40 {
		publication.ArtifactChecks.Outputs = append(publication.ArtifactChecks.Outputs, lifecycle.ArtifactCheckOutput{Name: fmt.Sprintf("tool-%02d", i),
			Subject: "sha256:" + strings.Repeat("e", 64), Checks: []lifecycle.ArtifactCheckResult{{Name: "build-signature-and-provenance", Status: "fail",
				Code: string(e.CodeSignatureInvalid), Message: stderr}}})
	}
	outcome, err := NewOutcome(&RunResult{Qualification: publication}, e.New(e.CodeSignatureInvalid, "refused"), outcomeInputs())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := outcome.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.ResultOmitted || len(outcome.Result) != 0 || len(outcome.Checks) != 40 || len(raw) > qualificationoutcome.MaxBytes {
		t.Fatalf("got %d bytes, omitted=%v, %d checks", len(raw), outcome.ResultOmitted, len(outcome.Checks))
	}
}

// The code has its own field, so the message drops it, unless nothing else is left.
func TestReleaseManifestOutcomeErrorMessages(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"coded", e.New(e.CodeGateUnmet, "qualification refused"), "qualification refused"},
		{"code only", e.New(e.CodeGateUnmet, ""), "NVKR-E-GATE-UNMET"},
		{"blank", e.New(e.CodeGateUnmet, "\n"), "NVKR-E-GATE-UNMET"},
		{"uncoded and empty", errors.New(""), "NVKR-E-INTERNAL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outcome, err := NewOutcome(nil, tc.err, outcomeInputs())
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Error == nil || outcome.Error.Message != tc.want {
				t.Fatalf("got %#v, want message %q", outcome.Error, tc.want)
			}
		})
	}
}

func TestReleaseManifestOutcomeRefusesUnrecordableAttempts(t *testing.T) {
	fork := outcomeInputs()
	fork.Event.HeadRepository = qualificationoutcome.Repository{FullName: "fork/nvkr", ID: "8"}
	_, err := NewOutcome(nil, e.New(e.CodeTrustPolicyMissing, "policy refused"), fork)
	requireOutcomeCode(t, err, e.CodeVerifyFailed)

	// A passed result must name its evidence; the mapper never invents it.
	result := outcomeManifest(&RunResult{Qualification: outcomePublication(true), Successful: true})
	result.Qualification.EvidenceDigest = ""
	_, err = NewOutcome(result, nil, outcomeInputs())
	requireOutcomeCode(t, err, e.CodeVerifyFailed)
}

func TestReleaseManifestOutcomeLaneIsPipelineAttested(t *testing.T) {
	if qualificationoutcome.LanePipelineAttested != factory.TierPipelineAttested.String() {
		t.Fatal("the outcome lane drifted from the pipeline-attested evidence tier")
	}
}

// The outcome copies qualificationorigin's Run patterns because the leaf cannot
// import it: every Run one admits, the other must admit too.
func TestReleaseManifestOutcomeRunPatternsMatchOrigin(t *testing.T) {
	base := qualificationoutcome.Run{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-run.yml", RunID: "101", Attempt: "1", Commit: strings.Repeat("a", 40)}
	var runs []qualificationoutcome.Run
	for _, repository := range []string{"my_org/nvkr", "my.org/nvkr", "org-/nvkr", "-org/nvkr", "example/.nvkr", "example/-nvkr", "example/nvkr.git", "ex ample/nvkr", "example"} {
		run := base
		run.Repository = repository
		runs = append(runs, run)
	}
	for _, workflow := range []string{".github/workflows/a_b.yaml", ".github/workflows/.hidden.yml", ".github/workflows/-x.yml", ".github/workflows/x.json", "workflows/x.yml"} {
		run := base
		run.Workflow = workflow
		runs = append(runs, run)
	}
	for _, number := range []string{"1", "0", "01", "12345678901234567890", "123456789012345678901", "1a"} {
		run, attempt := base, base
		run.RunID, attempt.Attempt = number, number
		runs = append(runs, run, attempt)
	}
	for _, commit := range []string{strings.Repeat("A", 40), strings.Repeat("a", 39)} {
		run := base
		run.Commit = commit
		runs = append(runs, run)
	}
	for _, run := range append(runs, base) {
		origin := qualificationorigin.SourceRequest{Repository: run.Repository, RunID: run.RunID, Attempt: run.Attempt, Commit: run.Commit, Workflow: run.Workflow, DefaultBranch: "main"}.Validate()
		event := outcomeEvent()
		event.Repository = qualificationoutcome.Repository{FullName: run.Repository, ID: "7"}
		event.HeadRepository, event.RunID, event.Attempt, event.HeadSHA, event.Path = event.Repository, run.RunID, run.Attempt, run.Commit, run.Workflow
		qualifier := outcomeInputs().Qualifier
		qualifier.Repository = run.Repository
		_, outcome := qualificationoutcome.FromEvent(event, qualifier, qualificationoutcome.StageQualify, time.Unix(1, 0), time.Unix(2, 0))
		if (origin == nil) != (outcome == nil) {
			t.Errorf("%+v: origin admits=%v, outcome admits=%v", run, origin == nil, outcome == nil)
		}
	}
}

// AdmitOutcome reads only the environment it is given and refuses in order: the
// qualifier Run, then the event and the errored record it would yield, then the
// secrets.
func TestReleaseManifestAdmitOutcome(t *testing.T) {
	writeEvent := func(change func(run map[string]any)) string {
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
	eventPath := writeEvent(func(map[string]any) {})
	// A readable event the errored record refuses: its head is a fork.
	forkHead := writeEvent(func(run map[string]any) {
		run["head_repository"] = map[string]any{"full_name": "fork/nvkr", "id": json.Number("8")}
	})
	// An event that reading itself refuses: it names no head commit.
	headless := writeEvent(func(run map[string]any) { delete(run, "head_sha") })
	// The process environment must not leak in: if it did, the "no qualifier
	// Run" and "no event" cases would find these.
	t.Setenv("GITHUB_RUN_ID", "999")
	t.Setenv("GITHUB_EVENT_PATH", eventPath)
	// environment applies change to the qualify job's environment; an empty
	// value unsets the variable.
	environment := func(change map[string]string) []string {
		env := map[string]string{"GITHUB_REPOSITORY": "example/nvkr", "GITHUB_WORKFLOW_REF": "example/nvkr/.github/workflows/nvkr-qualify.yml@refs/heads/main",
			"GITHUB_RUN_ID": "202", "GITHUB_RUN_ATTEMPT": "1", "GITHUB_SHA": strings.Repeat("b", 40), "GITHUB_EVENT_PATH": eventPath,
			"NVKR_REGISTRY_PASSWORD": outcomeSecret}
		for name, value := range change {
			if value == "" {
				delete(env, name)
			} else {
				env[name] = value
			}
		}
		var environ []string
		for name, value := range env {
			environ = append(environ, name+"="+value)
		}
		return environ
	}
	at := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	inputs, err := AdmitOutcome(t.Context(), environment(nil), at)
	if err != nil {
		t.Fatal(err)
	}
	event, err := qualificationoutcome.ReadEvent(t.Context(), eventPath)
	if err != nil {
		t.Fatal(err)
	}
	if inputs.Qualifier.RunID != "202" || !reflect.DeepEqual(inputs.Event, event) || !slices.Contains(inputs.Secrets, outcomeSecret) ||
		!inputs.StartedAt.Equal(at) || !inputs.FinishedAt.IsZero() {
		t.Fatalf("admitted %#v", inputs)
	}
	// Like os.LookupEnv, the first entry for a name wins.
	shadowed, err := AdmitOutcome(t.Context(), append([]string{"GITHUB_RUN_ID=303"}, environment(nil)...), at)
	if err != nil || shadowed.Qualifier.RunID != "303" {
		t.Fatalf("the first entry did not win: %v", err)
	}

	for _, tc := range []struct {
		name   string
		change map[string]string
		code   e.Code
	}{
		{"no qualifier Run", map[string]string{"GITHUB_RUN_ID": ""}, e.CodeProvenanceMissing},
		{"no event", map[string]string{"GITHUB_EVENT_PATH": ""}, e.CodeProvenanceMissing},
		{"an event reading refuses", map[string]string{"GITHUB_EVENT_PATH": headless}, e.CodeVerifyFailed},
		{"an event the errored record refuses", map[string]string{"GITHUB_EVENT_PATH": forkHead}, e.CodeVerifyFailed},
		{"secret too short to redact", map[string]string{"NVKR_REGISTRY_PASSWORD": "abc"}, e.CodeUsage},
		// Each refusal comes before every later one.
		{"the qualifier Run before the event", map[string]string{"GITHUB_RUN_ID": "", "GITHUB_EVENT_PATH": headless}, e.CodeProvenanceMissing},
		{"no event before the secrets", map[string]string{"GITHUB_EVENT_PATH": "", "NVKR_REGISTRY_PASSWORD": "abc"}, e.CodeProvenanceMissing},
		{"the errored record before the secrets", map[string]string{"GITHUB_EVENT_PATH": forkHead, "NVKR_REGISTRY_PASSWORD": "abc"}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := AdmitOutcome(t.Context(), environment(tc.change), at)
			requireOutcomeCode(t, err, tc.code)
		})
	}
	t.Run("an empty environment fails closed", func(t *testing.T) {
		_, err := AdmitOutcome(t.Context(), nil, at)
		requireOutcomeCode(t, err, e.CodeProvenanceMissing)
	})
}

func requireOutcomeCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}
