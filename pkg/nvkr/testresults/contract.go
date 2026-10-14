// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package testresults describes original external test outputs. It supplies no
// producer implementation, report parser, collector or authentication authority.
package testresults

import (
	"bytes"
	"context"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
)

const SchemaVersion = "nvkr.dev/test-results-output/v1"

// Limits bound descriptive handoffs, not report payloads or execution coverage.
const MaxDocumentBytes = 16 << 20
const MaxSelections = 1024
const MaxReports = 1024
const MaxEnvironmentFacts = 10000
const MaxSubjects = 1024
const CaptureComplete = "complete"
const CapturePartial = "partial"
const emptyBlobDigest = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

const FormatJUnit = "junit-xml"
const FormatCTRF = "ctrf-json"

// CTRFProfile names working draft 0.2.0 at ctrf-io/ctrf commit
// e325eb33c7662a208bfa4d093a41cc55439d6e6a. It declares a profile; this package
// does not validate JSON against the upstream schema or interpret test results.
const CTRFProfile = "ctrf-0.2.0"

// Producer implementations outside the qualified verifier own configuration and
// execution. On error, any retained output must use CapturePartial. Neither
// output, CaptureComplete nor Validate implies that tests passed.
type Producer interface {
	Produce(context.Context) (Output, error)
}

// Output is a descriptive handoff, not a receipt or a new lifecycle input.
// An absent Binding permits standalone producers without invented NVKR inputs.
type Output struct {
	// CaptureStatus records capture completeness, never a test verdict. Partial
	// output may retain reports, facts and bindings after producer failure.
	CaptureStatus string            `json:"captureStatus"`
	SchemaVersion string            `json:"schemaVersion"`
	Producer      ProducerIdentity  `json:"producer"`
	Suite         SuiteSelection    `json:"suite"`
	Harness       *HarnessIdentity  `json:"harness,omitempty"`
	Reports       []ReportReference `json:"reports,omitempty"`
	Environment   []EnvironmentFact `json:"environment,omitempty"`
	Binding       *Binding          `json:"binding,omitempty"`
}

type ProducerIdentity struct {
	Name     string   `json:"name"`
	Revision Metadata `json:"revision"`
}

type SuiteSelection struct {
	Name      string   `json:"name"`
	Selection []string `json:"selection"`
	Revision  Metadata `json:"revision"`
}

// HarnessIdentity describes the selected executor separately from the producer
// adapter and suite. An absent harness supplies no executor identity claim.
type HarnessIdentity struct {
	Name    string   `json:"name"`
	Version Metadata `json:"version"`
}

// ReportReference preserves an original report's location and declared byte
// integrity. Reference is opaque: future collectors own location admission.
// DerivedFrom optionally names an included original report; it describes a
// one-step derivation and does not prove that conversion ran.
// JUnit Profile names the producer's XML dialect, not a universal JUnit schema.
type ReportReference struct {
	Format      string `json:"format"`
	Profile     string `json:"profile"`
	Reference   string `json:"reference"`
	Digest      string `json:"digest"`
	ByteLength  int64  `json:"byteLength"`
	DerivedFrom string `json:"derivedFrom,omitempty"`
}

// Metadata distinguishes a producer declaration or observation from missing
// information. Observed is descriptive and never independently authenticated.
type Metadata struct {
	Status string `json:"status"`
	Value  string `json:"value,omitempty"`
}

// EnvironmentFact scopes one fact and its observation phase. An empty fact set
// describes no environment information; unavailable facts can be explicit rows.
type EnvironmentFact struct {
	Scope    string   `json:"scope"`
	Name     string   `json:"name"`
	Phase    string   `json:"phase"`
	Metadata Metadata `json:"metadata"`
}

// Binding reuses existing owners' descriptions without issuing their receipts.
// Inputs and Subjects are independently optional; a present binding must include
// at least one of them. Native platform remains optional descriptive metadata.
type Binding struct {
	Inputs   *lifecycle.ExactInputDescription `json:"inputs,omitempty"`
	Subjects []SubjectBinding                 `json:"subjects,omitempty"`
}

type SubjectBinding struct {
	Output   string             `json:"output"`
	Platform string             `json:"platform,omitempty"`
	Subject  SubjectDescription `json:"subject"`
}

// SubjectDescription owns this handoff's stable wire keys. Converting to the
// signing owner's shape only validates descriptions; it grants no signing proof.
type SubjectDescription struct {
	Ref    string `json:"ref"`
	Digest string `json:"digest"`
}

