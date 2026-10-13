// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

func TestPortableEvidenceCLIIndependentLocalLoop(t *testing.T) {
	source := t.TempDir()
	pin := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("fixture")))
	result := []byte(`<testsuite name="local" tests="1" failures="0"><testcase name="case"/></testsuite>`)
	if err := os.WriteFile(filepath.Join(source, "junit.xml"), result, 0o600); err != nil {
		t.Fatal(err)
	}
	description := testobservation.Observation{SchemaVersion: testobservation.SchemaVersion, Inputs: testobservation.Inputs{DefinitionDigest: pin, EnvironmentDigest: pin, PlanDigest: pin}, Origin: testobservation.Origin{Status: "unknown"}, Subjects: []testobservation.Subject{{Output: "fixture", Platform: "linux/amd64", BuildDigest: pin, ArtifactDigest: pin, PayloadDigest: pin}}, Suite: testobservation.Suite{Name: "local-fixture", HarnessRevision: strings.Repeat("a", 40), ImageDigest: pin, PolicyDigest: pin, Invocation: "local", Selection: []string{"case"}, RequiredCases: []testobservation.CaseID{{Suite: "local", Name: "case"}}}, Environment: testobservation.Environment{Role: "unknown", Facts: []testobservation.Fact{{Name: "runner", Value: "fixture", Evidence: "declared", Phase: "before"}}}, Execution: testobservation.Outcome{Status: "completed"}, Cleanup: testobservation.Outcome{Status: "completed"}}
	writeCanonical := func(name string, value any) string {
		t.Helper()
		raw, err := canonical.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(source, name)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	descriptionPath := writeCanonical("description.json", description)
	filesPath := writeCanonical("selection.json", []testobservation.FileInput{{Name: "junit.xml", Role: "junit", ExpectedDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(result))}})
	out := filepath.Join(t.TempDir(), "bundle")
	app := newApp()
	var output bytes.Buffer
	app.Writer = &output
	app.ErrWriter = &output
	if err := app.RunContext(t.Context(), []string{"nvkr", "evidence", "emit", "--root", source, "--description", descriptionPath, "--files", filesPath, "--out", out}); err != nil {
		t.Fatal(err)
	}
	bundle, err := testobservation.Open(t.Context(), out)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	expected := testobservation.Expected{ObservationDigest: bundle.Digest(), Inputs: description.Inputs, Subjects: description.Subjects, Suite: description.Suite}
	expectedPath := writeCanonical("expected.json", expected)
	output.Reset()
	if err := app.RunContext(t.Context(), []string{"nvkr", "evidence", "verify-local", "--root", out, "--expected", expectedPath, "--json"}); err != nil {
		t.Fatal(err)
	}
	var verified testobservation.Verification
	if err := canonical.UnmarshalExact(output.Bytes(), &verified); err != nil {
		t.Fatal(err)
	}
	if !verified.SelfConsistent || !verified.Passing || verified.SignerAuthenticated {
		t.Fatalf("unqualified verification escalated: %+v", verified)
	}
	if err := os.WriteFile(filepath.Join(out, "files", "junit.xml"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.RunContext(t.Context(), []string{"nvkr", "evidence", "verify-local", "--root", out, "--expected", expectedPath, "--json"}); err == nil {
		t.Fatal("tampered result accepted")
	}
}
