// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationoutcome

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// collectTimeout bounds reading one Run's outcomes, normally a few files.
const collectTimeout = 2 * time.Minute

// builtQualifyMessage is the cause of a qualify attempt built because its job
// wrote no outcome.
const builtQualifyMessage = "the qualify job wrote no outcome; its job log has the cause"

// Expect is what a publish job requires of the producer Run behind its event.
type Expect struct {
	// Repository is the publishing repository; the producer must be in it and
	// built from its own head.
	Repository string
	// Workflow is the producer workflow path.
	Workflow string
	// Branch is the branch the producer must have run on.
	Branch string
}

// AdmitEvent admits the producer Run a publish job describes: the configured
// producer workflow, in this repository, built from its own head on the configured
// branch, started by a producer event, with a conclusion that has an outcome. It
// repeats the job's own condition, so a mismatch is CodeTrustPolicyMissing.
func AdmitEvent(event Event, expect Expect) error {
	refuse := func(reason string) error { return e.New(e.CodeTrustPolicyMissing, "the producer Run %s", reason) }
	switch {
	case expect.Repository == "" || expect.Workflow == "" || expect.Branch == "":
		return e.New(e.CodeUsage, "admitting the producer Run requires its repository, workflow and branch")
	case event.Action != "completed":
		return refuse("has not completed")
	case event.Repository.FullName != expect.Repository || event.HeadRepository != event.Repository:
		return refuse("is not built from this repository's own head")
	case event.HeadBranch != expect.Branch:
		return refuse("is not on the configured branch")
	case event.Path != expect.Workflow:
		return refuse("is not the configured producer workflow")
	case !slices.Contains(ProducerEvents(), event.Event):
		return refuse("was not started by a producer event")
	case !slices.Contains(ProducerConclusions(), event.Conclusion):
		return refuse("has a conclusion with no outcome")
	}
	return nil
}

// Selection is one publish job's request: where its Run's outcomes and triggering
// event are, the producer and qualify workflows it trusts, and what the qualify
// job reported.
type Selection struct {
	Outcomes        string
	EventPath       string
	SourceWorkflow  string
	SourceBranch    string
	QualifyWorkflow string
	Qualify         QualifyReport
}

// QualifyReport is what the publish job knows of the qualify job: its result, as
// GitHub reports it, and from its report step the run attempt it last ran in and
// whether that attempt wrote an outcome. Attempt is empty and Written false when
// the report step did not run. Re-running only failed jobs keeps a successful
// qualify job's result and report, so the attempt says which outcome they are
// about.
type QualifyReport struct {
	Result  string
	Attempt string
	Written bool
}

// The qualify job results that mean it ran; GitHub also reports a canceled or
// skipped job.
const (
	qualifyPassed = "success"
	qualifyFailed = "failure"
)

var qualifyResults = []string{qualifyPassed, qualifyFailed, "cancelled", "skipped"} //nolint:misspell // GitHub's job result spelling.

func (r QualifyReport) ran() bool { return r.Result == qualifyPassed || r.Result == qualifyFailed }

func (r QualifyReport) validate(current Run) error {
	switch {
	case !slices.Contains(qualifyResults, r.Result):
		return e.New(e.CodeUsage, "the qualify job's result must be success, failure, cancelled or skipped") //nolint:misspell // GitHub's job result spelling.
	case r.Attempt == "" && r.Written:
		return e.New(e.CodeUsage, "a written qualify outcome needs the qualify job's attempt")
	case r.Attempt != "" && (!numberPattern.MatchString(r.Attempt) || compareDecimal(r.Attempt, current.Attempt) > 0):
		return e.New(e.CodeUsage, "the qualify job's attempt is not an attempt of this Run")
	}
	return nil
}

// Select chooses what one publish job publishes. It admits the job's own Run from
// lookup (QualifierFromEnv), which must be the configured qualify workflow, and the
// producer Run its event describes (AdmitEvent), then collects the Run's outcomes
// and plans them (Collect, Plan). A job outside the configured qualify workflow is
// CodeTrustPolicyMissing.
func Select(ctx context.Context, lookup func(string) (string, bool), selection Selection, now time.Time) (publish []Outcome, built *Outcome, err error) {
	if ctx == nil || selection.QualifyWorkflow == "" {
		return nil, nil, e.New(e.CodeUsage, "selecting outcomes requires context and the qualify workflow")
	}
	current, err := QualifierFromEnv(lookup)
	if err != nil {
		return nil, nil, err
	}
	if current.Workflow != selection.QualifyWorkflow {
		return nil, nil, e.New(e.CodeTrustPolicyMissing, "this job is not the configured qualify workflow")
	}
	event, err := ReadEvent(ctx, selection.EventPath)
	if err != nil {
		return nil, nil, err
	}
	if err := AdmitEvent(event, Expect{Repository: current.Repository, Workflow: selection.SourceWorkflow, Branch: selection.SourceBranch}); err != nil {
		return nil, nil, err
	}
	outcomes, err := Collect(ctx, selection.Outcomes)
	if err != nil {
		return nil, nil, err
	}
	return Plan(event, outcomes, current, selection.Qualify, now)
}

