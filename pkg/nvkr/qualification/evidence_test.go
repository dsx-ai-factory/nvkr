// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func evidenceFixture() Evidence {
	digest := "sha256:" + strings.Repeat("a", 64)
	origin := qualificationorigin.Description{Repository: "example/nvkr", RunID: "12", Attempt: "1", Commit: strings.Repeat("c", 40), Workflow: ".github/workflows/build.yml", DefaultBranch: "main", Ref: "refs/heads/main", InvocationID: "https://github.com/example/nvkr/actions/runs/12/attempts/1", BuilderID: "https://github.com/example/nvkr/.github/workflows/build.yml@refs/heads/main"}
	invocation := origin
	invocation.RunID = "13"
	invocation.Workflow = ".github/workflows/qualify.yml"
	invocation.InvocationID = "https://github.com/example/nvkr/actions/runs/13/attempts/1"
	invocation.BuilderID = "https://github.com/example/nvkr/.github/workflows/qualify.yml@refs/heads/main"
	evidence := Evidence{SchemaVersion: SchemaVersion, GateVersion: GateVersion, Invocation: invocation, ArtifactChecks: lifecycle.ArtifactCheckDescription{
		GateVersion: lifecycle.ArtifactCheckGateVersion, Origin: origin, CatalogDigest: digest,
		Inputs: lifecycle.ExactInputDescription{ReleaseSubject: "registry.example.test/product@" + digest, PolicyDigest: digest, DefinitionDigest: digest, EnvironmentDigest: digest, PlanDigest: digest, ContentSubject: digest},
	}, ExternalSuites: []ExternalSuite{{Name: "nvke", Status: "not-evaluated", Reason: "outside MVP scope"}}}
	for _, name := range []string{"chart-a", "chart-b"} {
		output := lifecycle.ArtifactCheckOutput{Name: name, Kind: lifecycle.OutputHelm, Reference: "registry.example.test/" + name + "@" + digest, Subject: digest, BuildDigest: digest, Overall: "pass", BuildSubjects: []delivery.BuildSubject{{Component: name, Digest: digest}}}
		for _, check := range requiredArtifactChecks {
			output.Checks = append(output.Checks, lifecycle.ArtifactCheckResult{Name: check, Status: "pass"})
		}
		evidence.ArtifactChecks.Outputs = append(evidence.ArtifactChecks.Outputs, output)
		evidence.Smoke = append(evidence.Smoke, Verdict{Output: name, Subject: digest, Check: "delivered-tool-version", Status: "not-applicable", Reason: "non-tool artifact format"})
	}
	evidence.ArtifactChecks.Outputs[0].Kind = lifecycle.OutputExecutable
	evidence.ArtifactChecks.Outputs[0].Platform = "linux/amd64"
	evidence.Smoke[0].Status = "pass"
	evidence.Smoke[0].Reason = ""
	policy := lifecycle.QualificationPolicy{SchemaVersion: lifecycle.QualificationPolicySchema, Source: qualificationorigin.SourcePolicy{Repository: origin.Repository, Workflow: origin.Workflow, DefaultBranch: origin.DefaultBranch}, QualificationWorkflow: invocation.Workflow, Cosign: lifecycle.QualificationCosignPolicy{Digest: digest}, OutsideMVPSuites: []string{"nvke"}, Smoke: &lifecycle.QualificationSmokePolicy{Workflow: ".github/workflows/smoke.yml", Images: map[string]string{"linux/amd64": "registry.example.test/smoke@" + digest}, Tools: map[string]string{"chart-a": "helm-version"}}}
	policyDigest, err := policy.Digest()
	if err != nil {
		panic(err)
	}
	worker := lifecycle.NativeToolSmokeReport{SchemaVersion: lifecycle.NativeToolSmokeSchema, ArtifactChecks: evidence.ArtifactChecks, Invocation: invocation, PolicyDigest: policyDigest, Platform: "linux/amd64", Image: policy.Smoke.Images["linux/amd64"], RuntimeImage: digest, RuntimeIdentity: digest, Status: "pass", Members: []lifecycle.ToolSmokeMember{{Output: "chart-a", Member: "chart-a", Subject: digest, BuildDigest: digest, Platform: "linux/amd64", PayloadDigest: digest, Size: 100, Profile: "helm-version", ExpectedVersion: "v1.2.3", ObservedVersion: "v1.2.3"}}}
	evidence.ToolSmoke = lifecycle.ToolSmokeDescription{ArtifactChecks: evidence.ArtifactChecks, Invocation: invocation, PolicyDigest: policyDigest, Policy: policy, Workers: []lifecycle.NativeToolSmokeReport{worker}}
	return evidence
}

func requireCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("error=%v, want %s", err, code)
	}
}

func TestQualificationEvidenceStrictContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Evidence)
		code e.Code
	}{
		{"development source", func(v *Evidence) { v.ArtifactChecks.Origin.Branch = "feature/qualification" }, e.CodeVerifyFailed},
		{"development qualifier", func(v *Evidence) { v.Invocation.Branch = "feature/qualification" }, e.CodeVerifyFailed},
		{"development policy", func(v *Evidence) { v.ToolSmoke.Policy.Source.Development = true }, e.CodeVerifyFailed},
		{"unknown schema", func(v *Evidence) { v.SchemaVersion = "nvkr.dev/qualification-evidence/v1" }, e.CodeVerifyFailed},
		{"unknown gate", func(v *Evidence) { v.GateVersion = "other" }, e.CodeVerifyFailed},
		{"builder signs qualification", func(v *Evidence) { v.Invocation = v.ArtifactChecks.Origin }, e.CodeVerifyFailed},
		{"commit absent", func(v *Evidence) { v.ArtifactChecks.Origin.Commit = "" }, e.CodeTrustPolicyMissing},
		{"attempt changed", func(v *Evidence) { v.ArtifactChecks.Origin.Attempt = "2" }, e.CodeVerifyFailed},
		{"input missing", func(v *Evidence) { v.ArtifactChecks.Inputs.PlanDigest = "" }, e.CodeVerifyFailed},
		{"missing output", func(v *Evidence) { v.ArtifactChecks.Outputs = nil }, e.CodeReleaseIncomplete},
		{"duplicate output", func(v *Evidence) { v.ArtifactChecks.Outputs[1] = v.ArtifactChecks.Outputs[0] }, e.CodeGateUnmet},
		{"failed check", func(v *Evidence) { v.ArtifactChecks.Outputs[0].Checks[0].Status = "fail" }, e.CodeGateUnmet},
		{"declared embedded mismatch", func(v *Evidence) {
			for i := range v.ArtifactChecks.Outputs[0].Checks {
				check := &v.ArtifactChecks.Outputs[0].Checks[i]
				if check.Name == "embedded-declarations" {
					check.Status = "fail"
					check.Code = string(e.CodePinSetMismatch)
				}
			}
		}, e.CodeGateUnmet},
		{"missing check", func(v *Evidence) { v.ArtifactChecks.Outputs[0].Checks = nil }, e.CodeGateUnmet},
		{"missing producer subjects", func(v *Evidence) {
			v.ArtifactChecks.Outputs[0].Kind = lifecycle.OutputExecutable
			v.ArtifactChecks.Outputs[0].BuildSubjects = nil
		}, e.CodeVerifyFailed},
		{"duplicate producer subject", func(v *Evidence) {
			o := &v.ArtifactChecks.Outputs[0]
			o.BuildSubjects = append(o.BuildSubjects, o.BuildSubjects[0])
		}, e.CodeVerifyFailed},
		{"malformed producer subject", func(v *Evidence) { v.ArtifactChecks.Outputs[0].BuildSubjects[0].Digest = "latest" }, e.CodeVerifyFailed},
		{"unknown platform", func(v *Evidence) { v.ArtifactChecks.Outputs[0].Platform = "linux/unknown" }, e.CodeVerifyFailed},
		{"index for non-image", func(v *Evidence) { v.ArtifactChecks.Outputs[0].ImageIndex = &delivery.ImageIndexBinding{} }, e.CodeVerifyFailed},
		{"missing index children", func(v *Evidence) {
			o := &v.ArtifactChecks.Outputs[0]
			o.Kind = lifecycle.OutputOCI
			o.BuildSubjects = nil
			o.ImageIndex = &delivery.ImageIndexBinding{RequiredPlatforms: []string{"linux/amd64"}}
		}, e.CodeVerifyFailed},
		{"waiver", func(v *Evidence) { v.ArtifactChecks.Outputs[0].Checks[0].Status = "waived" }, e.CodeGateUnmet},
		{"required check not applicable", func(v *Evidence) { v.ArtifactChecks.Outputs[0].Checks[0].Status = "not-applicable" }, e.CodeGateUnmet},
		{"wrong smoke subject", func(v *Evidence) { v.Smoke[0].Subject = "sha256:" + strings.Repeat("b", 64) }, e.CodeGateUnmet},
		{"executable skipped", func(v *Evidence) { v.Smoke[0].Status = "not-applicable" }, e.CodeGateUnmet},
		{"missing worker", func(v *Evidence) { v.ToolSmoke.Workers = nil }, e.CodeReleaseIncomplete},
		{"disk inspected", func(v *Evidence) {
			v.ArtifactChecks.Outputs[0].Kind = lifecycle.OutputDiskImage
			v.ArtifactChecks.Outputs[0].DiskEvidence = "strict"
		}, e.CodeVerifyFailed},
		{"external suite fabricated", func(v *Evidence) { v.ExternalSuites[0].Status = "pass" }, e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence := evidenceFixture()
			tc.edit(&evidence)
			requireCode(t, evidence.Validate(), tc.code)
		})
	}
	good := evidenceFixture()
	// An omitted upstream metadata declaration is explicitly not applicable.
	// Keep that fact through the exact worker evidence and canonical wire claim.
	for i := range good.ArtifactChecks.Outputs {
		for j := range good.ArtifactChecks.Outputs[i].Checks {
			check := &good.ArtifactChecks.Outputs[i].Checks[j]
			if check.Name == "embedded-declarations" {
				check.Status = "not-applicable"
			}
		}
	}
	raw, err := good.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := good.Digest()
	got, _ := decoded.Digest()
	if got != want {
		t.Fatal("evidence identity changed")
	}
	for _, raw := range [][]byte{append(append([]byte(nil), raw...), '\n'), []byte(strings.Replace(string(raw), `"gateVersion":`, `"unknown":1,"gateVersion":`, 1))} {
		if _, err := Decode(raw); err == nil {
			t.Fatal("noncanonical or unknown field accepted")
		}
	}
}

