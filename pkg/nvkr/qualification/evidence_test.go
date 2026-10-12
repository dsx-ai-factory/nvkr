// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"context"
	"errors"
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
		Inputs: lifecycle.ExactInputDescription{ReleaseSubject: digest, PolicyDigest: digest, DefinitionDigest: digest, EnvironmentDigest: digest, PlanDigest: digest, ContentSubject: digest},
	}, ExternalSuites: []ExternalSuite{{Name: "nvke", Status: "not-evaluated", Reason: "outside MVP scope"}}}
	for _, name := range []string{"chart-a", "chart-b"} {
		output := lifecycle.ArtifactCheckOutput{Name: name, Kind: lifecycle.OutputHelm, Reference: "registry.example.test/" + name + "@" + digest, Subject: digest, BuildDigest: digest, Overall: "pass", BuildSubjects: []delivery.BuildSubject{{Component: name, Digest: digest}}}
		for _, check := range requiredArtifactChecks {
			output.Checks = append(output.Checks, lifecycle.ArtifactCheckResult{Name: check, Status: "pass"})
		}
		evidence.ArtifactChecks.Outputs = append(evidence.ArtifactChecks.Outputs, output)
		evidence.Smoke = append(evidence.Smoke, Verdict{Output: name, Subject: digest, Check: "delivered-tool-version", Status: "not-applicable", Reason: "non-tool artifact format"})
	}
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
		{"unknown schema", func(v *Evidence) { v.SchemaVersion = "nvkr.dev/qualification-evidence/v2" }, e.CodeVerifyFailed},
		{"unknown gate", func(v *Evidence) { v.GateVersion = "other" }, e.CodeVerifyFailed},
		{"builder signs qualification", func(v *Evidence) { v.Invocation = v.ArtifactChecks.Origin }, e.CodeVerifyFailed},
		{"commit absent", func(v *Evidence) { v.ArtifactChecks.Origin.Commit = "" }, e.CodeTrustPolicyMissing},
		{"attempt changed", func(v *Evidence) { v.ArtifactChecks.Origin.Attempt = "2" }, e.CodeVerifyFailed},
		{"input missing", func(v *Evidence) { v.ArtifactChecks.Inputs.PlanDigest = "" }, e.CodeVerifyFailed},
		{"missing output", func(v *Evidence) { v.ArtifactChecks.Outputs = nil }, e.CodeReleaseIncomplete},
		{"duplicate output", func(v *Evidence) { v.ArtifactChecks.Outputs[1] = v.ArtifactChecks.Outputs[0] }, e.CodeGateUnmet},
		{"failed check", func(v *Evidence) { v.ArtifactChecks.Outputs[0].Checks[0].Status = "fail" }, e.CodeGateUnmet},
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
		{"executable skipped", func(v *Evidence) { v.ArtifactChecks.Outputs[0].Kind = lifecycle.OutputExecutable }, e.CodeGateUnmet},
		{"unknown applicability", func(v *Evidence) { v.ArtifactChecks.Outputs[0].Kind = lifecycle.OutputOCI }, e.CodeEngineCapability},
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
	for _, tc := range []struct {
		name string
		kind string
		code e.Code
	}{
		{"chart non-tool", lifecycle.OutputHelm, ""}, {"disk non-tool", lifecycle.OutputDiskImage, ""},
		{"missing executable smoke", lifecycle.OutputExecutable, e.CodeGateUnmet},
		{"image applicability deferred", lifecycle.OutputOCI, e.CodeEngineCapability},
		{"APT applicability deferred", lifecycle.OutputAPT, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := evidenceFixture()
			for i := range fixture.ArtifactChecks.Outputs {
				fixture.ArtifactChecks.Outputs[i].Kind = tc.kind
			}
			verdicts, err := collectSmoke(fixture.ArtifactChecks, fixture.Invocation, nil)
			if len(verdicts) != len(fixture.ArtifactChecks.Outputs) {
				t.Fatal("short-circuited applicable verdict collection")
			}
			if tc.code != "" {
				requireCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
	fixture := evidenceFixture()
	_, err := collectSmoke(fixture.ArtifactChecks, fixture.Invocation, &SmokeReceipt{})
	requireCode(t, err, e.CodeGateUnmet)
	raw, _ := canonical.Marshal(fixture.ArtifactChecks)
	receipt := &SmokeReceipt{artifactChecks: string(raw), invocation: fixture.Invocation, verdicts: fixture.Smoke, closed: new(atomic.Bool)}
	receipt.verdicts[0].Status = "fail"
	_, err = collectSmoke(fixture.ArtifactChecks, fixture.Invocation, receipt)
	requireCode(t, err, e.CodeGateUnmet)
	_ = receipt.Close()
	_, err = collectSmoke(fixture.ArtifactChecks, fixture.Invocation, receipt)
	requireCode(t, err, e.CodeGateUnmet)
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
			for i := range fixture.ArtifactChecks.Outputs {
				fixture.ArtifactChecks.Outputs[i].Kind = lifecycle.OutputExecutable
				fixture.Smoke[i].Status = "pass"
				fixture.Smoke[i].Reason = ""
			}
			raw, err := canonical.Marshal(fixture.ArtifactChecks)
			if err != nil {
				t.Fatal(err)
			}
			// This private fixture exercises the receipt's actual shared lifetime;
			// it is not evidence from the still-unimplemented M3-B smoke issuer.
			receipt := &SmokeReceipt{artifactChecks: string(raw), invocation: fixture.Invocation, verdicts: fixture.Smoke, closed: new(atomic.Bool)}
			copied := *receipt
			recheck := func() error {
				_, err := collectSmoke(fixture.ArtifactChecks, fixture.Invocation, &copied)
				return err
			}
			if err := recheck(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var signed []string
			p := &Publisher{newAttester: func(string) (qualificationAttester, error) {
				return attesterFunc(func(_ context.Context, subject sign.Subject, _ []byte) error {
					signed = append(signed, subject.Ref)
					if len(signed) == tc.interruptAt {
						if tc.revoke {
							if err := receipt.Close(); err != nil {
								t.Fatal(err)
							}
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