// SelectRecorded is Select for the step that publishes: every outcome must already
// be on record, so a re-run publishes the outcome select built, and the job
// uploaded, instead of building another. An outcome Select would build is refused.
func SelectRecorded(ctx context.Context, lookup func(string) (string, bool), selection Selection, now time.Time) ([]Outcome, error) {
	publish, built, err := Select(ctx, lookup, selection, now)
	if err != nil {
		return nil, err
	}
	if built != nil {
		return nil, e.New(e.CodeVerifyFailed, "the outcome select built is not among the Run's outcomes")
	}
	return publish, nil
}

// Collect reads every outcome under dir as download-artifact leaves them: flat for
// one artifact, one directory per artifact for several. A missing dir holds no
// outcome. Any other entry must be a valid outcome, because the publisher never
// skips a record it cannot read. Identical copies collapse; two different
// outcomes for one source and qualifier attempt are refused as ambiguous. The
// result is in publish order (SortOutcomes).
func Collect(ctx context.Context, dir string) ([]Outcome, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "collecting outcomes requires context")
	}
	if strings.TrimSpace(dir) == "" {
		return nil, e.New(e.CodeUsage, "collecting outcomes requires a directory")
	}
	ctx, cancel := context.WithTimeout(ctx, collectTimeout)
	defer cancel()
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, e.Wrap(e.CodeVerifyFailed, err, "reading the outcome directory")
	case !info.IsDir():
		return nil, e.New(e.CodeVerifyFailed, "the outcome path is not a directory")
	}
	found := map[string][]byte{}
	outcomes := map[string]Outcome{}
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		name, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {
			return e.New(e.CodeVerifyFailed, "outcome entry %s is not a regular file", filepath.ToSlash(name))
		}
		raw, err := readOutcome(ctx, path)
		if err != nil {
			return e.Wrap(errorCode(err, e.CodeVerifyFailed), err, "outcome file %s", filepath.ToSlash(name))
		}
		outcome, err := Decode(raw)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "outcome file %s", filepath.ToSlash(name))
		}
		key := strings.Join([]string{outcome.Source.RunID, outcome.Source.Attempt, outcome.Qualifier.RunID, outcome.Qualifier.Attempt}, "/")
		if previous, ok := found[key]; ok {
			if !bytes.Equal(previous, raw) {
				return e.New(e.CodeVerifyFailed, "two different outcomes describe one qualifier attempt")
			}
			return nil
		}
		found[key], outcomes[key] = raw, outcome
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "collecting outcomes")
		}
		return nil, e.Wrap(errorCode(err, e.CodeVerifyFailed), err, "collecting outcomes")
	}
	collected := slices.Collect(maps.Values(outcomes))
	SortOutcomes(collected)
	return collected, nil
}

func readOutcome(ctx context.Context, path string) ([]byte, error) {
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "qualification outcome", MaxBytes: MaxBytes})
	if err != nil {
		return nil, err
	}
	defer func() { _ = held.Close() }()
	return held.ReadAllContext(ctx, MaxBytes)
}

