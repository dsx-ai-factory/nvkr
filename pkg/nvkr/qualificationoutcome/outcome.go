// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationoutcome

import (
	"cmp"
	"encoding/json"
	"regexp"
	"slices"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	// SchemaVersion is the only admitted outcome schema.
	SchemaVersion = "nvkr.dev/qualification-outcome/v1"
	// LanePipelineAttested is factory.TierPipelineAttested. The leaf cannot import factory.
	LanePipelineAttested = "pipeline-attested"
	// MaxBytes bounds one encoded outcome. A complete nvke-m2 RunResult is about 435 KiB.
	MaxBytes = 4 << 20
	// Directory holds the generated qualify job's outcome file. The job uploads it
	// as ArtifactPrefix followed by <run>-<attempt>.
	Directory      = ".nvkr/qualification-outcome"
	ArtifactPrefix = "nvkr-qualification-outcome-"
)

// FileName names one Run attempt's outcome inside Directory. The generator passes
// a shell expansion as the attempt.
func FileName(attempt string) string { return "outcome-" + attempt + ".json" }

// ProducerEvents are the producer triggers whose Runs can have an outcome.
func ProducerEvents() []string { return []string{"push", "workflow_dispatch"} }

// ProducerConclusions are the producer conclusions that yield an outcome: success
// through qualification, the others as an errored producer stage. A canceled,
// skipped or neutral producer Run has no outcome.
func ProducerConclusions() []string {
	return []string{"failure", "startup_failure", "success", "timed_out"}
}

// Status is the attempt's result.
type Status string

const (
	StatusPassed  Status = "passed"
	StatusFailed  Status = "failed"
	StatusErrored Status = "errored"
)

// Stage is where a failed or errored attempt stopped.
type Stage string

const (
	StageProducer Stage = "producer"
	StageQualify  Stage = "qualify"
	StageManifest Stage = "manifest"
)

// SourceIdentity says where the source Run fields came from.
type SourceIdentity string

const (
	// SourceEvidence copies the source Run that qualification authenticated.
	SourceEvidence SourceIdentity = "evidence"
	// SourceUnauthenticatedEvent copies the source Run from the triggering
	// workflow_run event, because qualification returned no per-output checks to
	// take an authenticated Run from. The copied fields are unauthenticated.
	SourceUnauthenticatedEvent SourceIdentity = "unauthenticated-event"
)

// Run is one exact GitHub Actions Run attempt.
type Run struct {
	Repository string `json:"repository"`
	Workflow   string `json:"workflow"`
	RunID      string `json:"runId"`
	Attempt    string `json:"attempt"`
	Commit     string `json:"commit"`
}

// Source is the producing Run. Event and Conclusion are present only when the
// source came from the event.
type Source struct {
	Run
	Event      string `json:"event,omitempty"`
	Conclusion string `json:"conclusion,omitempty"`
}

// Check is one artifact check or native smoke verdict for one output.
type Check struct {
	Output  string `json:"output"`
	Subject string `json:"subject,omitempty"`
	Check   string `json:"check"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// Failure is the attempt's coded error with scrubbed text.
type Failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Outcome is one qualifier attempt's outcome, or a failed producer attempt's.
// Result is the attempt's canonical RunResult, kept opaque: a reader projects the
// typed fields and never interprets Result. ResultOmitted says Result was dropped
// to keep the outcome within MaxBytes; the job log still carries it.
type Outcome struct {
	SchemaVersion   string          `json:"schemaVersion"`
	Status          Status          `json:"status"`
	Stage           Stage           `json:"stage,omitempty"`
	Lane            string          `json:"lane"`
	SourceIdentity  SourceIdentity  `json:"sourceIdentity"`
	Source          Source          `json:"source"`
	Qualifier       Run             `json:"qualifier"`
	StartedAt       string          `json:"startedAt"`
	FinishedAt      string          `json:"finishedAt"`
	EvidenceDigest  string          `json:"evidenceDigest,omitempty"`
	ManifestDigest  string          `json:"manifestDigest,omitempty"`
	ManifestSubject string          `json:"manifestSubject,omitempty"`
	Checks          []Check         `json:"checks,omitempty"`
	Error           *Failure        `json:"error,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	ResultOmitted   bool            `json:"resultOmitted,omitempty"`
}

