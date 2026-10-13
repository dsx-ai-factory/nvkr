// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

type invocationFixtureV2 struct {
	description qualificationorigin.Description
	source      qualificationorigin.Description
	closed      bool
}

func (f *invocationFixtureV2) Description() (qualificationorigin.Description, error) {
	if f.closed {
		return qualificationorigin.Description{}, e.New(e.CodeGateUnmet, "invocation closed")
	}
	return f.description, nil
}
func (f *invocationFixtureV2) RequireSource(source qualificationorigin.Description) error {
	if f.closed {
		return e.New(e.CodeGateUnmet, "invocation closed")
	}
	if source != f.source {
		return e.New(e.CodeVerifyFailed, "invocation source differs")
	}
	return nil
}

func publisherFixtureV2(t *testing.T) (*suiteFixtureV2, *suiteSourceV2, *invocationFixtureV2, *SmokeReceipt, PolicyV2, []*SuiteReceipt) {
	t.Helper()
	fixture, checks, requirement := suiteTestV2(t)
	receipt, err := authenticateSuiteV2(t.Context(), fixture, checks, requirement)
	if err != nil {
		t.Fatal(err)
	}
	base := evidenceFixture()
	raw, err := canonical.Marshal(checks.description)
	if err != nil {
		t.Fatal(err)
	}
	smoke := &SmokeReceipt{artifactChecks: string(raw), invocation: base.Invocation, verdicts: base.Smoke, closed: new(atomic.Bool)}
	return fixture, checks, &invocationFixtureV2{description: base.Invocation, source: checks.description.Origin}, smoke, PolicyV2{RequiredSuites: []SuiteRequirement{requirement}}, []*SuiteReceipt{receipt}
}

