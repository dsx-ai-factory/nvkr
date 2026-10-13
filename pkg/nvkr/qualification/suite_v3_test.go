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

type suiteFixtureV3 struct {
	observation      testobservation.Observation
	verification     testobservation.Verification
	identity, issuer string
	closed           bool
}

func (f *suiteFixtureV3) Observation(context.Context) (testobservation.Observation, error) {
	if f.closed {
		return testobservation.Observation{}, e.New(e.CodeGateUnmet, "closed fixture")
	}
	raw, err := f.observation.Canonical()
	if err != nil {
		return testobservation.Observation{}, err
	}
	return testobservation.Decode(raw)
}
func (f *suiteFixtureV3) Description(context.Context) (testobservation.Verification, error) {
	if f.closed {
		return testobservation.Verification{}, e.New(e.CodeGateUnmet, "closed fixture")
	}
	return f.verification, nil
}
func (f *suiteFixtureV3) Reference(context.Context) (string, error) {
	if f.closed {
		return "", e.New(e.CodeGateUnmet, "closed fixture")
	}
	return "registry.example.test/observations@" + f.verification.ObservationDigest, nil
}
func (f *suiteFixtureV3) Signer(context.Context) (string, string, error) {
	if f.closed {
		return "", "", e.New(e.CodeGateUnmet, "closed fixture")
	}
	return f.identity, f.issuer, nil
}

type suiteSourceV3 struct {
	description lifecycle.ArtifactCheckDescription
	closed      bool
}