func TestSmokeAdmissionCollectsCompleteSetAndRejectsFabrication(t *testing.T) {
	for _, kind := range []string{lifecycle.OutputHelm, lifecycle.OutputDiskImage, lifecycle.OutputExecutable, lifecycle.OutputOCI, lifecycle.OutputAPT} {
		t.Run(kind, func(t *testing.T) {
			fixture := evidenceFixture()
			for i := range fixture.ArtifactChecks.Outputs {
				fixture.ArtifactChecks.Outputs[i].Kind = kind
			}
			verdicts, _, err := collectSmoke(fixture.ArtifactChecks, fixture.Invocation, nil, fixture.ToolSmoke.PolicyDigest)
			requireCode(t, err, e.CodeGateUnmet)
			if len(verdicts) != len(fixture.ArtifactChecks.Outputs) {
				t.Fatal("short-circuited missing evidence diagnostics")
			}
			for _, verdict := range verdicts {
				if verdict.Status != "fail" {
					t.Fatal("missing complete B claimed success")
				}
			}
		})
	}
	fixture := evidenceFixture()
	for _, receipt := range []*SmokeReceipt{nil, {}} {
		_, _, err := collectSmoke(fixture.ArtifactChecks, fixture.Invocation, receipt, fixture.ToolSmoke.PolicyDigest)
		requireCode(t, err, e.CodeGateUnmet)
	}
	verdicts, err := smokeVerdicts(fixture.ToolSmoke)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(verdicts, fixture.Smoke) {
		t.Fatal("diagnostic projection differs from full B claim")
	}
}

type attesterFunc func(context.Context, sign.Subject, []byte) error

func (f attesterFunc) AttestQualification(ctx context.Context, subject sign.Subject, raw []byte) error {
	return f(ctx, subject, raw)
}

