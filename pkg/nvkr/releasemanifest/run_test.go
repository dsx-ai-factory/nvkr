// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releasemanifest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

type fakeTriggeredQualification struct {
	calls       []string
	publication *qualification.Publication
	publishErr  error
	evidenceErr error
	closeErr    error
	evidence    []byte
	checks      *lifecycle.ArtifactCheckReceipt
	after       func()
}

func (run *fakeTriggeredQualification) Publish(context.Context) (*qualification.Publication, error) {
	run.calls = append(run.calls, "publish")
	if run.after != nil {
		run.after()
	}
	return run.publication, run.publishErr
}

func (run *fakeTriggeredQualification) PublishedEvidence() (*lifecycle.ArtifactCheckReceipt, []byte, error) {
	run.calls = append(run.calls, "inputs")
	return run.checks, run.evidence, run.evidenceErr
}

func (run *fakeTriggeredQualification) Close() error {
	run.calls = append(run.calls, "close")
	return run.closeErr
}

func TestTriggeredManifestUsesSelectedPolicyIdentity(t *testing.T) {
	fixture := newIssueFixture(t)
	selected := fixture.evidence.ToolSmoke.Policy
	policy, err := triggeredManifestPolicy(selected)
	if err != nil {
		t.Fatal(err)
	}
	if policy != fixture.policy {
		t.Fatalf("policy projection differs from authenticated identity mapping: %+v", policy)
	}
	// The output evidence cannot select this trust identity or policy digest.
	fixture.evidence.Invocation.BuilderID = "https://github.com/other/workflow"
	fixture.evidence.ToolSmoke.PolicyDigest = fixtureDigest
	if after, err := triggeredManifestPolicy(selected); err != nil || after != policy {
		t.Fatalf("output evidence changed independently selected trust: %+v %v", after, err)
	}
	selected.Source.DefaultBranch = "release"
	changed, err := triggeredManifestPolicy(selected)
	if err != nil {
		t.Fatal(err)
	}
	if changed.QualificationIdentity != "https://github.com/example/nvkr/.github/workflows/qualify.yml@refs/heads/release" || changed.QualificationPolicyDigest == policy.QualificationPolicyDigest {
		t.Fatalf("selected branch did not change exact trust: %+v", changed)
	}
}

func TestTriggeredManifestPolicyRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*lifecycle.QualificationPolicy)
		code e.Code
	}{
		{"development", func(p *lifecycle.QualificationPolicy) {
			p.Source.Development = true
			p.Source.Workflow = ".github/workflows/nvkr-dev.yml"
			p.QualificationWorkflow = ".github/workflows/nvkr-dev-qualify.yml"
			p.Smoke.Workflow = ".github/workflows/nvkr-dev-smoke.yml"
		}, e.CodeEngineCapability},
		{"missing qualifier", func(p *lifecycle.QualificationPolicy) { p.QualificationWorkflow = "" }, e.CodeTrustPolicyMissing},
		{"builder is qualifier", func(p *lifecycle.QualificationPolicy) { p.QualificationWorkflow = p.Source.Workflow }, e.CodeTrustPolicyMissing},
		{"invalid source", func(p *lifecycle.QualificationPolicy) { p.Source.Repository = "invalid" }, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected := newIssueFixture(t).evidence.ToolSmoke.Policy
			tc.edit(&selected)
			_, err := triggeredManifestPolicy(selected)
			requireCode(t, err, tc.code)
		})
	}
}

