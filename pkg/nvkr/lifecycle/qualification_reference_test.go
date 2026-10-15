// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

func TestAuthenticatedReleaseRetainsOwnedSourceInvocation(t *testing.T) {
	invocation := qualificationSourceFixture().value.InvocationID
	fixture := newExactInputFixture(t).withReleaseMetadata(t, func(m *delivery.ReleaseMetadata) { m.InvocationID = invocation })
	metadata := filepath.Join(t.TempDir(), "metadata")
	if _, err := delivery.ExportMetadata(t.Context(), fixture.layout, metadata); err != nil {
		t.Fatal(err)
	}
	consumer := &Consumer{verifier: &authenticityVerifier{}, inspection: InspectionOptions{LayoutPath: metadata}}
	authenticated, err := consumer.Authenticate(t.Context(), fixture.reference, authenticityPolicy())
	if err != nil {
		t.Fatal(err)
	}
	got, err := authenticated.sourceInvocation(t.Context())
	if err != nil || got != invocation {
		t.Fatalf("invocation=%q err=%v", got, err)
	}
	copyReceipt := *authenticated
	if err := authenticated.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = copyReceipt.sourceInvocation(t.Context())
	assertCode(t, err, e.CodeGateUnmet)
}

func TestQualificationReferenceOwnedSourceAdmission(t *testing.T) {
	reference := "registry.example.test/product@sha256:" + strings.Repeat("a", 64)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name                  string
		ctx                   context.Context
		reference, invocation string
		closed                bool
		edit                  func(*QualificationPolicy)
		code                  e.Code
	}{
		{name: "nil context", reference: reference, code: e.CodeUsage},
		{name: "canceled", ctx: canceled, reference: reference, code: e.CodeTimeout},
		{name: "mutable root", ctx: t.Context(), reference: "registry.example.test/product:latest", code: e.CodeUnverifiedRef},
		{name: "schemed root", ctx: t.Context(), reference: "https://" + reference, code: e.CodeUnverifiedRef},
		{name: "bad policy", ctx: t.Context(), reference: reference, edit: func(p *QualificationPolicy) { p.Source.Repository = "" }, code: e.CodeTrustPolicyMissing},
		{name: "missing invocation", ctx: t.Context(), reference: reference, code: e.CodeProvenanceMissing},
		{name: "ambiguous attempt", ctx: t.Context(), reference: reference, invocation: "https://github.com/example/nvkr/actions/runs/42", code: e.CodeProvenanceMissing},
		{name: "foreign source", ctx: t.Context(), reference: reference, invocation: "https://github.com/other/nvkr/actions/runs/42/attempts/2", code: e.CodeUntrustedSigner},
		{name: "closed authenticity", ctx: t.Context(), reference: reference, invocation: qualificationSourceFixture().value.InvocationID, closed: true, code: e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := qualificationPolicyFixture()
			if tc.edit != nil {
				tc.edit(&policy)
			}
			authCalls, runCalls := 0, 0
			// Private orchestration fixture only: this value never issues artifact authority.
			authenticated := &AuthenticatedRelease{state: &authenticatedReleaseState{valid: !tc.closed, invocationID: tc.invocation}}
			authenticate := func(context.Context, string, VerificationPolicy) (*AuthenticatedRelease, error) {
				authCalls++
				return authenticated, nil
			}
			run := func(context.Context, qualificationorigin.RunLocator, QualificationPolicy, QualificationRuntimeOptions) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
				runCalls++
				t.Fatal("invalid source reached provider")
				return nil, nil, nil
			}
			receipt, _, err := checkQualificationReferenceWithPolicy(tc.ctx, tc.reference, policy, QualificationRuntimeOptions{}, authenticate, run)
			assertCode(t, err, tc.code)
			if receipt != nil || runCalls != 0 {
				t.Fatal("invalid selection returned authority")
			}
			if tc.invocation == "" && tc.name != "missing invocation" && authCalls != 0 {
				t.Fatal("bad admission performed authentication")
			}
		})
	}
}

