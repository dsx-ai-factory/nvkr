// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releasemanifest

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

type fakeChecks struct {
	description lifecycle.ArtifactCheckDescription
	members     []lifecycle.QualificationReleaseMember
	// closeAfter revokes the receipt after this many Description calls; 0 never.
	closeAfter int
	calls      int
	closed     bool
}

func (f *fakeChecks) Description() (lifecycle.ArtifactCheckDescription, error) {
	f.calls++
	if f.closed || (f.closeAfter > 0 && f.calls > f.closeAfter) {
		return lifecycle.ArtifactCheckDescription{}, e.New(e.CodeGateUnmet, "artifact check receipt is closed")
	}
	return f.description, nil
}

func (f *fakeChecks) ReleaseMembers() ([]lifecycle.QualificationReleaseMember, error) {
	return append([]lifecycle.QualificationReleaseMember(nil), f.members...), nil
}

type signerCall struct {
	operation string
	subject   sign.Subject
	raw       string
}

type fakeSigner struct {
	calls []signerCall
	fail  map[string]error // keyed by operation or operation+":"+subject ref
}

func (f *fakeSigner) record(operation string, subject sign.Subject, raw []byte) error {
	f.calls = append(f.calls, signerCall{operation: operation, subject: subject, raw: string(raw)})
	if err := f.fail[operation+":"+subject.Ref]; err != nil {
		return err
	}
	return f.fail[operation]
}

func (f *fakeSigner) VerifyQualification(_ context.Context, subject sign.Subject, raw []byte) error {
	return f.record("verify-qualification", subject, raw)
}

func (f *fakeSigner) AttestReleaseManifest(_ context.Context, subject sign.Subject, raw []byte) error {
	return f.record("attest-manifest", subject, raw)
}

func (f *fakeSigner) VerifyReleaseManifest(_ context.Context, subject sign.Subject, raw []byte) error {
	return f.record("verify-manifest", subject, raw)
}

type issueFixture struct {
	evidence qualification.Evidence
	raw      []byte
	checks   *fakeChecks
	signer   *fakeSigner
	issuer   *Issuer
	policy   Policy
	identity string
}

func newIssueFixture(t *testing.T) *issueFixture {
	t.Helper()
	evidence := evidenceFixture(t)
	raw, err := evidence.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	fixture := &issueFixture{evidence: evidence, raw: raw,
		checks: &fakeChecks{description: evidence.ArtifactChecks, members: membersFixture(evidence)},
		signer: &fakeSigner{fail: map[string]error{}},
		policy: Policy{QualificationIdentity: evidence.Invocation.BuilderID, QualificationPolicyDigest: evidence.ToolSmoke.PolicyDigest}}
	fixture.issuer = &Issuer{newSigner: func(identity string) (manifestSigner, error) {
		fixture.identity = identity
		return fixture.signer, nil
	}}
	return fixture
}

func (f *issueFixture) issue(ctx context.Context) (*Receipt, error) {
	return f.issuer.issue(ctx, f.checks, f.raw, f.policy)
}

func TestIssueAuthenticatesEveryMemberBeforeSigningAndVerifiesManifest(t *testing.T) {
	fixture := newIssueFixture(t)
	receipt, err := fixture.issue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if fixture.identity != fixture.policy.QualificationIdentity {
		t.Fatalf("signer identity=%q", fixture.identity)
	}
	outputs := fixture.evidence.ArtifactChecks.Outputs
	calls := fixture.signer.calls
	if len(calls) != len(outputs)+2 {
		t.Fatalf("calls=%+v", calls)
	}
	for i, output := range outputs {
		if calls[i].operation != "verify-qualification" || calls[i].subject != (sign.Subject{Ref: output.Reference, Digest: output.Subject}) || calls[i].raw != string(fixture.raw) {
			t.Fatalf("member %q was not authenticated with exact evidence before signing: %+v", output.Name, calls[i])
		}
	}
	manifest, err := receipt.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	root := sign.Subject{Ref: fixture.evidence.ArtifactChecks.Inputs.ReleaseSubject, Digest: fixtureDigest}
	for i, operation := range []string{"attest-manifest", "verify-manifest"} {
		call := calls[len(outputs)+i]
		if call.operation != operation || call.subject != root || call.raw != string(raw) {
			t.Fatalf("manifest was not attested and verified on the publication root: %+v", call)
		}
	}
	digest, err := receipt.Digest()
	if err != nil {
		t.Fatal(err)
	}
	want, err := manifest.Digest()
	if err != nil || digest != want {
		t.Fatalf("receipt digest=%s want=%s (%v)", digest, want, err)
	}
	subject, err := receipt.Subject()
	if err != nil || subject != root {
		t.Fatalf("receipt subject=%+v (%v)", subject, err)
	}
	exact, err := receipt.Canonical()
	if err != nil || string(exact) != string(raw) {
		t.Fatalf("receipt bytes differ (%v)", err)
	}
	exact[0] = 'X'
	again, err := receipt.Canonical()
	if err != nil || string(again) != string(raw) {
		t.Fatal("receipt bytes alias caller memory")
	}
}