func TestPublicationFailureNeverReportsSuccessfulQualification(t *testing.T) {
	for _, tc := range []struct {
		name          string
		failAt        int
		wantPublished int
	}{
		{"all verified", 0, 2}, {"first attachment fails", 1, 0}, {"later attachment fails", 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := &Publisher{newAttester: func(identity string) (qualificationAttester, error) {
				if identity != evidenceFixture().Invocation.BuilderID {
					t.Fatal("not qualifier identity")
				}
				return attesterFunc(func(_ context.Context, subject sign.Subject, raw []byte) error {
					calls++
					evidence, err := Decode(raw)
					if err != nil {
						return err
					}
					if subject.Ref != evidence.ArtifactChecks.Outputs[calls-1].Reference {
						t.Fatal("different subject")
					}
					if calls == tc.failAt {
						return e.New(e.CodeSignatureInvalid, "fixture attachment failure")
					}
					return nil
				}), nil
			}}
			result, err := p.publishEvidence(t.Context(), evidenceFixture(), &Publication{}, func() error { return nil })
			if tc.failAt != 0 {
				requireCode(t, err, e.CodeGateUnmet)
				if result.Successful {
					t.Fatal("partial attachments claimed success")
				}
			} else if err != nil || !result.Successful {
				t.Fatalf("complete set not successful: %v", err)
			}
			if len(result.PublishedSubjects) != tc.wantPublished {
				t.Fatalf("published=%v", result.PublishedSubjects)
			}
		})
	}
}

func TestPublicationInterruptionAtAttachmentBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name        string
		interruptAt int
		revoke      bool
		code        e.Code
	}{
		{"receipt revoked between attachments", 1, true, e.CodeGateUnmet},
		{"receipt revoked during final attachment", 2, true, e.CodeGateUnmet},
		{"canceled between attachments", 1, false, e.CodeTimeout},
		{"canceled during final attachment", 2, false, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := evidenceFixture()
			// Attachment transport exercises its private recheck callback. Real B
			// receipt issuance and copied-handle revocation are lifecycle tests.
			revoked := new(atomic.Bool)
			recheck := func() error {
				if revoked.Load() {
					return e.New(e.CodeGateUnmet, "B receipt revoked")
				}
				return nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var signed []string
			p := &Publisher{newAttester: func(string) (qualificationAttester, error) {
				return attesterFunc(func(_ context.Context, subject sign.Subject, _ []byte) error {
					signed = append(signed, subject.Ref)
					if len(signed) == tc.interruptAt {
						if tc.revoke {
							revoked.Store(true)
						} else {
							cancel()
						}
					}
					// The attachment completed before the interruption was observed.
					// Its diagnostic must survive even though the gate cannot succeed.
					return nil
				}), nil
			}}
			result, err := p.publishEvidence(ctx, fixture, &Publication{}, recheck)
			requireCode(t, err, tc.code)
			if !tc.revoke && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation cause lost: %v", err)
			}
			if result.Successful {
				t.Fatal("interrupted qualification claimed success")
			}
			want := []string{fixture.ArtifactChecks.Outputs[0].Reference}
			if tc.interruptAt == 2 {
				want = append(want, fixture.ArtifactChecks.Outputs[1].Reference)
			}
			if !slices.Equal(signed, want) {
				t.Fatalf("signed=%v, want exactly %v", signed, want)
			}
			if !slices.Equal(result.PublishedSubjects, want) {
				t.Fatalf("published=%v, want exactly %v", result.PublishedSubjects, want)
			}
		})
	}
}

func TestFailedVerdictsAndUnauthenticatedReceiptsPerformNoSigning(t *testing.T) {
	calls := 0
	p := &Publisher{newAttester: func(string) (qualificationAttester, error) {
		calls++
		t.Fatal("failed gate reached signer construction")
		return nil, nil
	}}
	fixture := evidenceFixture()
	fixture.ArtifactChecks.Outputs[1].Checks[0].Status = "fail"
	_, err := p.publishEvidence(t.Context(), fixture, &Publication{}, func() error { return nil })
	requireCode(t, err, e.CodeGateUnmet)
	_, err = p.Publish(t.Context(), &lifecycle.ArtifactCheckReceipt{}, &qualificationorigin.Invocation{}, nil, Policy{})
	requireCode(t, err, e.CodeGateUnmet)
	_, err = p.Publish(t.Context(), nil, nil, nil, Policy{RequiredSuites: []string{"nvke"}})
	requireCode(t, err, e.CodeEngineCapability)
	if calls != 0 {
		t.Fatal("unauthenticated gate caused signing")
	}
}

