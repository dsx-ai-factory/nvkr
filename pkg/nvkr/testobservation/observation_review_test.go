// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func TestObservationEmptyArrayCanonicalIdentity(t *testing.T) {
	o := observationFixture()
	o.Execution = Outcome{Status: "not-run"}
	o.Cleanup = Outcome{Status: "not-run"}
	o.Suite.RequiredCases = nil
	o.Files = nil
	nilBytes, err := o.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	nilDigest, err := o.Digest()
	if err != nil {
		t.Fatal(err)
	}
	o.Suite.RequiredCases = []CaseID{}
	o.Files = []File{}
	emptyBytes, err := o.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	emptyDigest, err := o.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(nilBytes, emptyBytes) || nilDigest != emptyDigest {
		t.Fatal("empty descriptions acquired different identities")
	}
	if _, err := Decode(emptyBytes); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"requiredCases", "files"} {
		raw := []byte(strings.Replace(string(emptyBytes), `"`+field+`":[]`, `"`+field+`":null`, 1))
		_, err := Decode(raw)
		requireCode(t, err, e.CodeVerifyFailed)
	}
}
func TestObservationSchemaRejectsInvalidDescriptions(t *testing.T) {
	base := observationFixture()
	base.Execution = Outcome{Status: "not-run"}
	base.Cleanup = Outcome{Status: "not-run"}
	normalize(&base)
	for _, tc := range []struct {
		name string
		edit func(*Observation)
	}{
		{"unknown schema", func(o *Observation) { o.SchemaVersion = "future" }},
		{"unknown origin", func(o *Observation) { o.Origin.Status = "authenticated" }},
		{"unknown origin with run", func(o *Observation) { o.Origin.Run = "unproved" }},
		{"declared origin incomplete", func(o *Observation) { o.Origin = Origin{Status: "declared"} }},
		{"duplicate subjects", func(o *Observation) { o.Subjects = append(o.Subjects, o.Subjects[0]) }},
		{"duplicate selection", func(o *Observation) { o.Suite.Selection = append(o.Suite.Selection, o.Suite.Selection[0]) }},
		{"duplicate required cases", func(o *Observation) { o.Suite.RequiredCases = append(o.Suite.RequiredCases, o.Suite.RequiredCases[0]) }},
		{"duplicate facts", func(o *Observation) { o.Environment.Facts = append(o.Environment.Facts, o.Environment.Facts[0]) }},
		{"unsupported role", func(o *Observation) { o.Environment.Role = "production" }},
		{"unsupported fact evidence", func(o *Observation) { o.Environment.Facts[0].Evidence = "authenticated" }},
		{"unsupported fact phase", func(o *Observation) { o.Environment.Facts[0].Phase = "during" }},
		{"invalid execution", func(o *Observation) { o.Execution.Status = "passed" }},
		{"invalid cleanup", func(o *Observation) { o.Cleanup.ExitCode = 256 }},
		{"completed nonzero", func(o *Observation) { o.Execution = Outcome{Status: "completed", ExitCode: 1} }},
		{"unsafe file path", func(o *Observation) { o.Files = []File{{Name: "../private", Role: "fixture", Digest: rawPin("file")}} }},
		{"unsupported file role", func(o *Observation) { o.Files = []File{{Name: "file", Role: "secret", Digest: rawPin("file")}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := clone(base)
			tc.edit(&o)
			raw, err := canonical.Marshal(o)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Decode(raw)
			requireCode(t, err, e.CodeVerifyFailed)
		})
	}
	declared := clone(base)
	declared.Origin = Origin{Status: "declared", Repository: "https://github.com/example/project", Commit: strings.Repeat("a", 40), Run: "123", Workflow: "https://github.com/example/project/actions/workflows/build.yml", Attempt: 1}
	raw, err := declared.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(raw); err != nil {
		t.Fatal(err)
	}
}
func TestLocatorStrictAdmissionBeforeExternalWork(t *testing.T) {
	valid := Locator{SchemaVersion: LocatorSchemaVersion, Reference: "registry.example.test/observations@" + rawPin("manifest"), ObservationDigest: rawPin("observation")}
	for _, tc := range []struct {
		name string
		edit func(*Locator)
		code e.Code
	}{
		{"unknown schema", func(l *Locator) { l.SchemaVersion = "future" }, e.CodeVerifyFailed},
		{"bad observation digest", func(l *Locator) { l.ObservationDigest = "invalid" }, e.CodeVerifyFailed},
		{"tagged reference", func(l *Locator) { l.Reference = "registry.example.test/observations:latest" }, e.CodeUnpinnedInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := valid
			tc.edit(&l)
			raw, err := canonical.Marshal(l)
			if err != nil {
				t.Fatal(err)
			}
			_, err = DecodeLocator(raw)
			requireCode(t, err, tc.code)
			receipt, err := VerifyLocator(t.Context(), l, Expected{}, RegistryOptions{})
			if receipt != nil {
				t.Fatal("invalid locator issued receipt")
			}
			requireCode(t, err, tc.code)
		})
	}
	raw, err := canonical.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{append(raw, '\n'), append([]byte(`{"unknown":true,`), raw[1:]...), bytes.Repeat([]byte("x"), 16385)} {
		_, err := DecodeLocator(bad)
		requireCode(t, err, e.CodeVerifyFailed)
	}
	expected := Expected{ObservationDigest: rawPin("observation"), Inputs: observationFixture().Inputs, Subjects: observationFixture().Subjects, Suite: observationFixture().Suite}
	wrong := expected
	wrong.ObservationDigest = rawPin("other")
	rejected, err := VerifyLocator(t.Context(), valid, wrong, RegistryOptions{})
	if rejected != nil {
		t.Fatal("mismatched locator issued receipt")
	}
	requireCode(t, err, e.CodeVerifyFailed)
	options := RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}, Credential: sign.Credential{Registry: "other", Username: "user", Password: "private"}}
	receipt, err := VerifyOCI(t.Context(), valid.Reference, expected, options)
	if receipt != nil {
		t.Fatal("invalid credentials issued receipt")
	}
	requireCode(t, err, e.CodeUsage)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = VerifyOCI(canceled, valid.Reference, expected, options)
	requireCode(t, err, e.CodeTimeout)
	_, err = VerifyLocator(canceled, valid, expected, options)
	requireCode(t, err, e.CodeTimeout)
}