func TestQualificationReferenceBindsExactCandidateAndLifetime(t *testing.T) {
	request, consumer, verifier := newQualificationFixture(t)
	for _, mode := range []string{"matching", "different reference", "closed source", "late cancellation", "receipt and error"} {
		t.Run(mode, func(t *testing.T) {
			verifier.source = qualificationSourceFixture()
			checked, _, err := consumer.checkQualification(t.Context(), request, verifier.source)
			if err != nil {
				t.Fatal(err)
			}
			defer checked.Close()
			policy := qualificationPolicyFixture()
			reference := request.Reference
			if mode == "different reference" {
				reference = "registry.example.test/another@sha256:" + strings.Repeat("a", 64)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			authenticated := &AuthenticatedRelease{state: &authenticatedReleaseState{valid: true, invocationID: verifier.origin.InvocationID}}
			authenticate := func(_ context.Context, ref string, trust VerificationPolicy) (*AuthenticatedRelease, error) {
				if ref != reference || trust.Issuer != "https://token.actions.githubusercontent.com" || trust.IdentityRegexp != request.Environment.Signing.Identity {
					t.Fatalf("wrong source trust: %+v", trust)
				}
				return authenticated, nil
			}
			run := func(_ context.Context, locator qualificationorigin.RunLocator, frozen QualificationPolicy, _ QualificationRuntimeOptions) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
				if !reflect.DeepEqual(frozen, policy) || locator.Attempt != "2" || locator.RunID != "42" {
					t.Fatal("source or frozen policy changed")
				}
				if mode == "closed source" {
					verifier.source.closed = true
				}
				if mode == "late cancellation" {
					cancel()
				}
				if mode == "receipt and error" {
					return checked, nil, e.New(e.CodeUnavailable, "source check failed after issuing receipt")
				}
				return checked, nil, nil
			}
			got, report, err := checkQualificationReferenceWithPolicy(ctx, reference, policy, QualificationRuntimeOptions{}, authenticate, run)
			if mode == "matching" {
				if err != nil || got != checked || report.Inputs.ReleaseSubject != reference {
					t.Fatalf("receipt=%v report=%v err=%v", got, report, err)
				}
			} else {
				code := e.CodeVerifyFailed
				if mode == "closed source" {
					code = e.CodeProvenanceMissing
				}
				if mode == "late cancellation" {
					code = e.CodeTimeout
				}
				if mode == "receipt and error" {
					code = e.CodeUnavailable
				}
				assertCode(t, err, code)
				if got != nil || report != nil {
					t.Fatal("failed selection returned authority")
				}
				_, err = checked.Description()
				assertCode(t, err, e.CodeGateUnmet)
			}
			_, err = authenticated.sourceInvocation(t.Context())
			assertCode(t, err, e.CodeGateUnmet)
		})
	}
}

func TestQualificationReferenceHandoffCannotRedirectSelection(t *testing.T) {
	reference := "registry.example.test/product@sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		name string
		raw  []byte
		code e.Code
	}{
		{name: "matching", raw: qualificationHandoffJSON(t, QualificationHandoff{Candidate: QualificationCandidate{Reference: reference}})},
		{name: "different", raw: qualificationHandoffJSON(t, QualificationHandoff{Candidate: QualificationCandidate{Reference: "registry.example.test/other@sha256:" + strings.Repeat("b", 64)}}), code: e.CodeVerifyFailed},
		{name: "empty", code: e.CodeProvenanceMissing},
		{name: "noncanonical", raw: []byte("{}\n"), code: e.CodeProvenanceMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requireQualificationHandoffReference(tc.raw, reference)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, tc.code)
			}
		})
	}
	// Existing codec remains the authority for unknown fields, rather than a new parser.
	raw, _ := canonical.Marshal(map[string]string{"unexpected": "field"})
	assertCode(t, requireQualificationHandoffReference(raw, reference), e.CodeProvenanceMissing)
}

func TestQualificationReferenceTransportAdmission(t *testing.T) {
	reference := "registry.example.test/product@sha256:" + strings.Repeat("a", 64)
	complete := sign.Credential{Registry: "registry.example.test", Username: "user", Password: "private"}
	for _, tc := range []struct {
		name       string
		registry   delivery.RegistryOptions
		credential sign.Credential
		code       e.Code
	}{
		{name: "ambient auth refused", code: e.CodeEngineCapability},
		{name: "explicit anonymous", registry: delivery.RegistryOptions{Anonymous: true}},
		{name: "explicit scoped", registry: delivery.RegistryOptions{Username: "user", Password: "private"}, credential: complete},
		{name: "missing independent host", registry: delivery.RegistryOptions{Username: "user", Password: "private"}, code: e.CodeTrustPolicyMissing},
		{name: "foreign independent host", registry: delivery.RegistryOptions{Username: "user", Password: "private"}, credential: sign.Credential{Registry: "other.example.test", Username: "user", Password: "private"}, code: e.CodeUntrustedSigner},
		{name: "anonymous signing credential", registry: delivery.RegistryOptions{Anonymous: true}, credential: complete, code: e.CodeUsage},
		{name: "partial credential", credential: sign.Credential{Registry: "registry.example.test"}, code: e.CodeUsage},
		{name: "sign credential only", credential: complete, code: e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := admitQualificationReferenceTransport(reference, QualificationRuntimeOptions{Registry: tc.registry, Credential: tc.credential})
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, tc.code)
			}
		})
	}
	if runtime.GOOS == "linux" {
		_, _, err := CheckQualificationReference(t.Context(), reference, "/must-not-open-qualification-policy", QualificationRuntimeOptions{})
		assertCode(t, err, e.CodeEngineCapability)
		if !strings.Contains(err.Error(), "ambient credential helpers") {
			t.Fatalf("did not refuse auth before policy/tool/provider work: %v", err)
		}
	}
}

