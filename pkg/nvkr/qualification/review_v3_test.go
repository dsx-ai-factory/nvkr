// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

type invocationFixtureV3 struct {
	description qualificationorigin.Description
	source      qualificationorigin.Description
	closed      bool
}

func (f *invocationFixtureV3) Description() (qualificationorigin.Description, error) {
	if f.closed {
		return qualificationorigin.Description{}, e.New(e.CodeGateUnmet, "invocation closed")
	}
	return f.description, nil
}
func (f *invocationFixtureV3) RequireSource(source qualificationorigin.Description) error {
	if f.closed {
		return e.New(e.CodeGateUnmet, "invocation closed")
	}
	if source != f.source {
		return e.New(e.CodeVerifyFailed, "invocation source differs")
	}
	return nil
}

func publisherFixtureV3(t *testing.T) (*suiteFixtureV3, *suiteSourceV3, *invocationFixtureV3, *smokeFixtureV3, PolicyV3, []*SuiteReceipt) {
	t.Helper()
	fixture, checks, requirement := suiteTestV3(t)
	receipt, err := authenticateSuiteV3(t.Context(), fixture, checks, requirement)
	if err != nil {
		t.Fatal(err)
	}
	base := evidenceFixture()
	smoke := &smokeFixtureV3{description: base.ToolSmoke}
	return fixture, checks, &invocationFixtureV3{description: base.Invocation, source: checks.description.Origin}, smoke, PolicyV3{SchemaVersion: PolicySchemaV3, NativePolicyDigest: evidenceFixture().ToolSmoke.PolicyDigest, RequiredSuites: []SuiteRequirement{requirement}}, []*SuiteReceipt{receipt}
}

func TestPublisherV3ProductionRecheckRejectsRevocation(t *testing.T) {
	for _, stage := range []int{0, 1, 2} {
		for _, kind := range []string{"source", "invocation", "smoke", "suite", "observation", "cancellation", "attester"} {
			t.Run(kind+string(rune('0'+stage)), func(t *testing.T) {
				fixture, checks, invocation, smoke, policy, receipts := publisherFixtureV3(t)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				revoke := func() {
					switch kind {
					case "source":
						checks.closed = true
					case "invocation":
						invocation.closed = true
					case "smoke":
						_ = smoke.Close()
					case "suite":
						_ = receipts[0].Close()
					case "observation":
						fixture.closed = true
					case "cancellation":
						cancel()
					}
				}
				factories, calls := 0, 0
				p := &PublisherV3{cosignDigest: evidenceFixture().ToolSmoke.Policy.Cosign.Digest, newAttester: func(string) (qualificationAttesterV3, error) {
					factories++
					return attesterV3Func(func(context.Context, sign.Subject, []byte) error {
						calls++
						if calls == stage {
							if kind == "attester" {
								return e.New(e.CodeSignatureInvalid, "attachment failed")
							}
							revoke()
						}
						return nil
					}), nil
				}}
				if stage == 0 {
					if kind == "attester" {
						return
					}
					revoke()
				}
				result, err := p.publishReceiptsV3(ctx, checks, invocation, smoke, policy, receipts)
				code := e.CodeGateUnmet
				if kind == "cancellation" {
					code = e.CodeTimeout
				}
				requireCode(t, err, code)
				if calls != stage || (stage == 0 && factories != 0) {
					t.Fatalf("signing calls=%d factories=%d want %d", calls, factories, stage)
				}
				published := stage
				if kind == "attester" {
					published--
				}
				if result != nil {
					if result.Successful || len(result.PublishedSubjects) != published {
						t.Fatalf("partial result=%+v", result)
					}
				} else if published != 0 {
					t.Fatal("lost partial publication")
				}
			})
		}
	}
}

