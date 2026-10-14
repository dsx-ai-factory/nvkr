// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package testresults describes original external test outputs. It supplies no
// producer implementation, report parser, collector or authentication authority.
package testresults

import (
	"context"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

const SchemaVersion = "nvkr.dev/test-results-output/v1"
const FormatJUnit = "junit-xml"
const FormatCTRF = "ctrf-json"

// CTRFProfile names working draft 0.2.0 at ctrf-io/ctrf commit
// e325eb33c7662a208bfa4d093a41cc55439d6e6a. It declares a profile; this package
// does not validate JSON against the upstream schema or interpret test results.
const CTRFProfile = "ctrf-0.2.0"

// Producer implementations own configuration and execution. An error may
// accompany partial diagnostic output; neither output nor Validate implies pass.
type Producer interface {
	Produce(context.Context) (Output, error)
}

// Output is a descriptive handoff, not a receipt or a new lifecycle input.
// An absent Binding permits standalone producers without invented NVKR inputs.
type Output struct {
	SchemaVersion string            `json:"schemaVersion"`
	Producer      ProducerIdentity  `json:"producer"`
	Suite         SuiteSelection    `json:"suite"`
	Harness       *HarnessIdentity  `json:"harness,omitempty"`
	Reports       []ReportReference `json:"reports"`
	Environment   []EnvironmentFact `json:"environment"`
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
	Output   string       `json:"output"`
	Platform string       `json:"platform,omitempty"`
	Subject  sign.Subject `json:"subject"`
}

// Validate checks only the handoff description. It performs no I/O, hashing,
// format parsing, count reconciliation, coverage evaluation or passing verdict.
func (o Output) Validate() error {
	if o.SchemaVersion != SchemaVersion || !text(o.Producer.Name) || !text(o.Suite.Name) {
		return invalid("test output requires its schema and named producer and suite")
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
	if len(o.Suite.Selection) == 0 || len(o.Reports) == 0 {
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
		if report.Format != FormatJUnit && report.Format != FormatCTRF {
			return e.New(e.CodeUnsupported, "unsupported test report format")
		}
		if !text(report.Profile) || report.Format == FormatCTRF && report.Profile != CTRFProfile {
			return invalid("test report requires an admitted profile")
		}
		if !text(report.Reference) || !canonical.IsDigest(report.Digest) || report.ByteLength < 0 {
			return invalid("original report requires a unique reference, exact sha256 and nonnegative byte length")
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
		if !included || report.DerivedFrom == report.Reference {
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
			return invalid("unsupported environment observation phase")
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
	if inputs := o.Binding.Inputs; inputs != nil {
		for _, digest := range []string{inputs.PolicyDigest, inputs.DefinitionDigest, inputs.EnvironmentDigest, inputs.PlanDigest, inputs.ContentSubject} {
			if !canonical.IsDigest(digest) {
				return invalid("test input binding requires complete existing input identities")
			}
		}
		if err := delivery.ValidatePublicationReference(inputs.ReleaseSubject); err != nil {
			return err
		}
	}
	subjects := map[[2]string]bool{}
	for _, subject := range o.Binding.Subjects {
		key := [2]string{subject.Output, subject.Platform}
		if !text(subject.Output) || subject.Platform != "" && !text(subject.Platform) || subjects[key] {
			return invalid("test subjects require unique named output and platform descriptions")
		}
		if err := subject.Subject.Validate(); err != nil {
			return err
		}
		if err := delivery.ValidatePublicationReference(subject.Subject.Ref); err != nil {
			return err
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
		return invalid("metadata requires explicit declared, observed, unknown or unavailable status")
	}
	return nil
}

func text(value string) bool {
	return safetext.Valid(value, 4096, false) && strings.TrimSpace(value) == value
}
func invalid(message string) error { return e.New(e.CodeVerifyFailed, "%s", message) }
