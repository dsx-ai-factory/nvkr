// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

type suiteFixtureV2 struct {
	observation      testobservation.Observation
	verification     testobservation.Verification
	identity, issuer string
	closed           bool
}

func (f *suiteFixtureV2) Observation(context.Context) (testobservation.Observation, error) {
	if f.closed {
		return testobservation.Observation{}, e.New(e.CodeGateUnmet, "closed fixture")
	}
	raw, err := f.observation.Canonical()
	if err != nil {
		return testobservation.Observation{}, err
	}
	return testobservation.Decode(raw)
}
func (f *suiteFixtureV2) Description(context.Context) (testobservation.Verification, error) {
	if f.closed {
		return testobservation.Verification{}, e.New(e.CodeGateUnmet, "closed fixture")
	}
	return f.verification, nil
}
func (f *suiteFixtureV2) Reference(context.Context) (string, error) {
	if f.closed {
		return "", e.New(e.CodeGateUnmet, "closed fixture")
	}
	return "registry.example.test/observations@" + f.verification.ObservationDigest, nil
}
func (f *suiteFixtureV2) Signer(context.Context) (string, string, error) {
	if f.closed {
		return "", "", e.New(e.CodeGateUnmet, "closed fixture")
	}
	return f.identity, f.issuer, nil
}

type suiteSourceV2 struct {
	description lifecycle.ArtifactCheckDescription
	closed      bool
}

func (f *suiteSourceV2) Description() (lifecycle.ArtifactCheckDescription, error) {
	if f.closed {
		return lifecycle.ArtifactCheckDescription{}, e.New(e.CodeGateUnmet, "source closed")
	}
	return f.description, nil
}
func suiteTestV2(t *testing.T) (*suiteFixtureV2, *suiteSourceV2, SuiteRequirement) {
	t.Helper()
	base := evidenceFixture()
	checks := &suiteSourceV2{description: base.ArtifactChecks}
	subject := base.ArtifactChecks.Outputs[0]
	source := base.ArtifactChecks.Origin
	observation := testobservation.Observation{SchemaVersion: testobservation.SchemaVersion,
		Inputs:      testobservation.Inputs{DefinitionDigest: base.ArtifactChecks.Inputs.DefinitionDigest, EnvironmentDigest: base.ArtifactChecks.Inputs.EnvironmentDigest, PlanDigest: base.ArtifactChecks.Inputs.PlanDigest, PublicationDigest: base.ArtifactChecks.Inputs.ReleaseSubject},
		Origin:      testobservation.Origin{Status: "declared", Repository: source.Repository, Commit: source.Commit, Run: source.RunID, Attempt: 1, Workflow: source.Workflow},
		Subjects:    []testobservation.Subject{{Output: subject.Name, ArtifactDigest: subject.Subject, PayloadDigest: subject.BuildSubjects[0].Digest, BuildDigest: subject.BuildDigest, Platform: "linux/amd64"}},
		Suite:       testobservation.Suite{Name: "conformance", HarnessRevision: strings.Repeat("c", 40), ImageDigest: subject.Subject, PolicyDigest: subject.Subject, Invocation: "suite-invocation-1", Selection: []string{"conformance"}, RequiredCases: []testobservation.CaseID{{Suite: "suite", Name: "case"}}},
		Environment: testobservation.Environment{Role: "tenant", Facts: []testobservation.Fact{{Name: "kubernetes-version", Value: "fixture", Evidence: "cluster-api", Phase: "before"}}}, Execution: testobservation.Outcome{Status: "completed"}, Cleanup: testobservation.Outcome{Status: "completed"},
		Files: []testobservation.File{{Name: "results.xml", Role: "junit", Digest: subject.Subject, Size: 100}}, Summary: testresults.Summary{Total: 1, Passed: 1}}
	observation.Subjects = nil
	for _, selected := range base.ArtifactChecks.Outputs {
		observation.Subjects = append(observation.Subjects, testobservation.Subject{Output: selected.Name, ArtifactDigest: selected.Subject, PayloadDigest: selected.BuildSubjects[0].Digest, BuildDigest: selected.BuildDigest, Platform: "linux/amd64"})
	}
	digest, err := observation.Digest()
	if err != nil {
		t.Fatal(err)
	}
	fixture := &suiteFixtureV2{observation: observation, verification: testobservation.Verification{ObservationDigest: digest, SignerAuthenticated: true, SelfConsistent: true, Passing: true}, identity: "https://github.com/example/nvkr/.github/workflows/conformance.yml@refs/heads/main", issuer: "https://token.actions.githubusercontent.com"}
	requirement := SuiteRequirement{Expected: testobservation.Expected{ObservationDigest: digest, Inputs: observation.Inputs, Subjects: observation.Subjects, Suite: observation.Suite}, Identity: fixture.identity, Issuer: fixture.issuer, RequiredRole: "tenant"}
	return fixture, checks, requirement
}
func rebindSuiteFixtureV2(t *testing.T, f *suiteFixtureV2, r *SuiteRequirement) {
	t.Helper()
	digest, err := f.observation.Digest()
	if err != nil {
		t.Fatal(err)
	}
	r.Expected = testobservation.Expected{ObservationDigest: digest, Inputs: f.observation.Inputs, Subjects: f.observation.Subjects, Suite: f.observation.Suite}
	f.verification.ObservationDigest = digest
}