// Plan selects what a publish job publishes for one admitted producer Run (see
// AdmitEvent). It takes every collected outcome, the job's own Run, the qualify
// job's report and the time now. Every outcome of this Run is published, in
// publish order. An outcome of another Run or producer attempt, of a later
// qualifier attempt than this one, or whose stage contradicts the producer's
// conclusion is refused. One errored outcome is built for an attempt that nothing
// describes:
//   - a producer that did not succeed, when the Run has no outcome: stage
//     producer, timed by the event;
//   - a qualify attempt that failed without writing an outcome: stage qualify,
//     started with the producer Run and finishing now, so TestGrid still reads
//     it, with an error that says the job wrote no outcome.
//
// The outcome of a qualify attempt that passed, or that reports writing one, is a
// record; when it cannot be found it was lost, and it is never replaced by a
// built one.
func Plan(event Event, outcomes []Outcome, current Run, report QualifyReport, now time.Time) (publish []Outcome, built *Outcome, err error) {
	if err := current.validate("publishing"); err != nil {
		return nil, nil, err
	}
	if err := report.validate(current); err != nil {
		return nil, nil, err
	}
	if !slices.Contains(ProducerConclusions(), event.Conclusion) {
		return nil, nil, e.New(e.CodeTrustPolicyMissing, "the producer Run has a conclusion with no outcome")
	}
	succeeded := event.Conclusion == "success"
	if !succeeded && report.ran() {
		return nil, nil, e.New(e.CodeVerifyFailed, "the qualify job reports running for a producer Run that did not succeed")
	}
	described := false
	for _, outcome := range outcomes {
		if outcome.Source.Repository != current.Repository || outcome.Source.Workflow != event.Path || outcome.Source.RunID != event.RunID ||
			outcome.Source.Attempt != event.Attempt || outcome.Source.Commit != event.HeadSHA ||
			outcome.Qualifier.Repository != current.Repository || outcome.Qualifier.Workflow != current.Workflow ||
			outcome.Qualifier.RunID != current.RunID || outcome.Qualifier.Commit != current.Commit {
			return nil, nil, e.New(e.CodeVerifyFailed, "an outcome describes another Run or attempt")
		}
		if compareDecimal(outcome.Qualifier.Attempt, current.Attempt) > 0 {
			return nil, nil, e.New(e.CodeVerifyFailed, "an outcome comes from a later qualifier attempt than this one")
		}
		if (outcome.Stage == StageProducer) == succeeded {
			return nil, nil, e.New(e.CodeVerifyFailed, "an outcome's stage contradicts the producer Run's conclusion")
		}
		described = described || outcome.Qualifier.Attempt == report.Attempt
	}
	publish = slices.Clone(outcomes)
	switch {
	case report.ran() && report.Attempt == "":
		return nil, nil, e.New(e.CodeVerifyFailed, "the qualify job ran but reported no attempt")
	case !described && (report.Result == qualifyPassed || report.Written):
		// Whatever the job's result, an outcome it reports writing is a record.
		return nil, nil, e.New(e.CodeVerifyFailed, "the qualify job's outcome was not found; only running the qualify job again can recover it")
	case report.ran() && !described:
		// The attempt failed before it wrote an outcome. Clamp a clock behind the
		// producer's start rather than refuse the record.
		qualifier := current
		qualifier.Attempt = report.Attempt
		outcome, err := FromEvent(event, qualifier, StageQualify, event.RunStartedAt, latest(now, event.RunStartedAt))
		if err != nil {
			return nil, nil, err
		}
		// A qualify record names its cause; only the job log knows more.
		outcome.Error = &Failure{Code: string(e.CodeVerifyFailed), Message: builtQualifyMessage}
		built = &outcome
	case !succeeded && len(outcomes) == 0:
		outcome, err := FromEvent(event, current, StageProducer, event.RunStartedAt, event.UpdatedAt)
		if err != nil {
			return nil, nil, err
		}
		built = &outcome
	}
	if built != nil {
		publish = append(publish, *built)
	}
	SortOutcomes(publish)
	return publish, built, nil
}

// SortOutcomes puts outcomes in publish order: by qualifier attempt, then source
// attempt, both numerically, then by finish time.
func SortOutcomes(outcomes []Outcome) {
	slices.SortFunc(outcomes, func(a, b Outcome) int {
		return cmp.Or(compareDecimal(a.Qualifier.Attempt, b.Qualifier.Attempt), compareDecimal(a.Source.Attempt, b.Source.Attempt),
			compareFinish(a, b))
	})
}

// compareFinish orders two outcomes by finish time: RFC 3339 text with trimmed
// fractions does not sort as text.
func compareFinish(a, b Outcome) int {
	_, aFinished, aErr := a.Times()
	_, bFinished, bErr := b.Times()
	if aErr != nil || bErr != nil {
		return cmp.Compare(a.FinishedAt, b.FinishedAt)
	}
	return aFinished.Compare(bFinished)
}

// compareDecimal orders canonical decimal strings, which are too long for uint64.
func compareDecimal(a, b string) int { return cmp.Or(cmp.Compare(len(a), len(b)), cmp.Compare(a, b)) }

func latest(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}

// errorCode is err's own code, or fallback when it has none.
func errorCode(err error, fallback e.Code) e.Code {
	var coded *e.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return fallback
}