// Validate checks only the handoff description. It performs no I/O, hashing,
// format parsing, count reconciliation, coverage evaluation or passing verdict.
func (o Output) Validate() error {
	if o.SchemaVersion == "" {
		return invalid("schema is required")
	}
	if o.SchemaVersion != SchemaVersion {
		return unsupported("schema")
	}
	if o.CaptureStatus == "" {
		return invalid("capture status is required")
	}
	if o.CaptureStatus != CaptureComplete && o.CaptureStatus != CapturePartial {
		return unsupported("capture status")
	}
	if !text(o.Producer.Name) || !text(o.Suite.Name) {
		return invalid("named producer and suite are required")
	}
	if len(o.Suite.Selection) > MaxSelections || len(o.Reports) > MaxReports || len(o.Environment) > MaxEnvironmentFacts {
		return invalid("collection count exceeds its limit")
	}
	if err := o.Producer.Revision.validate(); err != nil {
		return err
	}
	if err := o.Suite.Revision.validate(); err != nil {
		return err
	}
	if o.Harness != nil {
		if !text(o.Harness.Name) {
			return invalid("test harness requires an explicit name")
		}
		if err := o.Harness.Version.validate(); err != nil {
			return err
		}
	}
	if len(o.Suite.Selection) == 0 || o.CaptureStatus == CaptureComplete && len(o.Reports) == 0 {
		return invalid("test output requires explicit selection and original report references")
	}
	selection := map[string]bool{}
	for _, item := range o.Suite.Selection {
		if !text(item) || selection[item] {
			return invalid("test selection must contain unique nonempty descriptions")
		}
		selection[item] = true
	}
	reports := map[string]ReportReference{}
	for _, report := range o.Reports {
		if report.Format == "" {
			return invalid("report format is required")
		}
		if report.Format != FormatJUnit && report.Format != FormatCTRF {
			return unsupported("report format")
		}
		if !text(report.Profile) {
			return invalid("report profile requires canonical text")
		}
		if report.Format == FormatCTRF && report.Profile != CTRFProfile {
			return unsupported("CTRF profile")
		}
		if !text(report.Reference) || !canonical.IsDigest(report.Digest) || report.ByteLength < 0 {
			return invalid("report requires canonical reference, exact sha256 and nonnegative byte length")
		}
		if report.ByteLength == 0 && report.Digest != emptyBlobDigest {
			return invalid("zero byte report requires the empty sha256 digest")
		}
		if _, duplicate := reports[report.Reference]; duplicate {
			return invalid("original report references must be unique")
		}
		reports[report.Reference] = report
	}
	for _, report := range o.Reports {
		if report.DerivedFrom == "" {
			continue
		}
		source, included := reports[report.DerivedFrom]
		if report.DerivedFrom == report.Reference {
			return invalid("report cannot derive from itself")
		}
		if !included {
			return invalid("derived report must reference another included original report")
		}
		if source.DerivedFrom != "" {
			return invalid("report derivation must be one step from an original report")
		}
	}
	facts := map[[3]string]bool{}
	for _, fact := range o.Environment {
		key := [3]string{fact.Scope, fact.Name, fact.Phase}
		if !text(fact.Scope) || !text(fact.Name) || facts[key] {
			return invalid("environment facts require unique scoped names and phases")
		}
		if fact.Phase != "before" && fact.Phase != "during" && fact.Phase != "after" && fact.Phase != "unknown" {
			if fact.Phase == "" {
				return invalid("environment phase is required")
			}
			return unsupported("environment phase")
		}
		if err := fact.Metadata.validate(); err != nil {
			return err
		}
		facts[key] = true
	}
	if o.Binding == nil {
		return nil
	}
	if o.Binding.Inputs == nil && len(o.Binding.Subjects) == 0 {
		return invalid("empty test output binding")
	}
	if len(o.Binding.Subjects) > MaxSubjects {
		return invalid("subject count exceeds its limit")
	}
	if inputs := o.Binding.Inputs; inputs != nil {
		if err := inputs.Validate(); err != nil {
			return e.Wrap(e.CodeUsage, err, "test output input binding")
		}
	}
	emptyPlatforms := map[string]bool{}
	nativePlatforms := map[string]bool{}
	subjects := map[[2]string]bool{}
	for _, subject := range o.Binding.Subjects {
		key := [2]string{subject.Output, subject.Platform}
		if !text(subject.Output) || subject.Platform != "" && !text(subject.Platform) || subjects[key] {
			return invalid("test subjects require unique named output and platform descriptions")
		}
		if subject.Platform != "" {
			if err := target.ValidatePlatform(subject.Platform); err != nil {
				return e.Wrap(e.CodeUsage, err, "test output subject platform")
			}
			nativePlatforms[subject.Output] = true
		} else {
			emptyPlatforms[subject.Output] = true
		}
		if emptyPlatforms[subject.Output] && nativePlatforms[subject.Output] {
			return invalid("subject output cannot mix absent and native platforms")
		}
		if !text(subject.Subject.Ref) || strings.Contains(subject.Subject.Ref, "://") {
			return invalid("subject reference requires canonical OCI locator")
		}
		if err := (sign.Subject{Ref: subject.Subject.Ref, Digest: subject.Subject.Digest}).Validate(); err != nil {
			return e.Wrap(e.CodeUsage, err, "test output subject binding")
		}
		if err := delivery.ValidatePublicationReference(subject.Subject.Ref); err != nil {
			return e.Wrap(e.CodeUsage, err, "test output subject reference")
		}
		subjects[key] = true
	}
	return nil
}