func TestIssueRefusesUnauthenticatedQualificationBeforeSigner(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*testing.T, *issueFixture)
		code e.Code
	}{
		{"failed verdict", func(t *testing.T, f *issueFixture) {
			f.raw = []byte(strings.Replace(string(f.raw), `"overall":"pass"`, `"overall":"fail"`, 1))
		}, e.CodeGateUnmet},
		{"missing evidence", func(_ *testing.T, f *issueFixture) { f.raw = nil }, e.CodeVerifyFailed},
		{"non-canonical evidence", func(_ *testing.T, f *issueFixture) {
			f.raw = append([]byte(" "), f.raw...)
		}, e.CodeVerifyFailed},
		{"evidence for another check", func(_ *testing.T, f *issueFixture) {
			f.checks.description.Inputs.PlanDigest = "sha256:" + strings.Repeat("e", 64)
		}, e.CodeVerifyFailed},
		{"wrong qualification identity", func(_ *testing.T, f *issueFixture) {
			f.policy.QualificationIdentity = "https://github.com/example/nvkr/.github/workflows/other.yml@refs/heads/main"
		}, e.CodeUntrustedSigner},
		{"identity pattern", func(_ *testing.T, f *issueFixture) { f.policy.QualificationIdentity = " .*" }, e.CodeTrustPolicyMissing},
		{"missing identity", func(_ *testing.T, f *issueFixture) { f.policy.QualificationIdentity = "" }, e.CodeTrustPolicyMissing},
		{"other qualification policy", func(_ *testing.T, f *issueFixture) {
			f.policy.QualificationPolicyDigest = "sha256:" + strings.Repeat("f", 64)
		}, e.CodeTrustPolicyMissing},
		{"missing policy", func(_ *testing.T, f *issueFixture) { f.policy.QualificationPolicyDigest = "" }, e.CodeTrustPolicyMissing},
		{"incomplete provenance", func(_ *testing.T, f *issueFixture) { f.checks.members = f.checks.members[1:] }, e.CodeReleaseIncomplete},
		{"closed receipt", func(_ *testing.T, f *issueFixture) { f.checks.closed = true }, e.CodeGateUnmet},
		{"receipt revoked before signer", func(_ *testing.T, f *issueFixture) { f.checks.closeAfter = 1 }, e.CodeGateUnmet},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newIssueFixture(t)
			tc.edit(t, fixture)
			receipt, err := fixture.issue(t.Context())
			requireCode(t, err, tc.code)
			if receipt != nil || len(fixture.signer.calls) != 0 || fixture.identity != "" {
				t.Fatalf("refused issuance reached the signer: %+v", fixture.signer.calls)
			}
		})
	}
}

