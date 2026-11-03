// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationoutcome

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Qualification reports an output's digest as its subject.
func outcomeFixture(status Status) Outcome {
	subject := "sha256:" + strings.Repeat("e", 64)
	outcome := Outcome{SchemaVersion: SchemaVersion, Status: status, Lane: LanePipelineAttested, SourceIdentity: SourceEvidence,
		Source:    Source{Run: Run{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-run.yml", RunID: "101", Attempt: "1", Commit: strings.Repeat("a", 40)}},
		Qualifier: Run{Repository: "example/nvkr", Workflow: ".github/workflows/nvkr-qualify.yml", RunID: "202", Attempt: "2", Commit: strings.Repeat("b", 40)},
		StartedAt: "2026-11-02T10:00:00.5Z", FinishedAt: "2026-11-02T10:30:00Z",
		Checks: []Check{{Output: "tool", Subject: subject, Check: "build-signature-and-provenance", Status: "pass"},
			{Output: "tool", Subject: subject, Check: "delivered-tool-version", Status: "pass"}},
		Result: json.RawMessage(`{"successful":true}`)}
	switch status {
	case StatusPassed:
		outcome.EvidenceDigest, outcome.ManifestDigest = "sha256:"+strings.Repeat("c", 64), "sha256:"+strings.Repeat("d", 64)
		outcome.ManifestSubject = "ghcr.io/example/nvkr/manifest@sha256:" + strings.Repeat("d", 64)
	case StatusFailed:
		outcome.Stage = StageQualify
		outcome.Checks[0].Status, outcome.Checks[0].Code, outcome.Checks[0].Message = "fail", string(e.CodeSignatureInvalid), "signature does not verify"
		outcome.Error = &Failure{Code: string(e.CodeGateUnmet), Message: "[NVKR-E-GATE-UNMET] qualification refused"}
		outcome.Result = json.RawMessage(`{"successful":false}`)
	case StatusErrored:
		outcome.Stage, outcome.SourceIdentity, outcome.Checks, outcome.Result = StageQualify, SourceUnauthenticatedEvent, nil, nil
		outcome.Source.Event, outcome.Source.Conclusion = "push", "success"
		outcome.Error = &Failure{Code: string(e.CodeTrustPolicyMissing), Message: "[NVKR-E-TRUST-POLICY-MISSING] qualification policy refused"}
	}
	return outcome
}

// encode bypasses Validate so invalid fixtures reach Decode.
func encode(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func requireCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}

func TestQualificationOutcomeDecode(t *testing.T) {
	mutate := func(status Status, change func(*Outcome)) func(*testing.T) []byte {
		return func(t *testing.T) []byte {
			outcome := outcomeFixture(status)
			change(&outcome)
			return encode(t, outcome)
		}
	}
	extraField := func(t *testing.T) []byte {
		var document map[string]any
		if err := json.Unmarshal(encode(t, outcomeFixture(StatusPassed)), &document); err != nil {
			t.Fatal(err)
		}
		document["authority"] = "granted"
		return encode(t, document)
	}
	manifest := func(o *Outcome) {
		o.ManifestDigest = "sha256:" + strings.Repeat("d", 64)
		o.ManifestSubject = "ghcr.io/example/nvkr/manifest@sha256:" + strings.Repeat("d", 64)
	}
	for _, tc := range []struct {
		name  string
		raw   func(*testing.T) []byte
		valid bool
	}{
		{"passed", mutate(StatusPassed, func(*Outcome) {}), true},
		{"passed with an omitted result", mutate(StatusPassed, func(o *Outcome) { o.Result, o.ResultOmitted = nil, true }), true},
		{"repository subject", mutate(StatusPassed, func(o *Outcome) { o.Checks[0].Subject = "ghcr.io/example/nvkr/tool@sha256:" + strings.Repeat("e", 64) }), true},
		{"origin-shaped owner", mutate(StatusPassed, func(o *Outcome) { o.Source.Repository, o.Qualifier.Repository = "my_org.x/nvkr", "my_org.x/nvkr" }), true},
		{"twenty-digit Run", mutate(StatusPassed, func(o *Outcome) { o.Source.RunID = "12345678901234567890" }), true},
		{"failed", mutate(StatusFailed, func(*Outcome) {}), true},
		{"failed manifest", mutate(StatusFailed, func(o *Outcome) { o.Stage = StageManifest }), true},
		{"failed manifest with its manifest", mutate(StatusFailed, func(o *Outcome) { o.Stage = StageManifest; manifest(o) }), true},
		{"errored qualify", mutate(StatusErrored, func(*Outcome) {}), true},
		{"errored producer", mutate(StatusErrored, func(o *Outcome) { o.Stage, o.Source.Conclusion, o.Error = StageProducer, "timed_out", nil }), true},
		{"failed qualify with a manifest", mutate(StatusFailed, manifest), false},
		{"manifest digest without subject", mutate(StatusPassed, func(o *Outcome) { o.ManifestSubject = "" }), false},
		{"failed without error", mutate(StatusFailed, func(o *Outcome) { o.Error = nil }), false},
		{"passed without result", mutate(StatusPassed, func(o *Outcome) { o.Result = nil }), false},
		{"result also marked omitted", mutate(StatusPassed, func(o *Outcome) { o.ResultOmitted = true }), false},
		{"email subject", mutate(StatusPassed, func(o *Outcome) { o.Checks[0].Subject = "someone@example.com" }), false},
		{"control character in subject", mutate(StatusPassed, func(o *Outcome) { o.Checks[0].Subject = "\x00@sha256:" + strings.Repeat("e", 64) }), false},
		{"result with a bucket URL", mutate(StatusPassed, func(o *Outcome) { o.Result = json.RawMessage(`{"note":"gs://private-bucket/x"}`) }), false},
		{"result with a JWT", mutate(StatusPassed, func(o *Outcome) {
			o.Result = json.RawMessage(`{"note":"eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJyZXBvIn0.c2lnbmF0dXJl"}`)
		}), false},
		{"workflow starting with a dot", mutate(StatusPassed, func(o *Outcome) { o.Source.Workflow = ".github/workflows/.nvkr-run.yml" }), false},
		{"twenty-one-digit Run", mutate(StatusPassed, func(o *Outcome) { o.Source.RunID = "123456789012345678901" }), false},
		{"unknown field", extraField, false},
		{"insignificant whitespace", func(t *testing.T) []byte { return append(encode(t, outcomeFixture(StatusPassed)), '\n') }, false},
		{"schema version", mutate(StatusPassed, func(o *Outcome) { o.SchemaVersion = "nvkr.dev/qualification-outcome/v2" }), false},
		{"lane", mutate(StatusPassed, func(o *Outcome) { o.Lane = "qualified" }), false},
		{"status", mutate(StatusPassed, func(o *Outcome) { o.Status = "skipped" }), false},
		{"passed with stage", mutate(StatusPassed, func(o *Outcome) { o.Stage = StageQualify }), false},
		{"passed with failed check", mutate(StatusPassed, func(o *Outcome) { o.Checks[1].Status = "fail" }), false},
		{"passed without manifest", mutate(StatusPassed, func(o *Outcome) { o.ManifestDigest = "" }), false},
		{"passed with error", mutate(StatusPassed, func(o *Outcome) { o.Error = &Failure{Code: string(e.CodeGateUnmet), Message: "refused"} }), false},
		{"failed without stage", mutate(StatusFailed, func(o *Outcome) { o.Stage = "" }), false},
		{"failed producer", mutate(StatusFailed, func(o *Outcome) { o.Stage = StageProducer }), false},
		{"errored manifest", mutate(StatusErrored, func(o *Outcome) { o.Stage = StageManifest }), false},
		{"errored with checks", mutate(StatusErrored, func(o *Outcome) { o.Checks = outcomeFixture(StatusPassed).Checks }), false},
		{"errored from evidence", mutate(StatusErrored, func(o *Outcome) { o.SourceIdentity = SourceEvidence }), false},
		{"passed from event", mutate(StatusPassed, func(o *Outcome) { o.SourceIdentity = SourceUnauthenticatedEvent }), false},
		{"source identity", mutate(StatusPassed, func(o *Outcome) { o.SourceIdentity = "github" }), false},
		{"evidence with event fields", mutate(StatusPassed, func(o *Outcome) { o.Source.Event = "push" }), false},
		{"errored producer that succeeded", mutate(StatusErrored, func(o *Outcome) { o.Stage = StageProducer }), false},
		{"errored qualify after failed producer", mutate(StatusErrored, func(o *Outcome) { o.Source.Conclusion = "failure" }), false},
		{"pull request source", mutate(StatusErrored, func(o *Outcome) { o.Source.Event = "pull_request" }), false},
		{"non-decimal run", mutate(StatusPassed, func(o *Outcome) { o.Source.RunID = "10a" }), false},
		{"leading zero attempt", mutate(StatusPassed, func(o *Outcome) { o.Qualifier.Attempt = "01" }), false},
		{"short commit", mutate(StatusPassed, func(o *Outcome) { o.Source.Commit = strings.Repeat("a", 39) }), false},
		{"uppercase commit", mutate(StatusPassed, func(o *Outcome) { o.Qualifier.Commit = strings.Repeat("B", 40) }), false},
		{"workflow outside workflows", mutate(StatusPassed, func(o *Outcome) { o.Source.Workflow = "../nvkr-run.yml" }), false},
		{"foreign qualifier", mutate(StatusPassed, func(o *Outcome) { o.Qualifier.Repository = "other/nvkr" }), false},
		{"non-UTC time", mutate(StatusPassed, func(o *Outcome) { o.StartedAt = "2026-11-02T11:00:00+01:00" }), false},
		{"non-canonical time", mutate(StatusPassed, func(o *Outcome) { o.StartedAt = "2026-11-02T10:00:00.50Z" }), false},
		{"finished before started", mutate(StatusPassed, func(o *Outcome) { o.FinishedAt = "2026-11-02T09:00:00Z" }), false},
		{"malformed digest", mutate(StatusPassed, func(o *Outcome) { o.EvidenceDigest = "sha256:abc" }), false},
		{"tag subject", mutate(StatusPassed, func(o *Outcome) { o.ManifestSubject = "ghcr.io/example/nvkr/manifest:latest" }), false},
		{"unsorted checks", mutate(StatusPassed, func(o *Outcome) { o.Checks[0], o.Checks[1] = o.Checks[1], o.Checks[0] }), false},
		{"duplicate check", mutate(StatusPassed, func(o *Outcome) { o.Checks[1] = o.Checks[0] }), false},
		{"unknown check status", mutate(StatusFailed, func(o *Outcome) { o.Checks[0].Status = "not-evaluated" }), false},
		{"unscrubbed message", mutate(StatusFailed, func(o *Outcome) { o.Checks[0].Message = "read gs://private/results denied" }), false},
		{"uncoded error", mutate(StatusFailed, func(o *Outcome) { o.Error.Code = "E-1" }), false},
		{"empty error", mutate(StatusFailed, func(o *Outcome) { o.Error.Message = "" }), false},
		{"array result", mutate(StatusFailed, func(o *Outcome) { o.Result = json.RawMessage(`[]`) }), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(tc.raw(t))
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			requireCode(t, err, e.CodeVerifyFailed)
		})
	}
}

func TestQualificationOutcomeDecodeSizeBound(t *testing.T) {
	sized := func(t *testing.T, size int) []byte {
		outcome := outcomeFixture(StatusPassed)
		outcome.Result = json.RawMessage(`{"pad":""}`)
		base := len(encode(t, outcome))
		outcome.Result = json.RawMessage(`{"pad":"` + strings.Repeat("x", size-base) + `"}`)
		raw := encode(t, outcome)
		if len(raw) != size {
			t.Fatalf("fixture is %d bytes, want %d", len(raw), size)
		}
		return raw
	}
	if _, err := Decode(sized(t, MaxBytes)); err != nil {
		t.Fatalf("outcome at the bound was refused: %v", err)
	}
	_, err := Decode(sized(t, MaxBytes+1))
	requireCode(t, err, e.CodeVerifyFailed)
}

func TestQualificationOutcomeRoundTrip(t *testing.T) {
	for _, status := range []Status{StatusPassed, StatusFailed, StatusErrored} {
		t.Run(string(status), func(t *testing.T) {
			outcome := outcomeFixture(status)
			raw, err := outcome.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Decode(raw)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded, outcome) {
				t.Fatalf("round trip changed the outcome:\n got %#v\nwant %#v", decoded, outcome)
			}
			again, err := decoded.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(again, raw) {
				t.Fatal("outcome bytes are not stable")
			}
		})
	}
}

func TestQualificationOutcomeMarshalRefusesInvalid(t *testing.T) {
	outcome := outcomeFixture(StatusPassed)
	outcome.Lane = "local-execution"
	_, err := outcome.Marshal()
	requireCode(t, err, e.CodeVerifyFailed)
}

func TestQualificationOutcomeSortChecks(t *testing.T) {
	checks := []Check{{Output: "b", Check: "a"}, {Output: "a", Check: "b", Subject: "y"}, {Output: "a", Check: "b", Subject: "x"}, {Output: "a", Check: "a"}}
	SortChecks(checks)
	want := []Check{{Output: "a", Check: "a"}, {Output: "a", Check: "b", Subject: "x"}, {Output: "a", Check: "b", Subject: "y"}, {Output: "b", Check: "a"}}
	if !reflect.DeepEqual(checks, want) {
		t.Fatalf("got %#v", checks)
	}
}