func TestPublisherV2ProductionRecheckRejectsRevocation(t *testing.T) {
	for _, stage := range []int{0, 1, 2} {
		for _, kind := range []string{"source", "invocation", "smoke", "suite", "observation", "cancellation", "attester"} {
			t.Run(kind+string(rune('0'+stage)), func(t *testing.T) {
				fixture, checks, invocation, smoke, policy, receipts := publisherFixtureV2(t)
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
				p := &PublisherV2{newAttester: func(string) (qualificationAttesterV2, error) {
					factories++
					return attesterV2Func(func(context.Context, sign.Subject, []byte) error {
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
				result, err := p.publishReceiptsV2(ctx, checks, invocation, smoke, policy, receipts)
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

func TestSuiteV2CompleteCoverageBeforeAnyAttestation(t *testing.T) {
	fixture, checks, invocation, smoke, policy, receipts := publisherFixtureV2(t)
	fixture.observation.Subjects = fixture.observation.Subjects[:1]
	r := policy.RequiredSuites[0]
	rebindSuiteFixtureV2(t, fixture, &r)
	receipt, err := authenticateSuiteV2(t.Context(), fixture, checks, r)
	requireCode(t, err, e.CodeGateUnmet)
	if receipt != nil {
		t.Fatal("uncovered sibling received suite authority")
	}
	claim, _, _ := twoSuiteEvidenceFixtureV2(t)
	claim.Suites[0].Requirement.Expected.Subjects = claim.Suites[0].Requirement.Expected.Subjects[:1]
	factories := 0
	publisher := &PublisherV2{newAttester: func(string) (qualificationAttesterV2, error) { factories++; return nil, nil }}
	_, err = publisher.publishEvidenceV2(t.Context(), claim, &PublicationV2{}, func() error { t.Fatal("invalid coverage reached authority recheck"); return nil })
	requireCode(t, err, e.CodeGateUnmet)
	if factories != 0 {
		t.Fatal("uncovered sibling constructed signer")
	}
	// Existing receipt becomes stale when its original authenticated observation
	// changes; the public production delegate must still fail with no signer.
	calls := 0
	p := &PublisherV2{newAttester: func(string) (qualificationAttesterV2, error) { calls++; return nil, nil }}
	_, err = p.publishReceiptsV2(t.Context(), checks, invocation, smoke, policy, receipts)
	requireCode(t, err, e.CodeVerifyFailed)
	if calls != 0 {
		t.Fatal("uncovered sibling signed")
	}
}

func TestSuiteV2IndependentBindingRejections(t *testing.T) {
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
			f, s, r := suiteTestV2(t)
			tc.edit(&f.observation, &r)
			rebindSuiteFixtureV2(t, f, &r)
			_, err := authenticateSuiteV2(t.Context(), f, s, r)
			requireCode(t, err, tc.code)
			if !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("wrong rejection: %v", err)
			}
		})
	}
	f, s, r := suiteTestV2(t)
	receipt, err := authenticateSuiteV2(t.Context(), f, s, r)
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

func twoSuiteEvidenceFixtureV2(t *testing.T) (EvidenceV2, PolicyV2, []*SuiteReceipt) {
	t.Helper()
	var policy PolicyV2
	var receipts []*SuiteReceipt
	var checks lifecycle.ArtifactCheckDescription
	for _, name := range []string{"z-conformance", "a-conformance"} {
		f, s, r := suiteTestV2(t)
		f.observation.Suite.Name = name
		rebindSuiteFixtureV2(t, f, &r)
		receipt, err := authenticateSuiteV2(t.Context(), f, s, r)
		if err != nil {
			t.Fatal(err)
		}
		checks = s.description
		policy.RequiredSuites = append(policy.RequiredSuites, r)
		receipts = append(receipts, receipt)
	}
	suites, err := collectSuitesV2(t.Context(), checks, policy, receipts)
	if err != nil {
		t.Fatal(err)
	}
	base := evidenceFixture()
	return EvidenceV2{SchemaVersion: SchemaVersionV2, GateVersion: GateVersionV2, ArtifactChecks: checks, Invocation: base.Invocation, Smoke: base.Smoke, ExternalSuites: []ExternalSuite{}, Suites: suites}, policy, receipts
}

func TestQualificationV2MultiSuiteAdmission(t *testing.T) {
	evidence, policy, receipts := twoSuiteEvidenceFixtureV2(t)
	if evidence.Suites[0].Name != "a-conformance" || evidence.Suites[1].Name != "z-conformance" {
		t.Fatalf("not sorted: %+v", evidence.Suites)
	}
	for _, tc := range []struct {
		name    string
		edit    func(*PolicyV2, *[]*SuiteReceipt)
		code    e.Code
		message string
	}{
		{"missing", func(_ *PolicyV2, r *[]*SuiteReceipt) { *r = (*r)[:1] }, e.CodeGateUnmet, "incomplete"},
		{"duplicate receipt", func(_ *PolicyV2, r *[]*SuiteReceipt) { (*r)[1] = (*r)[0] }, e.CodeGateUnmet, "duplicate suite receipt"},
		{"missing named receipt", func(p *PolicyV2, _ *[]*SuiteReceipt) { p.RequiredSuites[0].Expected.Suite.Name = "other" }, e.CodeGateUnmet, "missing"},
		{"duplicate policy", func(p *PolicyV2, _ *[]*SuiteReceipt) { p.RequiredSuites[1] = p.RequiredSuites[0] }, e.CodeTrustPolicyMissing, "duplicate"},
		{"required outside overlap", func(p *PolicyV2, _ *[]*SuiteReceipt) {
			p.OutsideMVPSuites = []string{p.RequiredSuites[0].Expected.Suite.Name}
		}, e.CodeTrustPolicyMissing, "outside scope"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := canonical.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			var detached PolicyV2
			if err := canonical.UnmarshalExact(raw, &detached); err != nil {
				t.Fatal(err)
			}
			selected := append([]*SuiteReceipt(nil), receipts...)
			tc.edit(&detached, &selected)
			_, err = collectSuitesV2(t.Context(), evidence.ArtifactChecks, detached, selected)
			requireCode(t, err, tc.code)
			if !strings.Contains(err.Error(), tc.message) {
				t.Fatal(err)
			}
		})
	}
}

func TestQualificationV2EvidenceNegativeContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*EvidenceV2)
		code e.Code
	}{
		{"schema", func(v *EvidenceV2) { v.SchemaVersion = "unknown" }, e.CodeVerifyFailed},
		{"gate", func(v *EvidenceV2) { v.GateVersion = "unknown" }, e.CodeVerifyFailed},
		{"no suites", func(v *EvidenceV2) { v.Suites = nil }, e.CodeGateUnmet},
		{"too many suites", func(v *EvidenceV2) { v.Suites = make([]SuiteEvidenceV2, 257) }, e.CodeGateUnmet},
		{"unsorted", func(v *EvidenceV2) { v.Suites[0], v.Suites[1] = v.Suites[1], v.Suites[0] }, e.CodeGateUnmet},
		{"duplicate", func(v *EvidenceV2) { v.Suites[1] = v.Suites[0] }, e.CodeGateUnmet},
		{"required outside overlap", func(v *EvidenceV2) {
			v.ExternalSuites = []ExternalSuite{{Name: v.Suites[0].Name, Status: "not-evaluated", Reason: "outside MVP scope"}}
		}, e.CodeGateUnmet},
		{"uncovered sibling", func(v *EvidenceV2) {
			v.Suites[0].Requirement.Expected.Subjects = v.Suites[0].Requirement.Expected.Subjects[:1]
		}, e.CodeGateUnmet},
		{"failed verdict", func(v *EvidenceV2) { v.Suites[0].Status = "fail" }, e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, _, _ := twoSuiteEvidenceFixtureV2(t)
			tc.edit(&v)
			requireCode(t, v.Validate(), tc.code)
			raw, err := canonical.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			_, err = DecodeV2(raw)
			requireCode(t, err, tc.code)
		})
	}
	_, err := DecodeV2([]byte(strings.Repeat(" ", maxEvidenceV2Bytes+1)))
	requireCode(t, err, e.CodeVerifyFailed)
}

func TestPublisherV2ProductionDelegateCompleteCoverage(t *testing.T) {
	_, checks, invocation, smoke, policy, receipts := publisherFixtureV2(t)
	calls := 0
	p := &PublisherV2{newAttester: func(string) (qualificationAttesterV2, error) {
		return attesterV2Func(func(_ context.Context, subject sign.Subject, raw []byte) error {
			calls++
			evidence, err := DecodeV2(raw)
			if err != nil {
				return err
			}
			if subject.Ref != checks.description.Outputs[calls-1].Reference || len(evidence.Suites[0].Requirement.Expected.Subjects) != len(checks.description.Outputs) {
				t.Fatal("attestation lacks exact output coverage")
			}
			return nil
		}), nil
	}}
	result, err := p.publishReceiptsV2(t.Context(), checks, invocation, smoke, policy, receipts)
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