func TestSuiteV3CompleteCoverageBeforeAnyAttestation(t *testing.T) {
	fixture, checks, invocation, smoke, policy, receipts := publisherFixtureV3(t)
	fixture.observation.Subjects = fixture.observation.Subjects[:1]
	r := policy.RequiredSuites[0]
	rebindSuiteFixtureV3(t, fixture, &r)
	receipt, err := authenticateSuiteV3(t.Context(), fixture, checks, r)
	requireCode(t, err, e.CodeGateUnmet)
	if receipt != nil {
		t.Fatal("uncovered sibling received suite authority")
	}
	claim, _, _ := twoSuiteEvidenceFixtureV3(t)
	claim.Suites[0].Requirement.Expected.Subjects = claim.Suites[0].Requirement.Expected.Subjects[:1]
	factories := 0
	publisher := &PublisherV3{cosignDigest: evidenceFixture().ToolSmoke.Policy.Cosign.Digest, newAttester: func(string) (qualificationAttesterV3, error) { factories++; return nil, nil }}
	_, err = publisher.publishEvidenceV3(t.Context(), claim, &PublicationV3{}, func() error { t.Fatal("invalid coverage reached authority recheck"); return nil })
	requireCode(t, err, e.CodeGateUnmet)
	if factories != 0 {
		t.Fatal("uncovered sibling constructed signer")
	}
	// Existing receipt becomes stale when its original authenticated observation
	// changes; the public production delegate must still fail with no signer.
	calls := 0
	p := &PublisherV3{cosignDigest: evidenceFixture().ToolSmoke.Policy.Cosign.Digest, newAttester: func(string) (qualificationAttesterV3, error) { calls++; return nil, nil }}
	_, err = p.publishReceiptsV3(t.Context(), checks, invocation, smoke, policy, receipts)
	requireCode(t, err, e.CodeVerifyFailed)
	if calls != 0 {
		t.Fatal("uncovered sibling signed")
	}
}

func TestSuiteV3IndependentBindingRejections(t *testing.T) {
	pin := "sha256:" + strings.Repeat("b", 64)
	for _, tc := range []struct {
		name    string
		edit    func(*testobservation.Observation, *SuiteRequirement)
		code    e.Code
		message string
	}{
		{"definition", func(o *testobservation.Observation, _ *SuiteRequirement) { o.Inputs.DefinitionDigest = pin }, e.CodeGateUnmet, "input bindings"},
		{"environment", func(o *testobservation.Observation, _ *SuiteRequirement) { o.Inputs.EnvironmentDigest = pin }, e.CodeGateUnmet, "input bindings"},
		{"plan", func(o *testobservation.Observation, _ *SuiteRequirement) { o.Inputs.PlanDigest = pin }, e.CodeGateUnmet, "input bindings"},
		{"publication", func(o *testobservation.Observation, _ *SuiteRequirement) { o.Inputs.PublicationDigest = pin }, e.CodeGateUnmet, "input bindings"},
		{"payload", func(o *testobservation.Observation, _ *SuiteRequirement) { o.Subjects[0].PayloadDigest = pin }, e.CodeGateUnmet, "acquired payload"},
		{"output name", func(o *testobservation.Observation, _ *SuiteRequirement) { o.Subjects[0].Output = "chart-0" }, e.CodeGateUnmet, "acquired payload"},
		{"build", func(o *testobservation.Observation, _ *SuiteRequirement) { o.Subjects[0].BuildDigest = pin }, e.CodeGateUnmet, "exact named artifact"},
		{"local observation", func(o *testobservation.Observation, _ *SuiteRequirement) {
			o.Environment.Role = "local"
			o.Origin = testobservation.Origin{Status: "unknown"}
		}, e.CodeGateUnmet, "origin, role or verdict"},
		{"unknown origin", func(o *testobservation.Observation, _ *SuiteRequirement) {
			o.Origin = testobservation.Origin{Status: "unknown"}
		}, e.CodeGateUnmet, "origin, role or verdict"},
		{"repository", func(o *testobservation.Observation, _ *SuiteRequirement) { o.Origin.Repository = "other/repo" }, e.CodeGateUnmet, "origin, role or verdict"},
		{"commit", func(o *testobservation.Observation, _ *SuiteRequirement) { o.Origin.Commit = strings.Repeat("b", 40) }, e.CodeGateUnmet, "origin, role or verdict"},
		{"workflow", func(o *testobservation.Observation, _ *SuiteRequirement) {
			o.Origin.Workflow = ".github/workflows/other.yml"
		}, e.CodeGateUnmet, "origin, role or verdict"},
		{"local requirement", func(_ *testobservation.Observation, r *SuiteRequirement) { r.RequiredRole = "local" }, e.CodeTrustPolicyMissing, "site or tenant"},
		{"unknown requirement", func(_ *testobservation.Observation, r *SuiteRequirement) { r.RequiredRole = "unknown" }, e.CodeTrustPolicyMissing, "site or tenant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, s, r := suiteTestV3(t)
			tc.edit(&f.observation, &r)
			rebindSuiteFixtureV3(t, f, &r)
			_, err := authenticateSuiteV3(t.Context(), f, s, r)
			requireCode(t, err, tc.code)
			if !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("wrong rejection: %v", err)
			}
		})
	}
	f, s, r := suiteTestV3(t)
	receipt, err := authenticateSuiteV3(t.Context(), f, s, r)
	if err != nil {
		t.Fatal(err)
	}
	target := s.description
	target.CatalogDigest = pin
	_, err = receipt.require(t.Context(), target)
	requireCode(t, err, e.CodeGateUnmet)
	if !strings.Contains(err.Error(), "source changed or differs") {
		t.Fatal(err)
	}
	s.description.CatalogDigest = pin
	_, err = receipt.require(t.Context(), s.description)
	requireCode(t, err, e.CodeGateUnmet)
}