func (f *suiteSourceV3) Description() (lifecycle.ArtifactCheckDescription, error) {
	if f.closed {
		return lifecycle.ArtifactCheckDescription{}, e.New(e.CodeGateUnmet, "source closed")
	}
	return f.description, nil
}
func suiteTestV3(t *testing.T) (*suiteFixtureV3, *suiteSourceV3, SuiteRequirement) {
	t.Helper()
	base := evidenceFixture()
	checks := &suiteSourceV3{description: base.ArtifactChecks}
	subject := base.ArtifactChecks.Outputs[0]
	source := base.ArtifactChecks.Origin
	observation := testobservation.Observation{SchemaVersion: testobservation.SchemaVersion,
		Inputs:      testobservation.Inputs{DefinitionDigest: base.ArtifactChecks.Inputs.DefinitionDigest, EnvironmentDigest: base.ArtifactChecks.Inputs.EnvironmentDigest, PlanDigest: base.ArtifactChecks.Inputs.PlanDigest, PublicationDigest: strings.TrimPrefix(base.ArtifactChecks.Inputs.ReleaseSubject, "registry.example.test/product@")},
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
	fixture := &suiteFixtureV3{observation: observation, verification: testobservation.Verification{ObservationDigest: digest, SignerAuthenticated: true, SelfConsistent: true, Passing: true}, identity: "https://github.com/example/nvkr/.github/workflows/conformance.yml@refs/heads/main", issuer: "https://token.actions.githubusercontent.com"}
	requirement := SuiteRequirement{Expected: testobservation.Expected{ObservationDigest: digest, Inputs: observation.Inputs, Subjects: observation.Subjects, Suite: observation.Suite}, Identity: fixture.identity, Issuer: fixture.issuer, RequiredRole: "tenant"}
	return fixture, checks, requirement
}
func rebindSuiteFixtureV3(t *testing.T, f *suiteFixtureV3, r *SuiteRequirement) {
	t.Helper()
	digest, err := f.observation.Digest()
	if err != nil {
		t.Fatal(err)
	}
	r.Expected = testobservation.Expected{ObservationDigest: digest, Inputs: f.observation.Inputs, Subjects: f.observation.Subjects, Suite: f.observation.Suite}
	f.verification.ObservationDigest = digest
}

type attesterV3Func func(context.Context, sign.Subject, []byte) error

func (f attesterV3Func) AttestQualificationV3(ctx context.Context, s sign.Subject, raw []byte) error {
	return f(ctx, s, raw)
}

func TestSuiteV3NegativeAdmissionPerformsNoSigning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*suiteFixtureV3, *suiteSourceV3, *SuiteRequirement)
		code   e.Code
		rebind bool
	}{
		{"wrong Run", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) { f.observation.Origin.Run = "99" }, e.CodeGateUnmet, true},
		{"wrong attempt", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) { f.observation.Origin.Attempt = 2 }, e.CodeGateUnmet, true},
		{"wrong subject", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) {
			f.observation.Subjects[0].ArtifactDigest = "sha256:" + strings.Repeat("b", 64)
		}, e.CodeGateUnmet, true},
		{"wrong policy", func(_ *suiteFixtureV3, _ *suiteSourceV3, r *SuiteRequirement) {
			r.Expected.Suite.PolicyDigest = "sha256:" + strings.Repeat("b", 64)
		}, e.CodeVerifyFailed, false},
		{"wrong signer", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) { f.identity = "different" }, e.CodeUntrustedSigner, false},
		{"wrong issuer", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) { f.issuer = "different" }, e.CodeUntrustedSigner, false},
		{"wrong role", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) {
			f.observation.Environment.Role = "site"
		}, e.CodeGateUnmet, true},
		{"failed results", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) {
			f.observation.Summary.Passed = 0
			f.observation.Summary.Failed = 1
		}, e.CodeGateUnmet, true},
		{"skipped results", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) {
			f.observation.Summary.Passed = 0
			f.observation.Summary.Skipped = 1
		}, e.CodeGateUnmet, true},
		{"empty required coverage", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) {
			f.observation.Suite.RequiredCases = nil
		}, e.CodeGateUnmet, true},
		{"missing results", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) {
			f.observation.Summary = testresults.Summary{}
			f.observation.Execution.Status = "unknown"
		}, e.CodeGateUnmet, true},
		{"missing cleanup", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) {
			f.observation.Cleanup.Status = "unknown"
		}, e.CodeGateUnmet, true},
		{"failed cleanup", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) {
			f.observation.Cleanup = testobservation.Outcome{Status: "failed", ExitCode: 1}
		}, e.CodeGateUnmet, true},
		{"closed observation", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) { f.closed = true }, e.CodeGateUnmet, false},
		{"closed source", func(_ *suiteFixtureV3, s *suiteSourceV3, _ *SuiteRequirement) { s.closed = true }, e.CodeGateUnmet, false},
		{"unsigned", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) {
			f.verification.SignerAuthenticated = false
		}, e.CodeGateUnmet, false},
		{"incomplete", func(f *suiteFixtureV3, _ *suiteSourceV3, _ *SuiteRequirement) { f.verification.Passing = false }, e.CodeGateUnmet, false},
		{"installed byte proof required", func(_ *suiteFixtureV3, _ *suiteSourceV3, r *SuiteRequirement) {
			r.RequireAuthenticatedDeployment = true
		}, e.CodeEngineCapability, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, r := suiteTestV3(t)
			tc.edit(f, s, &r)
			if tc.rebind {
				rebindSuiteFixtureV3(t, f, &r)
			}
			calls := 0
			p := &PublisherV3{cosignDigest: evidenceFixture().ToolSmoke.Policy.Cosign.Digest, newAttester: func(string) (qualificationAttesterV3, error) { calls++; return nil, nil }}
			receipt, err := authenticateSuiteV3(t.Context(), f, s, r)
			requireCode(t, err, tc.code)
			// Exercise the same production delegate and admission closure rather
			// than failing at an unrelated empty artifact capability.
			base := evidenceFixture()
			invocation := &invocationFixtureV3{description: base.Invocation, source: s.description.Origin}
			_, err = p.publishReceiptsV3(t.Context(), s, invocation, nil, PolicyV3{SchemaVersion: PolicySchemaV3, NativePolicyDigest: evidenceFixture().ToolSmoke.PolicyDigest, RequiredSuites: []SuiteRequirement{r}}, []*SuiteReceipt{receipt})
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
func TestSuiteV3PositiveReceiptAndRevocation(t *testing.T) {
	fixture, checks, requirement := suiteTestV3(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	receipt, err := authenticateSuiteV3(ctx, fixture, checks, requirement)
	if err != nil {
		t.Fatal(err)
	}
	copied := *receipt
	policy := PolicyV3{SchemaVersion: PolicySchemaV3, NativePolicyDigest: evidenceFixture().ToolSmoke.PolicyDigest, RequiredSuites: []SuiteRequirement{requirement}}
	suites, err := collectSuitesV3(ctx, checks.description, policy, []*SuiteReceipt{&copied})
	if err != nil {
		t.Fatal(err)
	}
	base := evidenceFixture()
	evidence := EvidenceV3{ToolSmoke: base.ToolSmoke, Policy: policyForSuiteEvidenceV3(t, suites), PolicyDigest: digestForSuiteEvidenceV3(t, suites), SchemaVersion: SchemaVersionV3, GateVersion: GateVersionV3, ArtifactChecks: base.ArtifactChecks, Invocation: base.Invocation, Smoke: base.Smoke, ExternalSuites: base.ExternalSuites, Suites: suites}
	calls := 0
	p := &PublisherV3{cosignDigest: evidenceFixture().ToolSmoke.Policy.Cosign.Digest, newAttester: func(string) (qualificationAttesterV3, error) {
		return attesterV3Func(func(_ context.Context, s sign.Subject, raw []byte) error {
			calls++
			if _, err := DecodeV3(raw); err != nil {
				return err
			}
			return nil
		}), nil
	}}
	result, err := p.publishEvidenceV3(ctx, evidence, &PublicationV3{}, func() error {
		_, err := collectSuitesV3(ctx, checks.description, policy, []*SuiteReceipt{&copied})
		return err
	})
	if err != nil || !result.Successful || calls != 2 {
		t.Fatalf("complete positive fixture: %+v %v calls=%d", result, err, calls)
	}
	if err := receipt.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = collectSuitesV3(ctx, checks.description, policy, []*SuiteReceipt{&copied})
	requireCode(t, err, e.CodeGateUnmet)
	fresh, err := authenticateSuiteV3(ctx, fixture, checks, requirement)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_, err = fresh.require(t.Context(), checks.description)
	requireCode(t, err, e.CodeTimeout)
}
func TestSuiteV3MissingDuplicateAndPolicyMismatch(t *testing.T) {
	fixture, checks, r := suiteTestV3(t)
	receipt, err := authenticateSuiteV3(t.Context(), fixture, checks, r)
	if err != nil {
		t.Fatal(err)
	}
	for _, receipts := range [][]*SuiteReceipt{nil, {nil}, {receipt, receipt}} {
		_, err := collectSuitesV3(t.Context(), checks.description, PolicyV3{SchemaVersion: PolicySchemaV3, NativePolicyDigest: evidenceFixture().ToolSmoke.PolicyDigest, RequiredSuites: []SuiteRequirement{r}}, receipts)
		requireCode(t, err, e.CodeGateUnmet)
	}
	other := r
	other.Identity = "another-independent-signer"
	_, err = collectSuitesV3(t.Context(), checks.description, PolicyV3{SchemaVersion: PolicySchemaV3, NativePolicyDigest: evidenceFixture().ToolSmoke.PolicyDigest, RequiredSuites: []SuiteRequirement{other}}, []*SuiteReceipt{receipt})
	requireCode(t, err, e.CodeGateUnmet)
}
func TestQualificationV3StrictVersionAndSmoke(t *testing.T) {
	fixture, checks, r := suiteTestV3(t)
	receipt, err := authenticateSuiteV3(t.Context(), fixture, checks, r)
	if err != nil {
		t.Fatal(err)
	}
	suites, err := collectSuitesV3(t.Context(), checks.description, PolicyV3{SchemaVersion: PolicySchemaV3, NativePolicyDigest: evidenceFixture().ToolSmoke.PolicyDigest, RequiredSuites: []SuiteRequirement{r}}, []*SuiteReceipt{receipt})
	if err != nil {
		t.Fatal(err)
	}
	base := evidenceFixture()
	evidence := EvidenceV3{ToolSmoke: base.ToolSmoke, Policy: policyForSuiteEvidenceV3(t, suites), PolicyDigest: digestForSuiteEvidenceV3(t, suites), SchemaVersion: SchemaVersionV3, GateVersion: GateVersionV3, ArtifactChecks: base.ArtifactChecks, Invocation: base.Invocation, Smoke: base.Smoke, ExternalSuites: base.ExternalSuites, Suites: suites}
	raw, err := evidence.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(raw); err == nil {
		t.Fatal("native v2 accepted v3")
	}
	if _, err := DecodeV3(raw); err != nil {
		t.Fatal(err)
	}
	nativeRaw, err := base.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeV3(nativeRaw); err == nil {
		t.Fatal("v3 accepted native v2")
	}
	evidence.ArtifactChecks.Outputs[0].Kind = lifecycle.OutputExecutable
	_, _, err = collectSmoke(evidence.ArtifactChecks, evidence.Invocation, nil, evidence.ToolSmoke.PolicyDigest)
	requireCode(t, err, e.CodeGateUnmet)
	evidence.Smoke[0].Status = "not-applicable"
	requireCode(t, evidence.Validate(), e.CodeGateUnmet)
}

func TestPublisherV3StockAdmissionBeforeProviderWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := NewPublisherV3(ctx, Options{})
	requireCode(t, err, e.CodeTimeout)
	if runtime.GOOS != "linux" {
		_, err := NewPublisherV3(t.Context(), Options{CosignBinary: "/does/not/exist", CosignDigest: "sha256:" + strings.Repeat("a", 64)})
		requireCode(t, err, e.CodeEngineCapability)
		return
	}
	_, err = NewPublisherV3(t.Context(), Options{CosignBinary: "/does/not/exist"})
	requireCode(t, err, e.CodeUnpinnedInput)
	path := filepath.Join(t.TempDir(), "cosign")
	if err := os.WriteFile(path, []byte("wrong executable bytes"), 0700); err != nil {
		t.Fatal(err)
	}
	_, err = NewPublisherV3(t.Context(), Options{CosignBinary: path, CosignDigest: "sha256:" + strings.Repeat("a", 64)})
	requireCode(t, err, e.CodeUnpinnedInput)
}
func TestSuiteV3PreSignRevocationAndDetachedDescriptions(t *testing.T) {
	for _, kind := range []string{"source", "observation", "suite", "cancellation"} {
		t.Run(kind, func(t *testing.T) {
			fixture, checks, r := suiteTestV3(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			receipt, err := authenticateSuiteV3(ctx, fixture, checks, r)
			if err != nil {
				t.Fatal(err)
			}
			policy := PolicyV3{SchemaVersion: PolicySchemaV3, NativePolicyDigest: evidenceFixture().ToolSmoke.PolicyDigest, RequiredSuites: []SuiteRequirement{r}}
			suites, err := collectSuitesV3(ctx, checks.description, policy, []*SuiteReceipt{receipt})
			if err != nil {
				t.Fatal(err)
			}
			suites[0].Requirement.Expected.Subjects[0].Output = "mutated-description"
			if _, err := receipt.require(ctx, checks.description); err != nil {
				t.Fatalf("description mutated authority: %v", err)
			}
			suites, err = collectSuitesV3(ctx, checks.description, policy, []*SuiteReceipt{receipt})
			if err != nil {
				t.Fatal(err)
			}
			base := evidenceFixture()
			evidence := EvidenceV3{ToolSmoke: base.ToolSmoke, Policy: policyForSuiteEvidenceV3(t, suites), PolicyDigest: digestForSuiteEvidenceV3(t, suites), SchemaVersion: SchemaVersionV3, GateVersion: GateVersionV3, ArtifactChecks: base.ArtifactChecks, Invocation: base.Invocation, Smoke: base.Smoke, ExternalSuites: base.ExternalSuites, Suites: suites}
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
			p := &PublisherV3{cosignDigest: evidenceFixture().ToolSmoke.Policy.Cosign.Digest, newAttester: func(string) (qualificationAttesterV3, error) { calls++; return nil, nil }}
			result, err := p.publishEvidenceV3(ctx, evidence, &PublicationV3{}, func() error {
				_, err := collectSuitesV3(ctx, checks.description, policy, []*SuiteReceipt{receipt})
				return err
			})
			if err == nil || calls != 0 || result.Successful {
				t.Fatalf("revoked gate reached signing: %v %d", err, calls)
			}
		})
	}
}

func TestSuiteV3UnselectedSkippedCasesAreNotRequiredCoverage(t *testing.T) {
	for _, requiredSkip := range []bool{false, true} {
		t.Run(fmt.Sprintf("required-skip-%t", requiredSkip), func(t *testing.T) {
			fixture, checks, r := suiteTestV3(t)
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
			rebindSuiteFixtureV3(t, fixture, &r)
			receipt, err := authenticateSuiteV3(t.Context(), fixture, checks, r)
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
func TestSuiteV3NativeChildMembership(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testobservation.Subject)
	}{
		{"arm64 child only", nil},
		{"all native children", func(_ *testobservation.Subject) {}},
		{"wrong platform", func(s *testobservation.Subject) { s.Platform = "linux/riscv64" }},
		{"wrong native build", func(s *testobservation.Subject) { s.BuildDigest = "sha256:" + strings.Repeat("f", 64) }},
		{"sibling payload", func(s *testobservation.Subject) { s.PayloadDigest = "sha256:" + strings.Repeat("b", 64) }},
		{"native manifest as payload", func(s *testobservation.Subject) { s.PayloadDigest = "sha256:" + strings.Repeat("d", 64) }},
		{"archive as native artifact", func(s *testobservation.Subject) { s.ArtifactDigest = "sha256:" + strings.Repeat("2", 64) }},
		{"aggregate artifact substituted", func(s *testobservation.Subject) { s.ArtifactDigest = "sha256:" + strings.Repeat("a", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, checks, r := suiteTestV3(t)
			pin := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
			children := []delivery.ImageIndexChildBinding{
				{Platform: "linux/amd64", ManifestDigest: pin("b"), Size: 100, BuildDigest: pin("c"), Component: "image", BuildSubject: pin("1")},
				{Platform: "linux/arm64", ManifestDigest: pin("d"), Size: 100, BuildDigest: pin("e"), Component: "image", BuildSubject: pin("2")},
			}
			output := &checks.description.Outputs[0]
			output.Kind = lifecycle.OutputOCI
			output.Platform = ""
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
			if tc.name != "arm64 child only" {
				fixture.observation.Subjects = append([]testobservation.Subject{{Output: output.Name, Platform: children[0].Platform, ArtifactDigest: children[0].ManifestDigest, BuildDigest: children[0].BuildDigest, PayloadDigest: children[0].BuildSubject}}, fixture.observation.Subjects...)
			}
			rebindSuiteFixtureV3(t, fixture, &r)
			receipt, err := authenticateSuiteV3(t.Context(), fixture, checks, r)
			if tc.name != "all native children" {
				requireCode(t, err, e.CodeGateUnmet)
				message := "suite native subject differs from authenticated image-index child"
				if tc.name == "arm64 child only" {
					message = "required suite omits an exact native image-index child"
				}
				if !strings.Contains(err.Error(), message) {
					t.Fatalf("wrong binding was not reached: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := receipt.require(t.Context(), checks.description); err != nil {
					t.Fatal(err)
				}
				// Suite acceptance cannot substitute for a missing native smoke receipt.
				_, _, err = collectSmoke(checks.description, evidenceFixture().Invocation, nil, evidenceFixture().ToolSmoke.PolicyDigest)
				requireCode(t, err, e.CodeGateUnmet)
			}
		})
	}
}

func TestSuiteV3MultiComponentDeliveredOutputCoverage(t *testing.T) {
	for _, kind := range []string{lifecycle.OutputExecutable, lifecycle.OutputDiskImage} {
		for _, tc := range []struct {
			name    string
			edit    func(*testobservation.Subject)
			missing bool
			message string
		}{
			{name: "first material"},
			{name: "second material", edit: func(s *testobservation.Subject) { s.PayloadDigest = "sha256:" + strings.Repeat("b", 64) }},
			{name: "missing named output", missing: true, message: "required suite omits an exact named output payload"},
			{name: "wrong artifact", edit: func(s *testobservation.Subject) { s.ArtifactDigest = "sha256:" + strings.Repeat("f", 64) }, message: "suite subject differs from exact named artifact"},
			{name: "wrong aggregate build", edit: func(s *testobservation.Subject) { s.BuildDigest = "sha256:" + strings.Repeat("f", 64) }, message: "suite subject differs from exact named artifact"},
			{name: "wrong material", edit: func(s *testobservation.Subject) { s.PayloadDigest = "sha256:" + strings.Repeat("f", 64) }, message: "suite subject has no matching exact acquired payload"},
			{name: "wrong platform", edit: func(s *testobservation.Subject) { s.Platform = "linux/arm64" }, message: "suite subject differs from exact named artifact"},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				fixture, checks, requirement := suiteTestV3(t)
				output := &checks.description.Outputs[0]
				output.Kind = kind
				output.Platform = "linux/amd64"
				output.BuildSubjects = []delivery.BuildSubject{{Component: "a-tool", Digest: output.BuildSubjects[0].Digest}, {Component: "z-material", Digest: "sha256:" + strings.Repeat("b", 64)}}
				if kind == lifecycle.OutputDiskImage {
					output.DiskEvidence = "producer-only; content inspection not performed"
				}
				if tc.edit != nil {
					tc.edit(&fixture.observation.Subjects[0])
				}
				if tc.missing {
					fixture.observation.Subjects = fixture.observation.Subjects[1:]
				}
				rebindSuiteFixtureV3(t, fixture, &requirement)
				receipt, err := authenticateSuiteV3(t.Context(), fixture, checks, requirement)
				if tc.message != "" {
					requireCode(t, err, e.CodeGateUnmet)
					if receipt != nil || !strings.Contains(err.Error(), tc.message) {
						t.Fatalf("wrong output binding was not reached: receipt=%v error=%v", receipt, err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if _, err := receipt.require(t.Context(), checks.description); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
