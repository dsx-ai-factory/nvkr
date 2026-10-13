// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"context"
	"encoding/hex"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

const SchemaVersion = "nvkr.dev/test-observation/v1"
const PredicateType = sign.TestObservationPredicateType
const ObservationFileName = "observation.json"
const MaxObservationBytes = 1 << 20

// Observation is a descriptive wire record. Decoding it restores no authority.
type Observation struct {
	SchemaVersion string              `json:"schemaVersion"`
	Inputs        Inputs              `json:"inputs"`
	Origin        Origin              `json:"origin"`
	Subjects      []Subject           `json:"subjects"`
	Suite         Suite               `json:"suite"`
	Environment   Environment         `json:"environment"`
	Execution     Outcome             `json:"execution"`
	Cleanup       Outcome             `json:"cleanup"`
	Files         []File              `json:"files"`
	Summary       testresults.Summary `json:"summary"`
}
type Inputs struct {
	DefinitionDigest  string `json:"definitionDigest"`
	EnvironmentDigest string `json:"environmentDigest"`
	PlanDigest        string `json:"planDigest"`
	PublicationDigest string `json:"publicationDigest,omitempty"`
}

// Origin unknown never invents a producing Run. Declared bindings are not authenticated.
type Origin struct {
	Status     string `json:"status"`
	Repository string `json:"repository,omitempty"`
	Commit     string `json:"commit,omitempty"`
	Run        string `json:"run,omitempty"`
	Attempt    int    `json:"attempt,omitempty"`
	Workflow   string `json:"workflow,omitempty"`
}
type Subject struct {
	Output         string `json:"output"`
	ArtifactDigest string `json:"artifactDigest"`
	PayloadDigest  string `json:"payloadDigest"`
	BuildDigest    string `json:"buildDigest"`
	Platform       string `json:"platform"`
}
type CaseID struct {
	Suite string `json:"suite"`
	Class string `json:"class"`
	Name  string `json:"name"`
}
type Suite struct {
	Name            string   `json:"name"`
	HarnessRevision string   `json:"harnessRevision"`
	ImageDigest     string   `json:"imageDigest"`
	Selection       []string `json:"selection"`
	PolicyDigest    string   `json:"policyDigest"`
	Invocation      string   `json:"invocation"`
	RequiredCases   []CaseID `json:"requiredCases"`
}

// Environment has only explicitly selected public facts. Values cannot be
// automatically considered safe to disclose; producers review the projection.
type Environment struct {
	Role  string `json:"role"`
	Facts []Fact `json:"facts"`
}
type Fact struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Evidence string `json:"evidence"`
	Phase    string `json:"phase"`
}
type Outcome struct {
	Status   string `json:"status"`
	ExitCode int    `json:"exitCode"`
}
type File struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

