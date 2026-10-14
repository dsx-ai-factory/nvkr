// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testresults

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func requireContractError(t *testing.T, err error, code e.Code, message string) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code || !strings.Contains(err.Error(), message) {
		t.Fatalf("error=%v want=%s containing %q", err, code, message)
	}
}
func TestWireGoldenAndStrictDecoding(t *testing.T) {
	output := bound()
	output.Environment = nil
	raw, err := Encode(output)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	golden := `{"binding":{"inputs":{"contentSubject":"DIGEST","definitionDigest":"DIGEST","environmentDigest":"DIGEST","planDigest":"DIGEST","policyDigest":"DIGEST","releaseSubject":"registry.example.test/release@DIGEST"},"subjects":[{"output":"tool","platform":"linux/arm64","subject":{"digest":"DIGEST","ref":"registry.example.test/tool@DIGEST"}}]},"captureStatus":"complete","producer":{"name":"external-tool","revision":{"status":"unknown"}},"reports":[{"byteLength":100,"digest":"sha256:AAAAAAAA","format":"junit-xml","profile":"tool-junit","reference":"/not-opened/original.xml"}],"schemaVersion":"nvkr.dev/test-results-output/v1","suite":{"name":"cluster-checks","revision":{"status":"unknown"},"selection":["upstream full suite"]}}`
	golden = strings.ReplaceAll(strings.ReplaceAll(golden, "DIGEST", digest), "AAAAAAAA", strings.Repeat("a", 64))
	if string(raw) != golden {
		t.Fatalf("wire differs\ngot: %s\nwant:%s", raw, golden)
	}
	if _, err = Decode(raw); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		raw     []byte
		message string
	}{
		{"unknown verdict", bytes.Replace(raw, []byte(`"producer":`), []byte(`"passed":true,"producer":`), 1), "decoding"},
		{"Go default subject keys", bytes.Replace(raw, []byte(`"ref":`), []byte(`"Ref":`), 1), "decoding"},
		{"insignificant whitespace", append([]byte(" "), raw...), "decoding"},
		{"empty", nil, "byte length"},
		{"over budget", bytes.Repeat([]byte("x"), MaxDocumentBytes+1), "byte length"},
	} {
		t.Run(tc.name, func(t *testing.T) { _, err := Decode(tc.raw); requireContractError(t, err, e.CodeUsage, tc.message) })
	}
}
func TestEncodeNormalizesWithoutMutatingCaller(t *testing.T) {
	a := bound()
	a.Suite.Selection = []string{"z", "a"}
	report := a.Reports[0]
	report.Reference = "another.xml"
	a.Reports = append(a.Reports, report)
	fact := a.Environment[0]
	fact.Name = "architecture"
	fact.Phase = "after"
	a.Environment = append(a.Environment, fact)
	subject := a.Binding.Subjects[0]
	subject.Output = "another"
	a.Binding.Subjects = append(a.Binding.Subjects, subject)
	b := a
	b.Suite.Selection = []string{"a", "z"}
	b.Reports = []ReportReference{report, a.Reports[0]}
	b.Environment = []EnvironmentFact{fact, a.Environment[0]}
	binding := *a.Binding
	b.Binding = &binding
	b.Binding.Subjects = []SubjectBinding{subject, a.Binding.Subjects[0]}
	first, err := Encode(a)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Encode(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("ordering affected canonical bytes")
	}
	if a.Suite.Selection[0] != "z" || a.Reports[0].Reference == "another.xml" || a.Binding.Subjects[0].Output != "tool" {
		t.Fatal("Encode mutated caller collections")
	}
	raw, err := canonical.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Decode(raw)
	requireContractError(t, err, e.CodeUsage, "normalized ordering")
	a = standalone()
	a.Environment = nil
	first, err = Encode(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Environment = []EnvironmentFact{}
	second, err = Encode(a)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("empty environment encoding differs")
	}
	a.CaptureStatus = CapturePartial
	a.Reports = nil
	first, err = Encode(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Reports = []ReportReference{}
	second, err = Encode(a)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("empty partial report encoding differs")
	}
	if _, err = Decode(first); err != nil {
		t.Fatal(err)
	}
}
func TestCapturePhasesAndStatuses(t *testing.T) {
	for _, phase := range []string{"before", "during", "after", "unknown"} {
		o := standalone()
		o.Environment[0].Phase = phase
		if err := o.Validate(); err != nil {
			t.Fatalf("phase %s: %v", phase, err)
		}
	}
	for _, tc := range []struct {
		name    string
		edit    func(*Output)
		code    e.Code
		message string
	}{
		{"missing capture", func(o *Output) { o.CaptureStatus = "" }, e.CodeUsage, "capture status is required"},
		{"unsupported capture", func(o *Output) { o.CaptureStatus = "passed" }, e.CodeUnsupported, "unsupported capture status"},
		{"unsupported metadata", func(o *Output) { o.Producer.Revision.Status = "verified" }, e.CodeUnsupported, "unsupported metadata status"},
		{"missing format", func(o *Output) { o.Reports[0].Format = "" }, e.CodeUsage, "report format is required"},
		{"missing schema", func(o *Output) { o.SchemaVersion = "" }, e.CodeUsage, "schema is required"},
		{"inconsistent zero bytes", func(o *Output) { o.Reports[0].ByteLength = 0 }, e.CodeUsage, "empty sha256"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := standalone()
			tc.edit(&o)
			requireContractError(t, o.Validate(), tc.code, tc.message)
		})
	}
}
func TestEveryInputIdentityDelegatesToOwner(t *testing.T) {
	for _, field := range []string{"policy", "definition", "environment", "plan", "content"} {
		t.Run(field, func(t *testing.T) {
			o := bound()
			inputs := o.Binding.Inputs
			switch field {
			case "policy":
				inputs.PolicyDigest = ""
			case "definition":
				inputs.DefinitionDigest = ""
			case "environment":
				inputs.EnvironmentDigest = ""
			case "plan":
				inputs.PlanDigest = ""
			case "content":
				inputs.ContentSubject = ""
			}
			err := o.Validate()
			requireContractError(t, err, e.CodeUsage, "input binding")
			requireContractError(t, errors.Unwrap(err), e.CodeVerifyFailed, "input")
		})
	}
	for _, ref := range []string{"https://registry.example.test/release@sha256:" + strings.Repeat("b", 64), "registry.example.test/release:latest"} {
		o := bound()
		o.Binding.Inputs.ReleaseSubject = ref
		requireContractError(t, o.Validate(), e.CodeUsage, "input binding")
	}
}
func TestSubjectDescriptionRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(*Output)
		message string
	}{
		{"missing output", func(o *Output) { o.Binding.Subjects[0].Output = "" }, "unique named output"},
		{"invalid platform", func(o *Output) { o.Binding.Subjects[0].Platform = "not a platform" }, "subject platform"},
		{"non-native platform", func(o *Output) { o.Binding.Subjects[0].Platform = "windows/amd64" }, "subject platform"},
		{"mixed platforms", func(o *Output) {
			s := o.Binding.Subjects[0]
			s.Platform = ""
			o.Binding.Subjects = append(o.Binding.Subjects, s)
		}, "mix absent and native"},
		{"blank reference", func(o *Output) { o.Binding.Subjects[0].Subject.Ref = "" }, "canonical OCI locator"},
		{"scheme reference", func(o *Output) { o.Binding.Subjects[0].Subject.Ref = "https://" + o.Binding.Subjects[0].Subject.Ref }, "canonical OCI locator"},
		{"implicit repository", func(o *Output) { o.Binding.Subjects[0].Subject.Ref = "tool@" + o.Binding.Subjects[0].Subject.Digest }, "subject reference"},
		{"tag reference", func(o *Output) { o.Binding.Subjects[0].Subject.Ref = "registry.example.test/tool:latest" }, "subject binding"},
		{"invalid digest", func(o *Output) { o.Binding.Subjects[0].Subject.Digest = "" }, "subject binding"},
		{"multiple separator", func(o *Output) {
			o.Binding.Subjects[0].Subject.Ref = "registry.example.test/tool@@" + o.Binding.Subjects[0].Subject.Digest
		}, "subject binding"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := bound()
			tc.edit(&o)
			requireContractError(t, o.Validate(), e.CodeUsage, tc.message)
		})
	}
}
func TestTextFieldsRejectWhitespaceAndControls(t *testing.T) {
	for _, field := range []struct {
		name    string
		edit    func(*Output, string)
		message string
	}{
		{"producer", func(o *Output, v string) { o.Producer.Name = v }, "named producer"},
		{"suite", func(o *Output, v string) { o.Suite.Name = v }, "named producer"},
		{"harness", func(o *Output, v string) { o.Harness = &HarnessIdentity{Name: v, Version: Metadata{Status: "unknown"}} }, "harness requires"},
		{"profile", func(o *Output, v string) { o.Reports[0].Profile = v }, "profile requires"},
		{"selection", func(o *Output, v string) { o.Suite.Selection[0] = v }, "selection must contain"},
		{"reference", func(o *Output, v string) { o.Reports[0].Reference = v }, "report requires"},
		{"scope", func(o *Output, v string) { o.Environment[0].Scope = v }, "unique scoped"},
		{"fact name", func(o *Output, v string) { o.Environment[0].Name = v }, "unique scoped"},
		{"metadata", func(o *Output, v string) { o.Environment[0].Metadata.Value = v }, "requires a value"},
		{"output", func(o *Output, v string) { *o = bound(); o.Binding.Subjects[0].Output = v }, "unique named output"},
		{"platform", func(o *Output, v string) { *o = bound(); o.Binding.Subjects[0].Platform = v }, "unique named output"},
		{"subject reference", func(o *Output, v string) { *o = bound(); o.Binding.Subjects[0].Subject.Ref = v }, "canonical OCI locator"},
	} {
		for _, value := range []string{" value", "value ", "value\x1b"} {
			t.Run(field.name+"/"+value, func(t *testing.T) {
				o := standalone()
				field.edit(&o, value)
				requireContractError(t, o.Validate(), e.CodeUsage, field.message)
			})
		}
	}
}
func TestCollectionLimits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		edit    func(*Output)
		message string
	}{
		{"selections", func(o *Output) { o.Suite.Selection = make([]string, MaxSelections+1) }, "collection count"},
		{"reports", func(o *Output) { o.Reports = make([]ReportReference, MaxReports+1) }, "collection count"},
		{"facts", func(o *Output) { o.Environment = make([]EnvironmentFact, MaxEnvironmentFacts+1) }, "collection count"},
		{"subjects", func(o *Output) { *o = bound(); o.Binding.Subjects = make([]SubjectBinding, MaxSubjects+1) }, "subject count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := standalone()
			tc.edit(&o)
			requireContractError(t, o.Validate(), e.CodeUsage, tc.message)
		})
	}
}