func (m Metadata) validate() error {
	switch m.Status {
	case "declared", "observed":
		if !text(m.Value) {
			return invalid("declared or observed metadata requires a value")
		}
	case "unknown", "unavailable":
		if m.Value != "" {
			return invalid("unknown or unavailable metadata cannot claim a value")
		}
	default:
		if m.Status == "" {
			return invalid("metadata status is required")
		}
		return unsupported("metadata status")
	}
	return nil
}

func text(value string) bool {
	return safetext.Valid(value, 4096, false) && strings.TrimSpace(value) == value
}
func invalid(message string) error { return e.New(e.CodeUsage, "test output: %s", message) }

func unsupported(field string) error {
	return e.New(e.CodeUnsupported, "test output: unsupported %s", field)
}

// Encode validates the description, sorts cloned collections and emits canonical
// JSON. Neither this encoding nor capture completeness interprets test outcomes.
func Encode(output Output) ([]byte, error) {
	if err := output.Validate(); err != nil {
		return nil, err
	}
	output.Suite.Selection = append([]string(nil), output.Suite.Selection...)
	sort.Strings(output.Suite.Selection)
	output.Reports = append([]ReportReference(nil), output.Reports...)
	sort.Slice(output.Reports, func(i, j int) bool { return output.Reports[i].Reference < output.Reports[j].Reference })
	output.Environment = append([]EnvironmentFact(nil), output.Environment...)
	sort.Slice(output.Environment, func(i, j int) bool {
		a, b := output.Environment[i], output.Environment[j]
		if a.Phase != b.Phase {
			return a.Phase < b.Phase
		}
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		return a.Name < b.Name
	})
	if output.Binding != nil {
		binding := *output.Binding
		binding.Subjects = append([]SubjectBinding(nil), binding.Subjects...)
		sort.Slice(binding.Subjects, func(i, j int) bool {
			a, b := binding.Subjects[i], binding.Subjects[j]
			if a.Output != b.Output {
				return a.Output < b.Output
			}
			return a.Platform < b.Platform
		})
		output.Binding = &binding
	}
	raw, err := canonical.Marshal(output)
	if err != nil {
		return nil, e.Wrap(e.CodeUsage, err, "test output encoding")
	}
	if len(raw) > MaxDocumentBytes {
		return nil, invalid("encoded document exceeds byte limit")
	}
	return raw, nil
}

// Decode accepts only the bounded, normalized canonical wire representation.
// Unknown fields, duplicate keys and alternative encodings fail before handoff.
func Decode(raw []byte) (Output, error) {
	if len(raw) == 0 || len(raw) > MaxDocumentBytes {
		return Output{}, invalid("document byte length exceeds bounds")
	}
	var output Output
	if err := canonical.UnmarshalExact(raw, &output); err != nil {
		return Output{}, e.Wrap(e.CodeUsage, err, "test output decoding")
	}
	normalized, err := Encode(output)
	if err != nil {
		return Output{}, err
	}
	if !bytes.Equal(raw, normalized) {
		return Output{}, invalid("collections must use normalized ordering")
	}
	return output, nil
}