func twoSuiteEvidenceFixtureV3(t *testing.T) (EvidenceV3, PolicyV3, []*SuiteReceipt) {
	t.Helper()
	policy := PolicyV3{SchemaVersion: PolicySchemaV3, NativePolicyDigest: evidenceFixture().ToolSmoke.PolicyDigest}
	var receipts []*SuiteReceipt
	var checks lifecycle.ArtifactCheckDescription
	for _, name := range []string{"z-conformance", "a-conformance"} {
		f, s, r := suiteTestV3(t)
		f.observation.Suite.Name = name
		rebindSuiteFixtureV3(t, f, &r)
		receipt, err := authenticateSuiteV3(t.Context(), f, s, r)
		if err != nil {
			t.Fatal(err)
		}
		checks = s.description
		policy.RequiredSuites = append(policy.RequiredSuites, r)
		receipts = append(receipts, receipt)
	}
	suites, err := collectSuitesV3(t.Context(), checks, policy, receipts)
	if err != nil {
		t.Fatal(err)
	}
	base := evidenceFixture()
	return EvidenceV3{ToolSmoke: base.ToolSmoke, Policy: policyForSuiteEvidenceV3(t, suites), PolicyDigest: digestForSuiteEvidenceV3(t, suites), SchemaVersion: SchemaVersionV3, GateVersion: GateVersionV3, ArtifactChecks: checks, Invocation: base.Invocation, Smoke: base.Smoke, ExternalSuites: base.ExternalSuites, Suites: suites}, policy, receipts
}

func TestQualificationV3MultiSuiteAdmission(t *testing.T) {
	evidence, policy, receipts := twoSuiteEvidenceFixtureV3(t)
	if evidence.Suites[0].Name != "a-conformance" || evidence.Suites[1].Name != "z-conformance" {
		t.Fatalf("not sorted: %+v", evidence.Suites)
	}
	for _, tc := range []struct {
		name    string
		edit    func(*PolicyV3, *[]*SuiteReceipt)
		code    e.Code
		message string
	}{
		{"missing", func(_ *PolicyV3, r *[]*SuiteReceipt) { *r = (*r)[:1] }, e.CodeGateUnmet, "incomplete"},
		{"duplicate receipt", func(_ *PolicyV3, r *[]*SuiteReceipt) { (*r)[1] = (*r)[0] }, e.CodeGateUnmet, "duplicate suite receipt"},
		{"missing named receipt", func(p *PolicyV3, _ *[]*SuiteReceipt) { p.RequiredSuites[0].Expected.Suite.Name = "other" }, e.CodeGateUnmet, "missing"},
		{"duplicate policy", func(p *PolicyV3, _ *[]*SuiteReceipt) { p.RequiredSuites[1] = p.RequiredSuites[0] }, e.CodeTrustPolicyMissing, "duplicate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := canonical.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			var detached PolicyV3
			if err := canonical.UnmarshalExact(raw, &detached); err != nil {
				t.Fatal(err)
			}
			selected := append([]*SuiteReceipt(nil), receipts...)
			tc.edit(&detached, &selected)
			_, err = collectSuitesV3(t.Context(), evidence.ArtifactChecks, detached, selected)
			requireCode(t, err, tc.code)
			if !strings.Contains(err.Error(), tc.message) {
				t.Fatal(err)
			}
		})
	}
}