func TestQualificationEvidenceBindsFullWorkerPolicyAndMembers(t *testing.T) {
	good := evidenceFixture()
	raw, err := good.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*Evidence)
		code e.Code
	}{
		{"worker policy mismatch", func(v *Evidence) { v.ToolSmoke.Workers[0].PolicyDigest = "sha256:" + strings.Repeat("b", 64) }, e.CodeGateUnmet},
		{"policy digest mismatch", func(v *Evidence) { v.ToolSmoke.PolicyDigest = "sha256:" + strings.Repeat("b", 64) }, e.CodeVerifyFailed},
		{"changed full policy", func(v *Evidence) { v.ToolSmoke.Policy.OutsideMVPSuites = []string{"different"} }, e.CodeVerifyFailed},
		{"wrong profile", func(v *Evidence) { v.ToolSmoke.Workers[0].Members[0].Profile = "kubectl-client-version-json" }, e.CodeGateUnmet},
		{"missing member", func(v *Evidence) { v.ToolSmoke.Workers[0].Members = nil }, e.CodeVerifyFailed},
		{"changed worker version", func(v *Evidence) { v.ToolSmoke.Workers[0].Members[0].ObservedVersion = "v9.9.9" }, e.CodeVerifyFailed},
		{"changed exact A", func(v *Evidence) { v.ArtifactChecks.Inputs.DefinitionDigest = "sha256:" + strings.Repeat("b", 64) }, e.CodeGateUnmet},
		{"wrong worker invocation", func(v *Evidence) { v.ToolSmoke.Workers[0].Invocation = v.ArtifactChecks.Origin }, e.CodeVerifyFailed},
		{"omitted external scope", func(v *Evidence) { v.ExternalSuites = nil }, e.CodeGateUnmet},
		{"invented external scope", func(v *Evidence) { v.ExternalSuites[0].Name = "different" }, e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var changed Evidence
			if err := canonical.UnmarshalExact(raw, &changed); err != nil {
				t.Fatal(err)
			}
			tc.edit(&changed)
			requireCode(t, changed.Validate(), tc.code)
		})
	}
	// ReleaseSubject follows the real A receipt: an immutable registry reference.
	if !strings.Contains(good.ArtifactChecks.Inputs.ReleaseSubject, "@sha256:") {
		t.Fatal("fixture does not model actual A")
	}
	var old Evidence
	if err := canonical.UnmarshalExact(raw, &old); err != nil {
		t.Fatal(err)
	}
	old.SchemaVersion = "nvkr.dev/qualification-evidence/v1"
	requireCode(t, old.Validate(), e.CodeVerifyFailed)
	if SchemaVersion != "nvkr.dev/qualification-evidence/v2" || GateVersion != "nvkr.dev/qualification-gate/v2" || sign.QualificationPredicateType != "https://nvkr.dev/qualification-evidence/v2" {
		t.Fatal("wire version and predicate type differ")
	}
}