func TestIssueRefusesPartialOrFailedPublication(t *testing.T) {
	failure := e.New(e.CodeSignatureInvalid, "injected cosign failure")
	wrongSigner := e.New(e.CodeUntrustedSigner, "certificate identity differs")
	for _, tc := range []struct {
		name       string
		failKey    string
		failure    error
		closeAfter int
		code       e.Code
		attested   bool
	}{
		// An earlier output's surviving attachment cannot stand in for the set.
		{name: "later member lacks evidence", failKey: "verify-qualification:registry.example.test/disk@" + fixtureDigest, failure: failure, code: e.CodeSignatureInvalid},
		{name: "member signed by another identity", failKey: "verify-qualification:registry.example.test/chart@" + fixtureDigest, failure: wrongSigner, code: e.CodeUntrustedSigner},
		{name: "manifest attachment fails", failKey: "attest-manifest", failure: failure, code: e.CodeSignatureInvalid, attested: true},
		{name: "manifest verification fails", failKey: "verify-manifest", failure: failure, code: e.CodeSignatureInvalid, attested: true},
		{name: "receipt revoked during member checks", closeAfter: 3, code: e.CodeGateUnmet},
		{name: "receipt revoked before attestation", closeAfter: 5, code: e.CodeGateUnmet},
		{name: "receipt revoked after attestation", closeAfter: 6, code: e.CodeGateUnmet, attested: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newIssueFixture(t)
			if tc.failKey != "" {
				fixture.signer.fail[tc.failKey] = tc.failure
			}
			fixture.checks.closeAfter = tc.closeAfter
			receipt, err := fixture.issue(t.Context())
			requireCode(t, err, tc.code)
			if receipt != nil {
				t.Fatal("failed publication returned a receipt")
			}
			attested := false
			for _, call := range fixture.signer.calls {
				attested = attested || call.operation == "attest-manifest"
			}
			if attested != tc.attested {
				t.Fatalf("attested=%v calls=%+v", attested, fixture.signer.calls)
			}
		})
	}
}

func TestIssueCancellationNeverSucceeds(t *testing.T) {
	fixture := newIssueFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := fixture.issue(ctx)
	requireCode(t, err, e.CodeTimeout)
	if len(fixture.signer.calls) != 0 {
		t.Fatal("canceled issuance reached the signer")
	}

	fixture = newIssueFixture(t)
	ctx, cancel = context.WithCancel(t.Context())
	fixture.signer.fail = nil
	canceling := &cancelSigner{fakeSigner: fixture.signer, cancel: cancel}
	fixture.issuer.newSigner = func(string) (manifestSigner, error) { return canceling, nil }
	receipt, err := fixture.issue(ctx)
	requireCode(t, err, e.CodeTimeout)
	if receipt != nil {
		t.Fatal("interrupted issuance returned a receipt")
	}
}

type cancelSigner struct {
	*fakeSigner
	cancel context.CancelFunc
}

func (c *cancelSigner) AttestReleaseManifest(ctx context.Context, subject sign.Subject, raw []byte) error {
	c.cancel()
	return c.fakeSigner.AttestReleaseManifest(ctx, subject, raw)
}

func TestIssueAdmission(t *testing.T) {
	fixture := newIssueFixture(t)
	//nolint:staticcheck // A nil context is the refused input under test.
	_, err := fixture.issuer.issue(nil, fixture.checks, fixture.raw, fixture.policy)
	requireCode(t, err, e.CodeUsage)
	_, err = (*Issuer)(nil).issue(t.Context(), fixture.checks, fixture.raw, fixture.policy)
	requireCode(t, err, e.CodeEngineCapability)
	_, err = fixture.issuer.Issue(t.Context(), nil, fixture.raw, fixture.policy)
	requireCode(t, err, e.CodeGateUnmet)
	fixture.issuer.newSigner = func(string) (manifestSigner, error) { return nil, nil }
	_, err = fixture.issue(t.Context())
	requireCode(t, err, e.CodeEngineCapability)

	_, err = NewIssuer(Options{CosignBinary: "cosign", CosignDigest: "sha256:" + strings.Repeat("1", 64)})
	if runtime.GOOS != "linux" {
		requireCode(t, err, e.CodeEngineCapability)
	} else if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "linux" {
		_, err = NewIssuer(Options{CosignBinary: "cosign"})
		requireCode(t, err, e.CodeUnpinnedInput)
	}
}

func TestReceiptCannotBeConstructedOrDecoded(t *testing.T) {
	for _, receipt := range []*Receipt{nil, {}} {
		_, err := receipt.Manifest()
		requireCode(t, err, e.CodeGateUnmet)
		_, err = receipt.Digest()
		requireCode(t, err, e.CodeGateUnmet)
		_, err = receipt.Subject()
		requireCode(t, err, e.CodeGateUnmet)
		_, err = receipt.Canonical()
		requireCode(t, err, e.CodeGateUnmet)
	}
	var decoded Receipt
	if err := canonical.UnmarshalExact([]byte(`{}`), &decoded); err == nil {
		if _, err := decoded.Digest(); err == nil {
			t.Fatal("decoded receipt restored authority")
		}
	}
}