func TestTriggeredManifestOrchestration(t *testing.T) {
	injected := e.New(e.CodeSignatureInvalid, "injected manifest verification failure")
	for _, tc := range []struct {
		name        string
		publishErr  error
		evidenceErr error
		issueErr    error
		closeErr    error
		want        []string
	}{
		{"complete flow", nil, nil, nil, nil, []string{"publish", "inputs", "issue", "close"}},
		{"partial M3", injected, nil, nil, nil, []string{"publish", "close"}},
		{"closed evidence", nil, injected, nil, nil, []string{"publish", "inputs", "close"}},
		{"manifest fails", nil, nil, injected, nil, []string{"publish", "inputs", "issue", "close"}},
		{"cleanup fails", nil, nil, nil, injected, []string{"publish", "inputs", "issue", "close"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newIssueFixture(t)
			run := &fakeTriggeredQualification{publication: &qualification.Publication{Evidence: &fixture.evidence, PublishedSubjects: []string{"first-retained-attachment"}},
				publishErr: tc.publishErr, evidenceErr: tc.evidenceErr, closeErr: tc.closeErr, evidence: fixture.raw, checks: &lifecycle.ArtifactCheckReceipt{}}
			result, err := publishTriggeredManifest(t.Context(), run, fixture.policy, func(ctx context.Context, checks *lifecycle.ArtifactCheckReceipt, raw []byte, policy Policy) (*Receipt, error) {
				run.calls = append(run.calls, "issue")
				if checks != run.checks || string(raw) != string(fixture.raw) || policy != fixture.policy {
					t.Fatal("bridge substituted retained checks, bytes or independently selected policy")
				}
				if tc.issueErr != nil {
					return nil, tc.issueErr
				}
				// Controlled issuer fixture validates actual complete evidence;
				// this is orchestration coverage, not a hosted receipt exercise.
				return fixture.issuer.issue(ctx, fixture.checks, raw, policy)
			})
			wantFailure := tc.publishErr != nil || tc.evidenceErr != nil || tc.issueErr != nil || tc.closeErr != nil
			if wantFailure {
				if !errors.Is(err, injected) || result.Successful {
					t.Fatalf("failed operation claimed success or lost cause: %+v %v", result, err)
				}
			} else {
				if err != nil || !result.Successful || result.ManifestDigest == "" || len(result.Manifest) == 0 || result.ManifestSubject.Ref == "" {
					t.Fatalf("successful manifest missing: %+v %v", result, err)
				}
				if _, err := Decode(result.Manifest); err != nil {
					t.Fatalf("manifest output is not exact canonical bytes: %v", err)
				}
			}
			if result.Qualification != run.publication || !slices.Equal(run.calls, tc.want) {
				t.Fatalf("diagnostics or operation order changed: %+v %v", result, run.calls)
			}
		})
	}
}

func TestTriggeredManifestCancellationAfterQualification(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	run := &fakeTriggeredQualification{publication: &qualification.Publication{}, after: cancel}
	result, err := publishTriggeredManifest(ctx, run, Policy{}, func(context.Context, *lifecycle.ArtifactCheckReceipt, []byte, Policy) (*Receipt, error) {
		t.Fatal("canceled flow invoked manifest issuance")
		return nil, nil
	})
	requireCode(t, err, e.CodeTimeout)
	if !errors.Is(err, context.Canceled) || result.Successful || result.Qualification != run.publication || !slices.Equal(run.calls, []string{"publish", "inputs", "close"}) {
		t.Fatalf("cancellation lost result or cleanup: %+v %v %v", result, err, run.calls)
	}
}

func TestTriggeredManifestCancellationBeforePolicyIO(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := PublishTriggeredRun(ctx, "/must-not-read", lifecycle.QualificationRuntimeOptions{})
	requireCode(t, err, e.CodeTimeout)
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled admission did work: %+v %v", result, err)
	}
}

func TestTriggeredManifestSharesHostedBudgetThroughIssuance(t *testing.T) {
	for _, tc := range []struct {
		name              string
		parentTimeout     time.Duration
		qualificationTime time.Duration
		wantTimeout       time.Duration
	}{
		{"complete hosted budget", 0, 58 * time.Minute, 59 * time.Minute},
		{"shorter caller", 5 * time.Minute, 4 * time.Minute, 5 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newIssueFixture(t)
			synctest.Test(t, func(t *testing.T) {
				ctx := t.Context()
				if tc.parentTimeout != 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, tc.parentTimeout)
					defer cancel()
				}
				start := time.Now()
				run := &fakeTriggeredQualification{publication: &qualification.Publication{}, after: func() { time.Sleep(tc.qualificationTime) }}
				result, err := publishTriggeredManifest(ctx, run, Policy{}, func(ctx context.Context, _ *lifecycle.ArtifactCheckReceipt, _ []byte, _ Policy) (*Receipt, error) {
					run.calls = append(run.calls, "issue")
					deadline, ok := ctx.Deadline()
					if !ok || !deadline.Equal(start.Add(tc.wantTimeout)) {
						t.Fatalf("manifest reset the shared deadline: %v, present = %t", deadline, ok)
					}
					<-ctx.Done()
					// A late success must not escape even if an adapter ignores cancellation.
					return &Receipt{raw: string(fixture.raw), digest: fixtureDigest, subject: sign.Subject{Ref: fixture.evidence.ArtifactChecks.Inputs.ReleaseSubject, Digest: fixtureDigest}}, nil
				})
				requireCode(t, err, e.CodeTimeout)
				if !errors.Is(err, context.DeadlineExceeded) || result.Successful || !time.Now().Equal(start.Add(tc.wantTimeout)) {
					t.Fatalf("complete operation escaped its shared budget: %+v %v at %v", result, err, time.Now())
				}
				if !slices.Equal(run.calls, []string{"publish", "inputs", "issue", "close"}) {
					t.Fatalf("deadline lost cleanup: %v", run.calls)
				}
			})
		})
	}
}