func text(s string) bool {
	return s != "" && len(s) <= 4096 && utf8.ValidString(s) && !strings.ContainsAny(s, "\x00\r\n")
}
func revision(value string) bool {
	if len(value) != 40 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
func invalid(format string, args ...any) error { return e.New(e.CodeVerifyFailed, format, args...) }
func caseKey(c CaseID) string                  { return c.Suite + "\x00" + c.Class + "\x00" + c.Name }
func subjectKey(s Subject) string              { return s.Output + "\x00" + s.Platform }
func factKey(f Fact) string                    { return f.Phase + "\x00" + f.Name }

func normalizeEmpty(o *Observation) {
	if o.Suite.RequiredCases == nil {
		o.Suite.RequiredCases = []CaseID{}
	}
	if o.Files == nil {
		o.Files = []File{}
	}
}
func normalize(o *Observation) {
	normalizeEmpty(o)
	slices.SortFunc(o.Subjects, func(a, b Subject) int { return strings.Compare(subjectKey(a), subjectKey(b)) })
	slices.Sort(o.Suite.Selection)
	slices.SortFunc(o.Suite.RequiredCases, func(a, b CaseID) int { return strings.Compare(caseKey(a), caseKey(b)) })
	slices.SortFunc(o.Environment.Facts, func(a, b Fact) int { return strings.Compare(factKey(a), factKey(b)) })
	slices.SortFunc(o.Files, func(a, b File) int { return strings.Compare(a.Name, b.Name) })
}
func validate(o Observation, inventory bool) error {
	if inventory && (o.Suite.RequiredCases == nil || o.Files == nil) {
		return invalid("empty requiredCases and files must use arrays, not null")
	}
	if o.SchemaVersion != SchemaVersion {
		return invalid("unsupported test observation schema %q", o.SchemaVersion)
	}
	if !canonical.IsDigest(o.Inputs.DefinitionDigest) || !canonical.IsDigest(o.Inputs.EnvironmentDigest) || !canonical.IsDigest(o.Inputs.PlanDigest) || (o.Inputs.PublicationDigest != "" && !canonical.IsDigest(o.Inputs.PublicationDigest)) {
		return invalid("test observation requires exact input digests")
	}
	switch o.Origin.Status {
	case "unknown":
		if o.Origin != (Origin{Status: "unknown"}) {
			return invalid("unknown origin must not assert producing Run bindings")
		}
	case "declared":
		if !text(o.Origin.Repository) || !revision(o.Origin.Commit) || !text(o.Origin.Run) || !text(o.Origin.Workflow) || o.Origin.Attempt < 1 {
			return invalid("declared origin is incomplete")
		}
	default:
		return invalid("unsupported origin evidence level")
	}
	if len(o.Subjects) == 0 || len(o.Subjects) > 256 {
		return invalid("exact named subjects required")
	}
	for i, s := range o.Subjects {
		if !text(s.Output) || !text(s.Platform) || !canonical.IsDigest(s.ArtifactDigest) || !canonical.IsDigest(s.PayloadDigest) || !canonical.IsDigest(s.BuildDigest) || (i > 0 && subjectKey(o.Subjects[i-1]) >= subjectKey(s)) {
			return invalid("subjects must have unique sorted exact output and platform bindings")
		}
	}
	suite := o.Suite
	if !text(suite.Name) || !revision(suite.HarnessRevision) || !text(suite.Invocation) || !canonical.IsDigest(suite.ImageDigest) || !canonical.IsDigest(suite.PolicyDigest) || len(suite.Selection) == 0 || len(suite.Selection) > 256 || len(suite.RequiredCases) > 10000 {
		return invalid("suite invocation requires harness, image, policy and selection pins")
	}
	for i, v := range suite.Selection {
		if !text(v) || (i > 0 && suite.Selection[i-1] >= v) {
			return invalid("suite selection must be unique and sorted")
		}
	}
	for i, c := range suite.RequiredCases {
		if !text(c.Name) || len(c.Suite) > 4096 || len(c.Class) > 4096 || strings.ContainsAny(c.Suite+c.Class, "\x00\r\n") || (i > 0 && caseKey(suite.RequiredCases[i-1]) >= caseKey(c)) {
			return invalid("required cases must be unique sorted identities")
		}
	}
	if o.Environment.Role != "site" && o.Environment.Role != "tenant" && o.Environment.Role != "local" && o.Environment.Role != "unknown" {
		return invalid("unsupported observed environment role")
	}
	if len(o.Environment.Facts) == 0 || len(o.Environment.Facts) > 256 {
		return invalid("public environment observations required")
	}
	for i, f := range o.Environment.Facts {
		if !text(f.Name) || !text(f.Value) || (f.Evidence != "declared" && f.Evidence != "cluster-api") || (f.Phase != "before" && f.Phase != "after") || (i > 0 && factKey(o.Environment.Facts[i-1]) >= factKey(f)) {
			return invalid("invalid public environment observation; authenticated deployment binding is not supported")
		}
	}
	for _, out := range []Outcome{o.Execution, o.Cleanup} {
		if (out.Status != "completed" && out.Status != "failed" && out.Status != "canceled" && out.Status != "unknown" && out.Status != "not-run" && out.Status != "infrastructure-error") || out.ExitCode < 0 || out.ExitCode > 255 || (out.Status == "completed" && out.ExitCode != 0) {
			return invalid("invalid execution or cleanup outcome")
		}
	}
	if inventory {
		counts := []int{o.Summary.Total, o.Summary.Passed, o.Summary.Failed, o.Summary.Errors, o.Summary.Skipped}
		for _, count := range counts {
			if count < 0 || count > testresults.MaxCases {
				return invalid("invalid recomputed result counts")
			}
		}
		if o.Summary.Total != o.Summary.Passed+o.Summary.Failed+o.Summary.Errors+o.Summary.Skipped {
			return invalid("inconsistent result summary")
		}
		if len(o.Files) >= MaxFiles {
			return invalid("covered result inventory required")
		}
		junit := false
		var total int64
		for i, f := range o.Files {
			if !capturePath("files/"+f.Name) || !canonical.IsDigest(f.Digest) || f.Size < 0 || f.Size > MaxFileBytes || (i > 0 && o.Files[i-1].Name >= f.Name) {
				return invalid("invalid covered file inventory")
			}
			switch f.Role {
			case "junit":
				junit = true
			case "sbom", "environment", "fixture", "public-log":
			default:
				return invalid("unsupported covered file role")
			}
			total += f.Size
		}
		if !junit && (o.Execution.Status == "completed" || o.Execution.Status == "failed") {
			return invalid("executed tests require covered JUnit results")
		}
		if !junit && o.Summary != (testresults.Summary{}) {
			return invalid("absent JUnit requires zero result summary")
		}
		if o.Execution.Status == "completed" && o.Summary.Total == 0 {
			return invalid("completed execution requires nonempty result cases")
		}
		if total > MaxTotalBytes-MaxObservationBytes {
			return invalid("inventory requires JUnit and bounded covered bytes")
		}
	}
	return nil
}

// Decode accepts only this registered canonical schema and validated inventory.
func Decode(raw []byte) (Observation, error) {
	if len(raw) > MaxObservationBytes {
		return Observation{}, invalid("observation exceeds byte budget")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return Observation{}, err
	}
	var version struct {
		SchemaVersion string `json:"schemaVersion"`
	}
	if err := decodeVersion(raw, &version); err != nil {
		return Observation{}, err
	}
	if version.SchemaVersion != SchemaVersion {
		return Observation{}, invalid("unsupported observation schema %q", version.SchemaVersion)
	}
	var o Observation
	if err := canonical.UnmarshalExact(raw, &o); err != nil {
		return Observation{}, err
	}
	if err := validate(o, true); err != nil {
		return Observation{}, err
	}
	return o, nil
}
func (o Observation) Canonical() ([]byte, error) {
	o = clone(o)
	normalizeEmpty(&o)
	if err := validate(o, true); err != nil {
		return nil, err
	}
	raw, err := canonical.Marshal(o)
	if err != nil {
		return nil, err
	}
	if len(raw) > MaxObservationBytes {
		return nil, invalid("observation exceeds byte budget")
	}
	return raw, nil
}
func (o Observation) Digest() (string, error) {
	o = clone(o)
	normalizeEmpty(&o)
	if err := validate(o, true); err != nil {
		return "", err
	}
	if _, err := o.Canonical(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindTestObservation, o)
}
func (expected Expected) Validate() error {
	if !canonical.IsDigest(expected.ObservationDigest) {
		return invalid("independent observation digest required")
	}
	o := Observation{SchemaVersion: SchemaVersion, Inputs: expected.Inputs, Origin: Origin{Status: "unknown"}, Subjects: expected.Subjects, Suite: expected.Suite, Environment: Environment{Role: "unknown", Facts: []Fact{{Name: "binding", Value: "expected", Evidence: "declared", Phase: "before"}}}, Execution: Outcome{Status: "unknown"}, Cleanup: Outcome{Status: "unknown"}}
	o = clone(o)
	normalize(&o)
	return validate(o, false)
}
func (expected Expected) Check(o Observation) error {
	o = clone(o)
	normalizeEmpty(&o)
	if err := expected.Validate(); err != nil {
		return err
	}
	digest, err := o.Digest()
	if err != nil {
		return err
	}
	s := clone(Observation{Subjects: expected.Subjects, Suite: expected.Suite})
	normalize(&s)
	if digest != expected.ObservationDigest || o.Inputs != expected.Inputs || !reflect.DeepEqual(o.Subjects, s.Subjects) || !reflect.DeepEqual(o.Suite, s.Suite) {
		return invalid("observation does not match independently selected bindings")
	}
	return nil
}

// ValidateDescription admits a producer description before any external work.
// Derived inventories and summaries are accepted only through emission.
func ValidateDescription(ctx context.Context, description Observation) error {
	if err := captureContext(ctx); err != nil {
		return err
	}
	o := clone(description)
	normalize(&o)
	if err := validate(o, false); err != nil {
		return err
	}
	if len(o.Files) != 0 || o.Summary != (testresults.Summary{}) {
		return invalid("emission inventory and summary must be derived, not supplied")
	}
	return nil
}