func TestQualificationV3EvidenceNegativeContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*EvidenceV3)
		code e.Code
	}{
		{"schema", func(v *EvidenceV3) { v.SchemaVersion = "unknown" }, e.CodeVerifyFailed},
		{"gate", func(v *EvidenceV3) { v.GateVersion = "unknown" }, e.CodeVerifyFailed},
		{"no suites", func(v *EvidenceV3) { v.Suites = nil }, e.CodeGateUnmet},
		{"too many suites", func(v *EvidenceV3) { v.Suites = make([]SuiteEvidenceV3, 257) }, e.CodeGateUnmet},
		{"unsorted", func(v *EvidenceV3) { v.Suites[0], v.Suites[1] = v.Suites[1], v.Suites[0] }, e.CodeGateUnmet},
		{"duplicate", func(v *EvidenceV3) { v.Suites[1] = v.Suites[0] }, e.CodeGateUnmet},
		{"required outside overlap", func(v *EvidenceV3) {
			v.ExternalSuites = []ExternalSuite{{Name: v.Suites[0].Name, Status: "not-evaluated", Reason: "outside MVP scope"}}
		}, e.CodeGateUnmet},
		{"uncovered sibling", func(v *EvidenceV3) {
			v.Suites[0].Requirement.Expected.Subjects = v.Suites[0].Requirement.Expected.Subjects[:1]
		}, e.CodeGateUnmet},
		{"failed verdict", func(v *EvidenceV3) { v.Suites[0].Status = "fail" }, e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, _, _ := twoSuiteEvidenceFixtureV3(t)
			tc.edit(&v)
			requireCode(t, v.Validate(), tc.code)
			raw, err := canonical.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			_, err = DecodeV3(raw)
			requireCode(t, err, tc.code)
		})
	}
	_, err := DecodeV3([]byte(strings.Repeat(" ", maxEvidenceV3Bytes+1)))
	requireCode(t, err, e.CodeVerifyFailed)
}

func TestPublisherV3ProductionDelegateCompleteCoverage(t *testing.T) {
	_, checks, invocation, smoke, policy, receipts := publisherFixtureV3(t)
	calls := 0
	p := &PublisherV3{cosignDigest: evidenceFixture().ToolSmoke.Policy.Cosign.Digest, newAttester: func(string) (qualificationAttesterV3, error) {
		return attesterV3Func(func(_ context.Context, subject sign.Subject, raw []byte) error {
			calls++
			evidence, err := DecodeV3(raw)
			if err != nil {
				return err
			}
			if subject.Ref != checks.description.Outputs[calls-1].Reference || len(evidence.Suites[0].Requirement.Expected.Subjects) != len(checks.description.Outputs) {
				t.Fatal("attestation lacks exact output coverage")
			}
			return nil
		}), nil
	}}
	result, err := p.publishReceiptsV3(t.Context(), checks, invocation, smoke, policy, receipts)
	if err != nil || !result.Successful || calls != 2 || len(result.PublishedSubjects) != 2 {
		t.Fatalf("complete publication=%+v error=%v calls=%d", result, err, calls)
	}
	// The exported boundary still rejects caller-authored zero capabilities; no
	// test seam can be selected through public constructor options.
	_, err = p.Publish(t.Context(), &lifecycle.ArtifactCheckReceipt{}, nil, nil, policy, receipts)
	requireCode(t, err, e.CodeGateUnmet)
	if calls != 2 {
		t.Fatal("zero artifact capability acquired signer")
	}
}

