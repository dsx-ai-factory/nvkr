// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releasemanifest

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationoutcome"
)

// OutcomeInputs supplies what a RunResult does not carry: the qualify job's own
// Run, the triggering workflow_run event (read before qualification, used only
// when qualification returned no per-output checks), the attempt's start and
// finish, and the secret values to keep out of the record.
type OutcomeInputs struct {
	Qualifier  qualificationoutcome.Run
	Event      qualificationoutcome.Event
	StartedAt  time.Time
	FinishedAt time.Time
	Secrets    []string
}

// NewOutcome classifies one production qualification attempt for run qualify
// --out. The outcome describes the result and points at its signed evidence and
// manifest by digest. It grants no qualification, manifest or release authority.
// Times stay outside the result, so they change no digest.
func NewOutcome(result *RunResult, runErr error, in OutcomeInputs) (qualificationoutcome.Outcome, error) {
	var publication *qualification.Publication
	if result != nil {
		publication = result.Qualification
	}
	var outcome qualificationoutcome.Outcome
	if publication == nil || publication.ArtifactChecks == nil {
		// Qualification returned no per-output checks (policy, source, invocation
		// or handoff admission refused first), so only the event names the source.
		var err error
		if outcome, err = qualificationoutcome.FromEvent(in.Event, in.Qualifier, qualificationoutcome.StageQualify, in.StartedAt, in.FinishedAt); err != nil {
			return qualificationoutcome.Outcome{}, err
		}
	} else {
		origin := publication.ArtifactChecks.Origin
		outcome = qualificationoutcome.Outcome{SchemaVersion: qualificationoutcome.SchemaVersion, Lane: qualificationoutcome.LanePipelineAttested,
			SourceIdentity: qualificationoutcome.SourceEvidence,
			Source:         qualificationoutcome.Source{Run: qualificationoutcome.Run{Repository: origin.Repository, Workflow: origin.Workflow, RunID: origin.RunID, Attempt: origin.Attempt, Commit: origin.Commit}},
			Qualifier:      in.Qualifier, StartedAt: qualificationoutcome.FormatTime(in.StartedAt), FinishedAt: qualificationoutcome.FormatTime(in.FinishedAt),
			EvidenceDigest: publication.EvidenceDigest, ManifestDigest: result.ManifestDigest, ManifestSubject: result.ManifestSubject.Ref,
			Checks: outcomeChecks(publication, in.Secrets)}
		switch {
		case result.Successful && runErr == nil:
			outcome.Status = qualificationoutcome.StatusPassed
		case !publication.Successful:
			outcome.Status, outcome.Stage = qualificationoutcome.StatusFailed, qualificationoutcome.StageQualify
		default:
			// Qualification passed; issuance, its read-back or the final close did not.
			outcome.Status, outcome.Stage = qualificationoutcome.StatusFailed, qualificationoutcome.StageManifest
		}
	}
	if runErr != nil {
		code := outcomeErrorCode(runErr)
		// The code has its own field, so its "[CODE] " prefix would only spend message bytes.
		message := qualificationoutcome.Scrub(strings.TrimPrefix(runErr.Error(), "["+string(code)+"] "), in.Secrets)
		if message == "" {
			// An error with no text still names its code.
			message = string(code)
		}
		outcome.Error = &qualificationoutcome.Failure{Code: string(code), Message: message}
	}
	if result != nil {
		// encoding/json turns invalid UTF-8, such as cosign stderr cut mid-character,
		// into U+FFFD where the canonical codec would refuse the whole result.
		// RedactDocument then renders canonical bytes; the result is never digested.
		raw, err := json.Marshal(result)
		if err != nil {
			return qualificationoutcome.Outcome{}, e.Wrap(e.CodeInternal, err, "encoding the qualification result for its outcome")
		}
		if outcome.Result, err = qualificationoutcome.RedactDocument(raw, in.Secrets); err != nil {
			return qualificationoutcome.Outcome{}, err
		}
	}
	if err := outcome.Validate(); err != nil {
		return qualificationoutcome.Outcome{}, err
	}
	// Shared error text repeats in every output's checks, so a badly failing
	// attempt can outgrow the bound. Keep its record and drop the full result,
	// which the job log also carries.
	raw, err := canonical.Marshal(outcome)
	if err != nil {
		return qualificationoutcome.Outcome{}, err
	}
	if len(raw) > qualificationoutcome.MaxBytes && len(outcome.Result) > 0 {
		outcome.Result, outcome.ResultOmitted = nil, true
	}
	return outcome, nil
}

// outcomeChecks lists every artifact check and native smoke verdict per output.
func outcomeChecks(publication *qualification.Publication, secrets []string) []qualificationoutcome.Check {
	var checks []qualificationoutcome.Check
	for _, output := range publication.ArtifactChecks.Outputs {
		for _, check := range output.Checks {
			checks = append(checks, qualificationoutcome.Check{Output: output.Name, Subject: output.Subject, Check: check.Name, Status: check.Status,
				Code: check.Code, Message: qualificationoutcome.Scrub(check.Message, secrets)})
		}
	}
	for _, verdict := range publication.Verdicts {
		checks = append(checks, qualificationoutcome.Check{Output: verdict.Output, Subject: verdict.Subject, Check: verdict.Check, Status: verdict.Status,
			Message: qualificationoutcome.Scrub(verdict.Reason, secrets)})
	}
	qualificationoutcome.SortChecks(checks)
	return checks
}

func outcomeErrorCode(err error) e.Code {
	var coded *e.Error
	if errors.As(err, &coded) && coded.Code != "" {
		return coded.Code
	}
	return e.CodeInternal
}