func TestQualificationSelectedRunAdmissionBeforeProviderIO(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		locator qualificationorigin.RunLocator
		policy  QualificationPolicy
		code    e.Code
	}{
		{"nil context", nil, qualificationorigin.RunLocator{}, qualificationPolicyFixture(), e.CodeUsage},
		{"canceled", canceled, qualificationorigin.RunLocator{}, qualificationPolicyFixture(), e.CodeVerifyFailed},
		{"bad policy", t.Context(), qualificationorigin.RunLocator{}, QualificationPolicy{}, e.CodeTrustPolicyMissing},
		{"bad locator", t.Context(), qualificationorigin.RunLocator{}, qualificationPolicyFixture(), e.CodeUsage},
		{"foreign repository", t.Context(), qualificationorigin.RunLocator{Repository: "other/repo", RunID: "42", Attempt: "2"}, qualificationPolicyFixture(), e.CodeUntrustedSigner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			receipt, report, err := checkQualificationRunWithPolicyAndReference(tc.ctx, tc.locator, tc.policy, QualificationRuntimeOptions{}, "registry.example.test/product@sha256:"+strings.Repeat("a", 64))
			assertCode(t, err, tc.code)
			if receipt != nil || report != nil {
				t.Fatal("provider admission returned authority")
			}
			if tc.ctx == canceled && !errors.Is(err, context.Canceled) {
				t.Fatal("provider cancellation cause lost")
			}
		})
	}
}

func TestQualificationRunReferenceOwnsCompletionAndFailureCleanup(t *testing.T) {
	request, consumer, verifier := newQualificationFixture(t)
	for _, mode := range []string{"download error", "reference mismatch", "check error", "receipt and error", "successful ownership"} {
		t.Run(mode, func(t *testing.T) {
			closed, checks := 0, 0
			closeSource := func() error { closed++; return nil }
			cause := e.New(e.CodeUnavailable, "bounded provider failure")
			reference := request.Reference
			raw := qualificationHandoffJSON(t, QualificationHandoff{Candidate: QualificationCandidate{Reference: reference}})
			if mode == "reference mismatch" {
				reference = "registry.example.test/other@sha256:" + strings.Repeat("b", 64)
			}
			var issued *ArtifactCheckReceipt
			download := func(context.Context) ([]byte, error) {
				if mode == "download error" {
					return nil, cause
				}
				return raw, nil
			}
			check := func(ctx context.Context, got []byte) (*ArtifactCheckReceipt, *ArtifactCheckDescription, error) {
				checks++
				if string(got) != string(raw) {
					t.Fatal("downloaded handoff bytes changed")
				}
				if mode == "check error" {
					return nil, nil, cause
				}
				var report *ArtifactCheckDescription
				var err error
				issued, report, err = consumer.checkQualification(ctx, request, verifier.source)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = issued.Close() })
				if mode == "receipt and error" {
					return issued, report, cause
				}
				return issued, report, nil
			}
			got, report, err := completeQualificationRunReference(t.Context(), reference, closeSource, download, check)
			if mode == "successful ownership" {
				if err != nil || got != issued || report == nil || closed != 0 {
					t.Fatalf("ownership transfer failed: %v closes%d", err, closed)
				}
				if err := got.Close(); err != nil {
					t.Fatal(err)
				}
				if closed != 1 {
					t.Fatal("receipt did not own source closure")
				}
				return
			}
			code := e.CodeUnavailable
			if mode == "reference mismatch" {
				code = e.CodeVerifyFailed
			}
			assertCode(t, err, code)
			if mode != "reference mismatch" && !errors.Is(err, cause) {
				t.Fatal("failure cause changed")
			}
			if closed != 1 || got != nil {
				t.Fatalf("failed run leaked ownership: closes%d receipt%v", closed, got)
			}
			if (mode == "download error" || mode == "reference mismatch") && checks != 0 {
				t.Fatal("failed transport or selection reached artifact work")
			}
			if issued != nil {
				_, err := issued.Description()
				assertCode(t, err, e.CodeGateUnmet)
			}
		})
	}
}