func TestQualificationPublisherBindsIndependentPolicyAndSigner(t *testing.T) {
	fixture := evidenceFixture()
	for _, tc := range []struct {
		name string
		edit func(*Policy, *lifecycle.QualificationPolicy, *string)
		code e.Code
	}{
		{"exact", func(*Policy, *lifecycle.QualificationPolicy, *string) {}, ""},
		{"missing digest", func(p *Policy, _ *lifecycle.QualificationPolicy, _ *string) { p.QualificationPolicyDigest = "" }, e.CodeTrustPolicyMissing},
		{"wrong digest", func(p *Policy, _ *lifecycle.QualificationPolicy, _ *string) {
			p.QualificationPolicyDigest = "sha256:" + strings.Repeat("b", 64)
		}, e.CodeTrustPolicyMissing},
		{"missing outside scope", func(p *Policy, _ *lifecycle.QualificationPolicy, _ *string) { p.OutsideMVPSuites = nil }, e.CodeTrustPolicyMissing},
		{"invented scope", func(p *Policy, _ *lifecycle.QualificationPolicy, _ *string) { p.OutsideMVPSuites = []string{"other"} }, e.CodeTrustPolicyMissing},
		{"required caller suite", func(p *Policy, _ *lifecycle.QualificationPolicy, _ *string) { p.RequiredSuites = []string{"nvke"} }, e.CodeEngineCapability},
		{"required signed suite", func(_ *Policy, p *lifecycle.QualificationPolicy, _ *string) { p.RequiredSuites = []string{"other"} }, e.CodeEngineCapability},
		{"different signer pin", func(_ *Policy, _ *lifecycle.QualificationPolicy, pin *string) {
			*pin = "sha256:" + strings.Repeat("b", 64)
		}, e.CodeUnpinnedInput},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected := Policy{QualificationPolicyDigest: fixture.ToolSmoke.PolicyDigest, OutsideMVPSuites: []string{"nvke"}}
			policy := fixture.ToolSmoke.Policy
			pin := policy.Cosign.Digest
			tc.edit(&selected, &policy, &pin)
			err := matchPublicationPolicy(selected, policy, pin)
			if tc.code != "" {
				requireCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQualificationRechecksBeforeSignerConstruction(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			calls := 0
			p := &Publisher{newAttester: func(string) (qualificationAttester, error) { calls++; return nil, nil }}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			code := e.CodeGateUnmet
			recheck := func() error { return e.New(e.CodeGateUnmet, "receipt revoked before signer") }
			if canceled {
				code = e.CodeTimeout
				recheck = func() error { cancel(); return nil }
			}
			result, err := p.publishEvidence(ctx, evidenceFixture(), &Publication{}, recheck)
			requireCode(t, err, code)
			if calls != 0 || result.Successful || len(result.PublishedSubjects) != 0 {
				t.Fatal("pre-sign failure reached signer or claimed output")
			}
		})
	}
}

func TestQualificationCompletePredicateBoundPrecedesSigner(t *testing.T) {
	fixture := evidenceFixture()
	// Every nested claim remains valid; duplication of a large independently
	// selected scope makes the complete signed predicate exceed its transport.
	largeSuite := strings.Repeat("s", int(sign.MaxQualificationPredicateBytes/2))
	fixture.ToolSmoke.Policy.OutsideMVPSuites = []string{largeSuite}
	fixture.ExternalSuites[0].Name = largeSuite
	digest, err := fixture.ToolSmoke.Policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	fixture.ToolSmoke.PolicyDigest = digest
	fixture.ToolSmoke.Workers[0].PolicyDigest = digest
	if err := fixture.validate(); err != nil {
		t.Fatalf("fixture is structurally invalid: %v", err)
	}
	raw, err := canonical.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(raw)) <= sign.MaxQualificationPredicateBytes {
		t.Fatal("fixture does not exceed final transport bound")
	}
	for _, check := range []struct {
		name string
		run  func() error
		code e.Code
	}{
		{"validate", fixture.Validate, e.CodeEngineCapability},
		{"canonical", func() error { _, err := fixture.Canonical(); return err }, e.CodeEngineCapability},
		{"digest", func() error { _, err := fixture.Digest(); return err }, e.CodeEngineCapability},
		{"decode", func() error { _, err := Decode(raw); return err }, e.CodeVerifyFailed},
	} {
		t.Run(check.name, func(t *testing.T) { requireCode(t, check.run(), check.code) })
	}
	calls := 0
	publisher := &Publisher{newAttester: func(string) (qualificationAttester, error) { calls++; return nil, nil }}
	result, err := publisher.publishEvidence(t.Context(), fixture, &Publication{}, func() error { return nil })
	requireCode(t, err, e.CodeEngineCapability)
	if calls != 0 || result.Successful || result.Evidence != nil || len(result.PublishedSubjects) != 0 {
		t.Fatal("oversized complete evidence reached signer or claimed publication")
	}
}