var (
	// The Run patterns copy qualificationorigin/source.go, which this leaf cannot
	// import; a releasemanifest test keeps them in step.
	repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	workflowPattern   = regexp.MustCompile(`^\.github/workflows/[A-Za-z0-9][A-Za-z0-9_.-]*\.ya?ml$`)
	numberPattern     = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
	commitPattern     = regexp.MustCompile(`^[0-9a-f]{40}$`)
	namePattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	codePattern       = regexp.MustCompile(`^NVKR-E-[A-Z0-9]+(?:-[A-Z0-9]+)*$`)
	subjectPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:-]*@sha256:[0-9a-f]{64}$`)
	checkStatuses     = []string{"fail", "not-applicable", "pass"}
)

const maxSubjectBytes = 512

// Decode admits one canonical, valid outcome of at most MaxBytes.
func Decode(raw []byte) (Outcome, error) {
	if len(raw) > MaxBytes {
		return Outcome{}, e.New(e.CodeVerifyFailed, "qualification outcome exceeds %d bytes", MaxBytes)
	}
	var outcome Outcome
	if err := canonical.UnmarshalExact(raw, &outcome); err != nil {
		return Outcome{}, e.Wrap(e.CodeVerifyFailed, err, "decoding qualification outcome")
	}
	if err := outcome.Validate(); err != nil {
		return Outcome{}, err
	}
	return outcome, nil
}

// Marshal validates the outcome and returns its canonical bytes.
func (o Outcome) Marshal() ([]byte, error) {
	if err := o.Validate(); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(o)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxBytes {
		return nil, e.New(e.CodeVerifyFailed, "qualification outcome exceeds %d bytes", MaxBytes)
	}
	return raw, nil
}

// Validate admits only a complete, internally consistent outcome. It checks the
// description's shape and authenticates none of its claims.
func (o Outcome) Validate() error {
	if o.SchemaVersion != SchemaVersion || o.Lane != LanePipelineAttested {
		return invalid("has an unsupported schema version or lane")
	}
	if err := o.validateStatus(); err != nil {
		return err
	}
	if err := o.Source.validate(o.SourceIdentity, o.Stage); err != nil {
		return err
	}
	if err := o.Qualifier.validate("qualifier"); err != nil {
		return err
	}
	if o.Source.Repository != o.Qualifier.Repository {
		return invalid("has source and qualifier Runs in different repositories")
	}
	started, err := parseTime("startedAt", o.StartedAt)
	if err != nil {
		return err
	}
	finished, err := parseTime("finishedAt", o.FinishedAt)
	if err != nil {
		return err
	}
	if finished.Before(started) {
		return invalid("finishes before it starts")
	}
	for _, digest := range []string{o.EvidenceDigest, o.ManifestDigest} {
		if digest != "" && !canonical.IsDigest(digest) {
			return invalid("has a malformed digest")
		}
	}
	if o.ManifestSubject != "" && !subjectPattern.MatchString(o.ManifestSubject) {
		return invalid("has a malformed manifest subject")
	}
	for i, check := range o.Checks {
		if !namePattern.MatchString(check.Output) || !namePattern.MatchString(check.Check) || !slices.Contains(checkStatuses, check.Status) ||
			(check.Subject != "" && !checkSubject(check.Subject)) || (check.Code != "" && !codePattern.MatchString(check.Code)) || !scrubbed(check.Message) {
			return invalid("has a malformed check")
		}
		if i > 0 && compareChecks(o.Checks[i-1], check) >= 0 {
			return invalid("checks are not sorted and unique")
		}
	}
	if o.Error != nil && (!codePattern.MatchString(o.Error.Code) || o.Error.Message == "" || !scrubbed(o.Error.Message)) {
		return invalid("has a malformed error")
	}
	if o.ResultOmitted && len(o.Result) > 0 {
		return invalid("has a result that it also marks omitted")
	}
	if len(o.Result) > 0 && (o.Result[0] != '{' || !redactedDocument(o.Result)) {
		return invalid("result is not a redacted canonical JSON object")
	}
	return nil
}

func (o Outcome) validateStatus() error {
	if (o.ManifestDigest == "") != (o.ManifestSubject == "") {
		return invalid("has a manifest digest without its subject, or the reverse")
	}
	manifest := o.ManifestDigest != ""
	switch o.Status {
	case StatusPassed:
		if o.Stage != "" || o.SourceIdentity != SourceEvidence || o.Error != nil || len(o.Checks) == 0 ||
			o.EvidenceDigest == "" || !manifest || (len(o.Result) == 0 && !o.ResultOmitted) {
			return invalid("passed requires authenticated checks, evidence, a manifest and its result, and no stage or error")
		}
		for _, check := range o.Checks {
			if check.Status == "fail" {
				return invalid("passed has a failed check")
			}
		}
	case StatusFailed:
		// A manifest exists only after qualification passed, so only a manifest
		// stage can carry one (issued, then refused at read-back or close).
		if (o.Stage != StageQualify && o.Stage != StageManifest) || o.SourceIdentity != SourceEvidence || o.Error == nil ||
			(manifest && o.Stage != StageManifest) {
			return invalid("failed requires an authenticated qualify or manifest stage and an error; only a manifest stage carries a manifest")
		}
	case StatusErrored:
		// No error is required: a failed producer has no qualifier error of its own.
		if (o.Stage != StageProducer && o.Stage != StageQualify) || o.SourceIdentity != SourceUnauthenticatedEvent || len(o.Checks) != 0 ||
			o.EvidenceDigest != "" || manifest {
			return invalid("errored requires a producer or qualify stage from the event, and no checks, evidence or manifest")
		}
	default:
		return invalid("has unknown status %q", o.Status)
	}
	return nil
}

func (s Source) validate(identity SourceIdentity, stage Stage) error {
	if err := s.Run.validate("source"); err != nil {
		return err
	}
	switch identity {
	case SourceEvidence:
		if s.Event != "" || s.Conclusion != "" {
			return invalid("has an authenticated source with unauthenticated event fields")
		}
	case SourceUnauthenticatedEvent:
		// Only a producer stage carries an unsuccessful producer; a qualifier ran
		// only because its producer succeeded.
		if !slices.Contains(ProducerEvents(), s.Event) || !slices.Contains(ProducerConclusions(), s.Conclusion) ||
			(stage == StageProducer) == (s.Conclusion == "success") {
			return invalid("has a source event, conclusion and stage that do not agree")
		}
	default:
		return invalid("has unknown source identity %q", identity)
	}
	return nil
}

func (r Run) validate(role string) error {
	if !repositoryPattern.MatchString(r.Repository) || !workflowPattern.MatchString(r.Workflow) || !numberPattern.MatchString(r.RunID) ||
		!numberPattern.MatchString(r.Attempt) || !commitPattern.MatchString(r.Commit) {
		return invalid("has a %s Run that is not an exact repository, workflow, Run attempt and commit", role)
	}
	return nil
}

// SortChecks orders checks by output, check and subject, as Validate requires.
func SortChecks(checks []Check) { slices.SortFunc(checks, compareChecks) }

func compareChecks(a, b Check) int {
	return cmp.Or(cmp.Compare(a.Output, b.Output), cmp.Compare(a.Check, b.Check), cmp.Compare(a.Subject, b.Subject))
}

// FormatTime renders an outcome time: RFC 3339 in UTC without trailing zeros.
func FormatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTime(name, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.IsZero() || parsed.Location() != time.UTC || parsed.Format(time.RFC3339Nano) != value {
		return time.Time{}, invalid("has a %s that is not an RFC 3339 UTC time", name)
	}
	return parsed, nil
}

// checkSubject admits the subjects qualification reports: an output's digest, or
// its repository reference by digest.
func checkSubject(value string) bool {
	return len(value) <= maxSubjectBytes && (canonical.IsDigest(value) || subjectPattern.MatchString(value))
}

func invalid(format string, args ...any) error {
	return e.New(e.CodeVerifyFailed, "qualification outcome "+format, args...)
}