func policyForSuiteEvidenceV3(t *testing.T, suites []SuiteEvidenceV3) PolicyV3 {
	t.Helper()
	p := PolicyV3{SchemaVersion: PolicySchemaV3, NativePolicyDigest: evidenceFixture().ToolSmoke.PolicyDigest}
	for _, suite := range suites {
		p.RequiredSuites = append(p.RequiredSuites, suite.Requirement)
	}
	return p
}
func digestForSuiteEvidenceV3(t *testing.T, suites []SuiteEvidenceV3) string {
	t.Helper()
	digest, err := policyForSuiteEvidenceV3(t, suites).Digest()
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

// Private fixture exercises the actual production publisher's native admission
// and recheck delegate. No exported constructor can issue native authority.
type smokeFixtureV3 struct {
	description lifecycle.ToolSmokeDescription
	closed      bool
}

func (f *smokeFixtureV3) Close() error { f.closed = true; return nil }
func (f *smokeFixtureV3) Description() (lifecycle.ToolSmokeDescription, error) {
	if f.closed {
		return lifecycle.ToolSmokeDescription{}, e.New(e.CodeGateUnmet, "native smoke closed")
	}
	return f.description, nil
}
func (f *smokeFixtureV3) Require(checks lifecycle.ArtifactCheckDescription, invocation qualificationorigin.Description, digest string) error {
	if f.closed {
		return e.New(e.CodeGateUnmet, "native smoke closed")
	}
	a, err := canonical.Marshal(checks)
	if err != nil {
		return err
	}
	b, err := canonical.Marshal(f.description.ArtifactChecks)
	if err != nil {
		return err
	}
	if string(a) != string(b) || invocation != f.description.Invocation || digest != f.description.PolicyDigest {
		return e.New(e.CodeGateUnmet, "native smoke scope differs")
	}
	return nil
}

func TestPublisherV3RequiresCompleteNativeSmokeBeforeSigner(t *testing.T) {
	for _, tc := range []struct {
		name   string
		edit   func(*smokeFixtureV3, *PolicyV3, *PublisherV3)
		actual suiteSmokeAuthorityV3
	}{
		{name: "nil native"},
		{name: "zero native receipt", actual: &lifecycle.ToolSmokeReceipt{}},
		{name: "closed native", edit: func(s *smokeFixtureV3, _ *PolicyV3, _ *PublisherV3) { s.closed = true }},
		{name: "missing native worker", edit: func(s *smokeFixtureV3, _ *PolicyV3, _ *PublisherV3) { s.description.Workers = nil }},
		{name: "different native policy", edit: func(_ *smokeFixtureV3, p *PolicyV3, _ *PublisherV3) {
			p.NativePolicyDigest = "sha256:" + strings.Repeat("b", 64)
		}},
		{name: "different signer pin", edit: func(_ *smokeFixtureV3, _ *PolicyV3, p *PublisherV3) {
			p.cosignDigest = "sha256:" + strings.Repeat("b", 64)
		}},
		{name: "different native invocation", edit: func(s *smokeFixtureV3, _ *PolicyV3, _ *PublisherV3) { s.description.Invocation.RunID = "14" }},
		{name: "required suite outside native scope", edit: func(s *smokeFixtureV3, p *PolicyV3, _ *PublisherV3) {
			s.description.Policy.OutsideMVPSuites = []string{p.RequiredSuites[0].Expected.Suite.Name}
			digest, err := s.description.Policy.Digest()
			if err != nil {
				panic(err)
			}
			s.description.PolicyDigest = digest
			p.NativePolicyDigest = digest
			for i := range s.description.Workers {
				s.description.Workers[i].PolicyDigest = digest
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, checks, invocation, smoke, policy, receipts := publisherFixtureV3(t)
			calls := 0
			publisher := &PublisherV3{cosignDigest: evidenceFixture().ToolSmoke.Policy.Cosign.Digest, newAttester: func(string) (qualificationAttesterV3, error) { calls++; return nil, nil }}
			var selected suiteSmokeAuthorityV3 = smoke
			if tc.edit != nil {
				tc.edit(smoke, &policy, publisher)
			} else {
				selected = tc.actual
			}
			result, err := publisher.publishReceiptsV3(t.Context(), checks, invocation, selected, policy, receipts)
			if err == nil || calls != 0 || result != nil && result.Successful {
				t.Fatalf("native refusal lost: result=%+v calls=%d err=%v", result, calls, err)
			}
		})
	}
}
func TestQualificationV3PolicyIdentityAndMandatoryNativeWire(t *testing.T) {
	evidence, policy, _ := twoSuiteEvidenceFixtureV3(t)
	digest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	policy.RequiredSuites[0], policy.RequiredSuites[1] = policy.RequiredSuites[1], policy.RequiredSuites[0]
	reverse, err := policy.Digest()
	if err != nil || reverse != digest {
		t.Fatal("required-suite set normalization changed identity")
	}
	for _, tc := range []struct {
		name string
		edit func(*EvidenceV3)
	}{
		{"missing native claim", func(v *EvidenceV3) { v.ToolSmoke = lifecycle.ToolSmokeDescription{} }},
		{"native worker missing", func(v *EvidenceV3) { v.ToolSmoke.Workers = nil }},
		{"extension policy digest missing", func(v *EvidenceV3) { v.PolicyDigest = "" }},
		{"extension policy substituted", func(v *EvidenceV3) { v.Policy.RequiredSuites[0].Identity = "other" }},
		{"native policy digest substituted", func(v *EvidenceV3) { v.Policy.NativePolicyDigest = "sha256:" + strings.Repeat("b", 64) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := canonical.Marshal(evidence)
			if err != nil {
				t.Fatal(err)
			}
			var v EvidenceV3
			if err := canonical.UnmarshalExact(raw, &v); err != nil {
				t.Fatal(err)
			}
			tc.edit(&v)
			if err := v.Validate(); err == nil {
				t.Fatal("substituted full contract accepted")
			}
		})
	}
}
