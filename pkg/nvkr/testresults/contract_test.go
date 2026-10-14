// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testresults

import (
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func standalone() Output {
	return Output{SchemaVersion: SchemaVersion,
		Producer:    ProducerIdentity{Name: "external-tool", Revision: Metadata{Status: "unknown"}},
		Suite:       SuiteSelection{Name: "cluster-checks", Selection: []string{"upstream full suite"}, Revision: Metadata{Status: "unknown"}},
		Reports:     []ReportReference{{Format: FormatJUnit, Profile: "tool-junit", Reference: "/not-opened/original.xml", Digest: "sha256:" + strings.Repeat("a", 64), ByteLength: 100}},
		Environment: []EnvironmentFact{{Scope: "test-cluster", Name: "version", Phase: "before", Metadata: Metadata{Status: "observed", Value: "v1.37.0"}}}}
}
func bound() Output {
	o := standalone()
	digest := "sha256:" + strings.Repeat("b", 64)
	o.Binding = &Binding{Inputs: &lifecycle.ExactInputDescription{ReleaseSubject: "registry.example.test/release@" + digest,
		PolicyDigest: digest, DefinitionDigest: digest, EnvironmentDigest: digest, PlanDigest: digest, ContentSubject: digest},
		Subjects: []SubjectBinding{{Output: "tool", Platform: "linux/arm64", Subject: sign.Subject{Ref: "registry.example.test/tool@" + digest, Digest: digest}}}}
	return o
}
func TestOutputDescriptionPermitsStandaloneOriginalFormats(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Output)
	}{
		{"junit only", func(*Output) {}},
		{"named versioned harness", func(o *Output) {
			o.Harness = &HarnessIdentity{Name: "hydrophone", Version: Metadata{Status: "declared", Value: "0.12.0"}}
		}},
		{"unknown harness version", func(o *Output) {
			o.Harness = &HarnessIdentity{Name: "custom-executor", Version: Metadata{Status: "unknown"}}
		}},
		{"declared suite revision", func(o *Output) { o.Suite.Revision = Metadata{Status: "declared", Value: "suite-revision-1"} }},
		{"ctrf only", func(o *Output) { o.Reports[0].Format = FormatCTRF; o.Reports[0].Profile = CTRFProfile }},
		{"both formats", func(o *Output) {
			r := o.Reports[0]
			r.Format = FormatCTRF
			r.Profile = CTRFProfile
			r.Reference = "/not-opened/original.json"
			o.Reports = append(o.Reports, r)
		}},
		{"declared JUnit to CTRF derivation", func(o *Output) {
			r := o.Reports[0]
			r.Format = FormatCTRF
			r.Profile = CTRFProfile
			r.Reference = "/not-opened/derived.json"
			r.DerivedFrom = o.Reports[0].Reference
			o.Reports = append(o.Reports, r)
		}},
		{"no environment recorded", func(o *Output) { o.Environment = nil }},
		{"unknown environment", func(o *Output) {
			o.Environment[0].Metadata = Metadata{Status: "unknown"}
			o.Environment[0].Phase = "unknown"
		}},
		{"unavailable environment", func(o *Output) { o.Environment[0].Metadata = Metadata{Status: "unavailable"} }},
		{"declared environment", func(o *Output) { o.Environment[0].Metadata.Status = "declared" }},
		{"zero byte description", func(o *Output) { o.Reports[0].ByteLength = 0 }},
		{"optional complete binding", func(o *Output) { *o = bound() }},
		{"only subject binding", func(o *Output) { *o = bound(); o.Binding.Inputs = nil }},
		{"only input binding", func(o *Output) { *o = bound(); o.Binding.Subjects = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := standalone()
			tc.edit(&o)
			if err := o.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
	// Success describes nonexistent file references; it establishes neither raw
	// byte integrity nor JUnit/CTRF validity and does not produce a passing result.
}
func TestOutputDescriptionRejectsAmbiguityAndUnsupportedDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Output)
		code e.Code
	}{
		{"unknown schema", func(o *Output) { o.SchemaVersion = "future" }, e.CodeVerifyFailed},
		{"missing producer", func(o *Output) { o.Producer.Name = "" }, e.CodeVerifyFailed},
		{"missing revision status", func(o *Output) { o.Producer.Revision = Metadata{} }, e.CodeVerifyFailed},
		{"unknown revision with value", func(o *Output) { o.Producer.Revision.Value = "fabricated" }, e.CodeVerifyFailed},
		{"missing suite revision status", func(o *Output) { o.Suite.Revision = Metadata{} }, e.CodeVerifyFailed},
		{"missing harness name", func(o *Output) { o.Harness = &HarnessIdentity{Version: Metadata{Status: "unknown"}} }, e.CodeVerifyFailed},
		{"missing harness version status", func(o *Output) { o.Harness = &HarnessIdentity{Name: "hydrophone"} }, e.CodeVerifyFailed},
		{"fabricated unknown harness version", func(o *Output) {
			o.Harness = &HarnessIdentity{Name: "hydrophone", Version: Metadata{Status: "unknown", Value: "0.12.0"}}
		}, e.CodeVerifyFailed},
		{"missing suite", func(o *Output) { o.Suite.Name = "" }, e.CodeVerifyFailed},
		{"missing selection", func(o *Output) { o.Suite.Selection = nil }, e.CodeVerifyFailed},
		{"duplicate selection", func(o *Output) { o.Suite.Selection = append(o.Suite.Selection, o.Suite.Selection[0]) }, e.CodeVerifyFailed},
		{"missing report", func(o *Output) { o.Reports = nil }, e.CodeVerifyFailed},
		{"unsupported format", func(o *Output) { o.Reports[0].Format = "sarif" }, e.CodeUnsupported},
		{"missing profile", func(o *Output) { o.Reports[0].Profile = "" }, e.CodeVerifyFailed},
		{"unsupported CTRF profile", func(o *Output) { o.Reports[0].Format = FormatCTRF; o.Reports[0].Profile = "future" }, e.CodeVerifyFailed},
		{"missing reference", func(o *Output) { o.Reports[0].Reference = "" }, e.CodeVerifyFailed},
		{"duplicate reference", func(o *Output) { o.Reports = append(o.Reports, o.Reports[0]) }, e.CodeVerifyFailed},
		{"missing derivation source", func(o *Output) { o.Reports[0].DerivedFrom = "/not-included/original.xml" }, e.CodeVerifyFailed},
		{"self derivation", func(o *Output) { o.Reports[0].DerivedFrom = o.Reports[0].Reference }, e.CodeVerifyFailed},
		{"chained derivation", func(o *Output) {
			r := o.Reports[0]
			r.Format = FormatCTRF
			r.Profile = CTRFProfile
			r.Reference = "derived.json"
			r.DerivedFrom = o.Reports[0].Reference
			o.Reports = append(o.Reports, r)
			next := r
			next.Reference = "second-derived.json"
			next.DerivedFrom = r.Reference
			o.Reports = append(o.Reports, next)
		}, e.CodeVerifyFailed},
		{"uppercase digest", func(o *Output) { o.Reports[0].Digest = "sha256:" + strings.Repeat("A", 64) }, e.CodeVerifyFailed},
		{"negative length", func(o *Output) { o.Reports[0].ByteLength = -1 }, e.CodeVerifyFailed},
		{"unsupported phase", func(o *Output) { o.Environment[0].Phase = "later" }, e.CodeVerifyFailed},
		{"missing fact scope", func(o *Output) { o.Environment[0].Scope = "" }, e.CodeVerifyFailed},
		{"duplicate fact", func(o *Output) { o.Environment = append(o.Environment, o.Environment[0]) }, e.CodeVerifyFailed},
		{"observed without value", func(o *Output) { o.Environment[0].Metadata.Value = "" }, e.CodeVerifyFailed},
		{"unavailable with value", func(o *Output) { o.Environment[0].Metadata.Status = "unavailable" }, e.CodeVerifyFailed},
		{"control text", func(o *Output) { o.Producer.Name = "producer\x1b" }, e.CodeVerifyFailed},
		{"empty binding", func(o *Output) { o.Binding = &Binding{} }, e.CodeVerifyFailed},
		{"partial input binding", func(o *Output) { *o = bound(); o.Binding.Inputs.PlanDigest = "" }, e.CodeVerifyFailed},
		{"mutable release", func(o *Output) {
			*o = bound()
			o.Binding.Inputs.ReleaseSubject = "registry.example.test/release:latest"
		}, e.CodeUnverifiedRef},
		{"subject digest mismatch", func(o *Output) {
			*o = bound()
			o.Binding.Subjects[0].Subject.Digest = "sha256:" + strings.Repeat("c", 64)
		}, e.CodeVerifyFailed},
		{"duplicate subject", func(o *Output) { *o = bound(); o.Binding.Subjects = append(o.Binding.Subjects, o.Binding.Subjects[0]) }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := standalone()
			tc.edit(&o)
			var coded *e.Error
			err := o.Validate()
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("error=%v want %s", err, tc.code)
			}
		})
	}
}
