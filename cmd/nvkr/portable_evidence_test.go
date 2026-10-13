// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
	"github.com/urfave/cli/v2"
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
	locator := testobservation.Locator{SchemaVersion: testobservation.LocatorSchemaVersion, Reference: "example.com/evidence@" + pin, ObservationDigest: "sha256:" + strings.Repeat("b", 64)}
	locatorPath := writeCanonical("locator.json", locator)
	if err := app.RunContext(t.Context(), []string{"nvkr", "evidence", "verify", "--locator", locatorPath, "--expected", expectedPath, "--cosign", "missing-binary", "--cosign-digest", pin, "--identity", "fixture", "--issuer", "fixture", "--anonymous"}); err != nil {
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
			t.Fatalf("locator mismatch = %v, want %s before signer admission", err, e.CodeVerifyFailed)
		}
	} else {
		t.Fatal("mismatched locator accepted")
	}
	output.Reset()
	if err := app.RunContext(t.Context(), []string{"nvkr", "evidence", "verify-local", "--root", out, "--expected", expectedPath, "--json"}); err != nil {
		t.Fatal(err)
	}
	var verified testobservation.Verification
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(output.Bytes(), []byte("\n")), &verified); err != nil {
		t.Fatal(err)
	}
	if !verified.SelfConsistent || !verified.Passing || verified.SignerAuthenticated {
		t.Fatalf("unqualified verification escalated: %+v", verified)
	}
	if err := os.WriteFile(filepath.Join(out, "files", "junit.xml"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.RunContext(t.Context(), []string{"nvkr", "evidence", "verify-local", "--root", out, "--expected", expectedPath, "--json"}); err != nil {
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeVerifyFailed {
			t.Fatalf("tamper error = %v, want %s", err, e.CodeVerifyFailed)
		}
	} else {
		t.Fatal("tampered result accepted")
	}
}

func TestEvidenceVerificationCoordinatesAdmittedBeforeFileIO(t *testing.T) {
	for _, args := range [][]string{nil, {"--reference", "example.com/evidence@sha256:" + strings.Repeat("a", 64), "--locator", "missing-locator"}} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			app := newApp()
			app.Writer, app.ErrWriter = &bytes.Buffer{}, &bytes.Buffer{}
			base := []string{"nvkr", "evidence", "verify", "--expected", "missing-policy", "--cosign", "missing-binary", "--cosign-digest", "sha256:" + strings.Repeat("a", 64), "--identity", "fixture", "--issuer", "fixture", "--anonymous"}
			err := app.RunContext(t.Context(), append(base, args...))
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeUsage {
				t.Fatalf("error = %v, want coordinate usage admission", err)
			}
		})
	}
}

func TestEvidenceOptionsCredentialScope(t *testing.T) {
	t.Setenv("NVKR_REGISTRY", "example.com")
	t.Setenv("NVKR_REGISTRY_USER", "fixture-user")
	t.Setenv("NVKR_REGISTRY_PASSWORD", "fixture-password")
	for _, anonymous := range []bool{false, true} {
		t.Run(fmt.Sprint(anonymous), func(t *testing.T) {
			flags := flag.NewFlagSet("evidence", flag.ContinueOnError)
			for _, f := range evidenceRegistryFlags() {
				if err := f.Apply(flags); err != nil {
					t.Fatal(err)
				}
			}
			if err := flags.Set("anonymous", fmt.Sprint(anonymous)); err != nil {
				t.Fatal(err)
			}
			options := evidenceOptions(cli.NewContext(cli.NewApp(), flags, nil))
			if options.Transport.Anonymous != anonymous {
				t.Fatal("anonymous mode changed")
			}
			if anonymous {
				if options.Credential.Registry != "" || options.Transport.Username != "" || options.Transport.Password != "" {
					t.Fatal("anonymous mode inherited credentials")
				}
			} else if options.Credential.Registry != "example.com" || options.Credential.Username != "fixture-user" || options.Credential.Password != "fixture-password" || options.Transport.Username != options.Credential.Username || options.Transport.Password != options.Credential.Password {
				t.Fatal("explicit host-scoped credentials were not mapped consistently")
			}
		})
	}
}