type attesterV2Func func(context.Context, sign.Subject, []byte) error

func (f attesterV2Func) AttestQualificationV2(ctx context.Context, s sign.Subject, raw []byte) error {
	return f(ctx, s, raw)
}

func TestSuiteV2NegativeAdmissionPerformsNoSigning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*suiteFixtureV2, *suiteSourceV2, *SuiteRequirement)
		code   e.Code
		rebind bool
	}{
		{"wrong Run", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) { f.observation.Origin.Run = "99" }, e.CodeGateUnmet, true},
		{"wrong attempt", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) { f.observation.Origin.Attempt = 2 }, e.CodeGateUnmet, true},
		{"wrong subject", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) {
			f.observation.Subjects[0].ArtifactDigest = "sha256:" + strings.Repeat("b", 64)
		}, e.CodeGateUnmet, true},
		{"wrong policy", func(_ *suiteFixtureV2, _ *suiteSourceV2, r *SuiteRequirement) {
			r.Expected.Suite.PolicyDigest = "sha256:" + strings.Repeat("b", 64)
		}, e.CodeVerifyFailed, false},
		{"wrong signer", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) { f.identity = "different" }, e.CodeUntrustedSigner, false},
		{"wrong issuer", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) { f.issuer = "different" }, e.CodeUntrustedSigner, false},
		{"wrong role", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) {
			f.observation.Environment.Role = "site"
		}, e.CodeGateUnmet, true},
		{"failed results", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) {
			f.observation.Summary.Passed = 0
			f.observation.Summary.Failed = 1
		}, e.CodeGateUnmet, true},
		{"skipped results", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) {
			f.observation.Summary.Passed = 0
			f.observation.Summary.Skipped = 1
		}, e.CodeGateUnmet, true},
		{"empty required coverage", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) {
			f.observation.Suite.RequiredCases = nil
		}, e.CodeGateUnmet, true},
		{"missing results", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) {
			f.observation.Summary = testresults.Summary{}
			f.observation.Execution.Status = "unknown"
		}, e.CodeGateUnmet, true},
		{"missing cleanup", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) {
			f.observation.Cleanup.Status = "unknown"
		}, e.CodeGateUnmet, true},
		{"failed cleanup", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) {
			f.observation.Cleanup = testobservation.Outcome{Status: "failed", ExitCode: 1}
		}, e.CodeGateUnmet, true},
		{"closed observation", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) { f.closed = true }, e.CodeGateUnmet, false},
		{"closed source", func(_ *suiteFixtureV2, s *suiteSourceV2, _ *SuiteRequirement) { s.closed = true }, e.CodeGateUnmet, false},
		{"unsigned", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) {
			f.verification.SignerAuthenticated = false
		}, e.CodeGateUnmet, false},
		{"incomplete", func(f *suiteFixtureV2, _ *suiteSourceV2, _ *SuiteRequirement) { f.verification.Passing = false }, e.CodeGateUnmet, false},
		{"installed byte proof required", func(_ *suiteFixtureV2, _ *suiteSourceV2, r *SuiteRequirement) {
			r.RequireAuthenticatedDeployment = true
		}, e.CodeEngineCapability, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, r := suiteTestV2(t)
			tc.edit(f, s, &r)
			if tc.rebind {
				rebindSuiteFixtureV2(t, f, &r)
			}
			calls := 0
			p := &PublisherV2{newAttester: func(string) (qualificationAttesterV2, error) { calls++; return nil, nil }}
			receipt, err := authenticateSuiteV2(t.Context(), f, s, r)
			requireCode(t, err, tc.code)
			// Exercise the same production delegate and admission closure rather
			// than failing at an unrelated empty artifact capability.
			base := evidenceFixture()
			invocation := &invocationFixtureV2{description: base.Invocation, source: s.description.Origin}
			_, err = p.publishReceiptsV2(t.Context(), s, invocation, nil, PolicyV2{RequiredSuites: []SuiteRequirement{r}}, []*SuiteReceipt{receipt})
			code := e.CodeGateUnmet
			if r.RequireAuthenticatedDeployment {
				code = e.CodeEngineCapability
			}
			requireCode(t, err, code)
			if err == nil || calls != 0 {
				t.Fatalf("negative admission reached signing: %v", err)
			}
		})
	}
}
func TestSuiteV2PositiveReceiptAndRevocation(t *testing.T) {
	fixture, checks, requirement := suiteTestV2(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	receipt, err := authenticateSuiteV2(ctx, fixture, checks, requirement)
	if err != nil {
		t.Fatal(err)
	}
	copied := *receipt
	policy := PolicyV2{RequiredSuites: []SuiteRequirement{requirement}}
	suites, err := collectSuitesV2(ctx, checks.description, policy, []*SuiteReceipt{&copied})
	if err != nil {
		t.Fatal(err)
	}
	base := evidenceFixture()
	base.ExternalSuites = nil
	evidence := EvidenceV2{SchemaVersion: SchemaVersionV2, GateVersion: GateVersionV2, ArtifactChecks: base.ArtifactChecks, Invocation: base.Invocation, Smoke: base.Smoke, ExternalSuites: []ExternalSuite{}, Suites: suites}
	calls := 0
	p := &PublisherV2{newAttester: func(string) (qualificationAttesterV2, error) {
		return attesterV2Func(func(_ context.Context, s sign.Subject, raw []byte) error {
			calls++
			if _, err := DecodeV2(raw); err != nil {
				return err
			}
			return nil
		}), nil
	}}
	result, err := p.publishEvidenceV2(ctx, evidence, &PublicationV2{}, func() error {
		_, err := collectSuitesV2(ctx, checks.description, policy, []*SuiteReceipt{&copied})
		return err
	})
	if err != nil || !result.Successful || calls != 2 {
		t.Fatalf("complete positive fixture: %+v %v calls=%d", result, err, calls)
	}
	if err := receipt.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = collectSuitesV2(ctx, checks.description, policy, []*SuiteReceipt{&copied})
	requireCode(t, err, e.CodeGateUnmet)
	fresh, err := authenticateSuiteV2(ctx, fixture, checks, requirement)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = fresh.require(t.Context(), checks.description)
	requireCode(t, err, e.CodeTimeout)
}
func TestSuiteV2MissingDuplicateAndPolicyMismatch(t *testing.T) {
	fixture, checks, r := suiteTestV2(t)
	receipt, err := authenticateSuiteV2(t.Context(), fixture, checks, r)
	if err != nil {
		t.Fatal(err)
	}
	for _, receipts := range [][]*SuiteReceipt{nil, {nil}, {receipt, receipt}} {
		_, err := collectSuitesV2(t.Context(), checks.description, PolicyV2{RequiredSuites: []SuiteRequirement{r}}, receipts)
		requireCode(t, err, e.CodeGateUnmet)
	}
	other := r
	other.Identity = "another-independent-signer"
	_, err = collectSuitesV2(t.Context(), checks.description, PolicyV2{RequiredSuites: []SuiteRequirement{other}}, []*SuiteReceipt{receipt})
	requireCode(t, err, e.CodeGateUnmet)
}
func TestQualificationV2StrictVersionAndSmoke(t *testing.T) {
	fixture, checks, r := suiteTestV2(t)
	receipt, err := authenticateSuiteV2(t.Context(), fixture, checks, r)
	if err != nil {
		t.Fatal(err)
	}
	suites, err := collectSuitesV2(t.Context(), checks.description, PolicyV2{RequiredSuites: []SuiteRequirement{r}}, []*SuiteReceipt{receipt})
	if err != nil {
		t.Fatal(err)
	}
	base := evidenceFixture()
	base.ExternalSuites = nil
	evidence := EvidenceV2{SchemaVersion: SchemaVersionV2, GateVersion: GateVersionV2, ArtifactChecks: base.ArtifactChecks, Invocation: base.Invocation, Smoke: base.Smoke, ExternalSuites: []ExternalSuite{}, Suites: suites}
	raw, err := evidence.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(raw); err == nil {
		t.Fatal("v1 accepted v2")
	}
	if _, err := DecodeV2(raw); err != nil {
		t.Fatal(err)
	}
	v1raw, err := base.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeV2(v1raw); err == nil {
		t.Fatal("v2 accepted v1")
	}
	evidence.ArtifactChecks.Outputs[0].Kind = lifecycle.OutputExecutable
	_, err = collectSmoke(evidence.ArtifactChecks, evidence.Invocation, nil)
	requireCode(t, err, e.CodeGateUnmet)
	evidence.Smoke[0].Status = "not-applicable"
	requireCode(t, evidence.Validate(), e.CodeGateUnmet)
}

func TestPublisherV2StockAdmissionBeforeProviderWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := NewPublisherV2(ctx, Options{})
	requireCode(t, err, e.CodeTimeout)
	if runtime.GOOS != "linux" {
		_, err := NewPublisherV2(t.Context(), Options{CosignBinary: "/does/not/exist", CosignDigest: "sha256:" + strings.Repeat("a", 64)})
		requireCode(t, err, e.CodeEngineCapability)
		return
	}
	_, err = NewPublisherV2(t.Context(), Options{CosignBinary: "/does/not/exist"})
	requireCode(t, err, e.CodeUnpinnedInput)
	path := filepath.Join(t.TempDir(), "cosign")
	if err := os.WriteFile(path, []byte("wrong executable bytes"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = NewPublisherV2(t.Context(), Options{CosignBinary: path, CosignDigest: "sha256:" + strings.Repeat("a", 64)})
	requireCode(t, err, e.CodeUnpinnedInput)
}
func TestSuiteV2PreSignRevocationAndDetachedDescriptions(t *testing.T) {
	for _, kind := range []string{"source", "observation", "suite", "cancellation"} {
		t.Run(kind, func(t *testing.T) {
			fixture, checks, r := suiteTestV2(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			receipt, err := authenticateSuiteV2(ctx, fixture, checks, r)
			if err != nil {
				t.Fatal(err)
			}
			policy := PolicyV2{RequiredSuites: []SuiteRequirement{r}}
			suites, err := collectSuitesV2(ctx, checks.description, policy, []*SuiteReceipt{receipt})
			if err != nil {
				t.Fatal(err)
			}
			suites[0].Requirement.Expected.Subjects[0].Output = "mutated-description"
			if _, err := receipt.require(ctx, checks.description); err != nil {
				t.Fatalf("description mutated authority: %v", err)
			}
			suites, err = collectSuitesV2(ctx, checks.description, policy, []*SuiteReceipt{receipt})
			if err != nil {
				t.Fatal(err)
			}
			base := evidenceFixture()
			evidence := EvidenceV2{SchemaVersion: SchemaVersionV2, GateVersion: GateVersionV2, ArtifactChecks: base.ArtifactChecks, Invocation: base.Invocation, Smoke: base.Smoke, ExternalSuites: []ExternalSuite{}, Suites: suites}
			switch kind {
			case "source":
				checks.closed = true
			case "observation":
				fixture.closed = true
			case "suite":
				_ = receipt.Close()
			case "cancellation":
				cancel()
			}
			calls := 0
			p := &PublisherV2{newAttester: func(string) (qualificationAttesterV2, error) { calls++; return nil, nil }}
			result, err := p.publishEvidenceV2(ctx, evidence, &PublicationV2{}, func() error {
				_, err := collectSuitesV2(ctx, checks.description, policy, []*SuiteReceipt{receipt})
				return err
			})
			if err == nil || calls != 0 || result.Successful {
				t.Fatalf("revoked gate reached signing: %v %d", err, calls)
			}
		})
	}
}

func TestSuiteV2UnselectedSkippedCasesAreNotRequiredCoverage(t *testing.T) {
	for _, requiredSkip := range []bool{false, true} {
		t.Run(fmt.Sprintf("required-skip-%t", requiredSkip), func(t *testing.T) {
			fixture, checks, r := suiteTestV2(t)
			result := `<testsuite name="suite" tests="2" skipped="1"><testcase name="case"/><testcase name="unselected"><skipped/></testcase></testsuite>`
			if requiredSkip {
				result = `<testsuite name="suite" tests="2" skipped="1"><testcase name="case"><skipped/></testcase><testcase name="unselected"/></testsuite>`
			}
			source := t.TempDir()
			if err := os.WriteFile(filepath.Join(source, "results.xml"), []byte(result), 0600); err != nil {
				t.Fatal(err)
			}
			observation := fixture.observation
			observation.Files = nil
			observation.Summary = testresults.Summary{}
			out := filepath.Join(t.TempDir(), "bundle")
			if err := testobservation.Emit(t.Context(), source, observation, []testobservation.FileInput{{Name: "results.xml", Role: "junit", ExpectedDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(result)))}}, out); err != nil {
				t.Fatal(err)
			}
			bundle, err := testobservation.Open(t.Context(), out)
			if err != nil {
				t.Fatal(err)
			}
			defer bundle.Close()
			fixture.observation = bundle.Observation()
			fixture.verification.Passing = bundle.Passing()
			rebindSuiteFixtureV2(t, fixture, &r)
			receipt, err := authenticateSuiteV2(t.Context(), fixture, checks, r)
			if requiredSkip {
				requireCode(t, err, e.CodeGateUnmet)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := receipt.require(t.Context(), checks.description); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestSuiteV2NativeChildMembership(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testobservation.Subject)
	}{
		{"arm64 child only", nil},
		{"all native children", func(_ *testobservation.Subject) {}},
		{"wrong platform", func(s *testobservation.Subject) { s.Platform = "linux/amd64" }},
		{"wrong native build", func(s *testobservation.Subject) { s.BuildDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"sibling payload", func(s *testobservation.Subject) { s.PayloadDigest = "sha256:" + strings.Repeat("b", 64) }},
		{"native manifest as payload", func(s *testobservation.Subject) { s.PayloadDigest = "sha256:" + strings.Repeat("d", 64) }},
		{"archive as native artifact", func(s *testobservation.Subject) { s.ArtifactDigest = "sha256:" + strings.Repeat("2", 64) }},
		{"aggregate artifact substituted", func(s *testobservation.Subject) { s.ArtifactDigest = "sha256:" + strings.Repeat("a", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, checks, r := suiteTestV2(t)
			pin := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
			children := []delivery.ImageIndexChildBinding{
				{Platform: "linux/amd64", ManifestDigest: pin("b"), Size: 100, BuildDigest: pin("c"), Component: "image", BuildSubject: pin("1")},
				{Platform: "linux/arm64", ManifestDigest: pin("d"), Size: 100, BuildDigest: pin("e"), Component: "image", BuildSubject: pin("2")},
			}
			output := &checks.description.Outputs[0]
			output.Kind = lifecycle.OutputOCI
			output.BuildSubjects = nil
			output.ImageIndex = &delivery.ImageIndexBinding{RequiredPlatforms: []string{"linux/amd64", "linux/arm64"}, Children: children}
			aggregate, err := ir.ImageIndexBuildDigest(output.ImageIndex.RequiredPlatforms, []ir.ImageIndexBuildChild{{Platform: children[0].Platform, BuildDigest: children[0].BuildDigest, Component: "image"}, {Platform: children[1].Platform, BuildDigest: children[1].BuildDigest, Component: "image"}})
			if err != nil {
				t.Fatal(err)
			}
			output.BuildDigest = aggregate
			fixture.observation.Subjects[0] = testobservation.Subject{Output: output.Name, Platform: children[1].Platform, ArtifactDigest: children[1].ManifestDigest, BuildDigest: children[1].BuildDigest, PayloadDigest: children[1].BuildSubject}
			if tc.edit != nil {
				tc.edit(&fixture.observation.Subjects[0])
			}
			if tc.name == "all native children" {
				fixture.observation.Subjects = append([]testobservation.Subject{{Output: output.Name, Platform: children[0].Platform, ArtifactDigest: children[0].ManifestDigest, BuildDigest: children[0].BuildDigest, PayloadDigest: children[0].BuildSubject}}, fixture.observation.Subjects...)
			}
			rebindSuiteFixtureV2(t, fixture, &r)
			receipt, err := authenticateSuiteV2(t.Context(), fixture, checks, r)
			if tc.name != "all native children" {
				requireCode(t, err, e.CodeGateUnmet)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := receipt.require(t.Context(), checks.description); err != nil {
					t.Fatal(err)
				}
				// Native suite acceptance does not implement OCI M3-B smoke applicability.
				_, err = collectSmoke(checks.description, evidenceFixture().Invocation, nil)
				requireCode(t, err, e.CodeEngineCapability)
			}
		})
	}
}
