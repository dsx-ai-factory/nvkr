// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// Crypto is substituted here; real metadata and exact subject bindings remain
// enforced. These tests do not establish Sigstore or hosted acceptance.
type authenticityVerifier struct {
	calls       int
	bundles     int
	mutate      func(int, *sign.VerificationRecord)
	verifyError error
	evidence    func(context.Context, string, []byte) error
}

func (*authenticityVerifier) SignAndAttest(context.Context, sign.Subject, []byte) (sign.SigningRecord, error) {
	return sign.SigningRecord{}, e.New(e.CodeUsage, "test verifier cannot sign")
}

func (v *authenticityVerifier) Verify(ctx context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, error) {
	v.calls++
	if _, bounded := ctx.Deadline(); !bounded || !policy.RequireAttestation {
		return sign.VerificationRecord{}, e.New(e.CodeInternal, "authentication omitted deadline or provenance")
	}
	record := sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion,
		Ref: subject.Ref, Digest: subject.Digest, Identity: policy.IdentityRegexp, Issuer: policy.Issuer,
		PredicateType: sign.ProvenancePredicateType, SignatureVerified: true, AttestationVerified: true,
		ObservedIdentity: "builder", ObservedIssuer: "issuer", Ran: [][]string{{"fixture", "verify"}}}
	if v.mutate != nil {
		v.mutate(v.calls, &record)
	}
	return record, v.verifyError
}

func (*authenticityVerifier) AttestSBOM(context.Context, string, []byte) ([]byte, error) {
	return nil, e.New(e.CodeUsage, "test verifier cannot attest")
}

func (v *authenticityVerifier) VerifySBOM(ctx context.Context, subject string, bundle []byte) error {
	v.bundles++
	if v.evidence != nil {
		return v.evidence(ctx, subject, bundle)
	}
	_, err := sbom.AttestedDocument(bundle, subject)
	return err
}

func authenticityPolicy() VerificationPolicy {
	return VerificationPolicy{Lane: LanePipeline, Scope: ScopeAuthenticity, IdentityRegexp: "^builder$", Issuer: "issuer"}
}

func authenticityMetadataFixture(t *testing.T) (string, string) {
	t.Helper()
	source, _ := testPublicationLayout(t)
	metadata := filepath.Join(t.TempDir(), "metadata")
	result, err := delivery.ExportMetadata(t.Context(), source, metadata)
	if err != nil {
		t.Fatal(err)
	}
	return metadata, result.RootDigest
}

func TestAuthenticityAdmissionPrecedesAllWork(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name   string
		change func(*Consumer, *VerificationPolicy, *string, *context.Context)
		code   e.Code
	}{
		{"nil context", func(_ *Consumer, _ *VerificationPolicy, _ *string, ctx *context.Context) { *ctx = nil }, e.CodeUsage},
		{"canceled context", func(_ *Consumer, _ *VerificationPolicy, _ *string, ctx *context.Context) { *ctx = canceled }, e.CodeTimeout},
		{"missing scope", func(_ *Consumer, p *VerificationPolicy, _ *string, _ *context.Context) { p.Scope = "" }, e.CodeTrustPolicyMissing},
		{"unknown scope", func(_ *Consumer, p *VerificationPolicy, _ *string, _ *context.Context) { p.Scope = "quick" }, e.CodeTrustPolicyMissing},
		{"full publication", func(_ *Consumer, p *VerificationPolicy, _ *string, _ *context.Context) {
			p.Scope = ScopeArtifactPublication
		}, e.CodeEngineCapability},
		{"full release", func(_ *Consumer, p *VerificationPolicy, _ *string, _ *context.Context) { p.Scope = ScopeRelease }, e.CodeEngineCapability},
		{"qualified authenticity", func(_ *Consumer, p *VerificationPolicy, _ *string, _ *context.Context) { p.Lane = LaneQualified }, e.CodeTrustPolicyMissing},
		{"qualified release", func(_ *Consumer, p *VerificationPolicy, _ *string, _ *context.Context) {
			p.Lane, p.Scope = LaneQualified, ScopeRelease
		}, e.CodeEngineCapability},
		{"missing identity", func(_ *Consumer, p *VerificationPolicy, _ *string, _ *context.Context) { p.IdentityRegexp = "" }, e.CodeTrustPolicyMissing},
		{"malformed identity", func(_ *Consumer, p *VerificationPolicy, _ *string, _ *context.Context) { p.IdentityRegexp = "[" }, e.CodeTrustPolicyMissing},
		{"missing issuer", func(_ *Consumer, p *VerificationPolicy, _ *string, _ *context.Context) { p.Issuer = "" }, e.CodeTrustPolicyMissing},
		{"unknown disk minimum", func(_ *Consumer, p *VerificationPolicy, _ *string, _ *context.Context) {
			p.MinimumDiskEvidence = "skip"
		}, e.CodeTrustPolicyMissing},
		{"tag reference", func(_ *Consumer, _ *VerificationPolicy, ref *string, _ *context.Context) {
			*ref = "registry.example.test/releases:latest"
		}, e.CodeUnverifiedRef},
		{"missing verifier", func(c *Consumer, _ *VerificationPolicy, _ *string, _ *context.Context) { c.verifier = nil }, e.CodeEngineCapability},
		{"missing SBOM adapter", func(c *Consumer, _ *VerificationPolicy, _ *string, _ *context.Context) {
			c.verifier = &releaseVerifier{}
		}, e.CodeEngineCapability},
		{"partial credential", func(c *Consumer, _ *VerificationPolicy, _ *string, _ *context.Context) {
			c.inspection.Registry.Username = "user"
		}, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			verifier := &authenticityVerifier{}
			workspace := filepath.Join(t.TempDir(), "must-not-exist")
			consumer := &Consumer{verifier: verifier, inspection: InspectionOptions{WorkspaceRoot: workspace}}
			policy, ref, ctx := authenticityPolicy(), "registry.example.test/releases@sha256:"+strings.Repeat("a", 64), t.Context()
			tc.change(consumer, &policy, &ref, &ctx)
			result, err := consumer.Authenticate(ctx, ref, policy)
			assertCode(t, err, tc.code)
			if result != nil || verifier.calls != 0 || verifier.bundles != 0 {
				t.Fatalf("failed admission returned a result or performed verification: %+v, %+v", result, verifier)
			}
			if _, err := os.Stat(workspace); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("failed admission touched workspace: %v", err)
			}
		})
	}
	var consumer *Consumer
	_, err := consumer.Authenticate(t.Context(), "registry.example.test/releases@sha256:"+strings.Repeat("a", 64), authenticityPolicy())
	assertCode(t, err, e.CodeEngineCapability)
}

func TestAuthenticityRejectsInvalidRootAndOutputVerification(t *testing.T) {
	layout, digest := authenticityMetadataFixture(t)
	for _, target := range []int{1, 2} {
		for _, tc := range []struct {
			name   string
			mutate func(*sign.VerificationRecord)
			code   e.Code
		}{
			{"missing signature", func(r *sign.VerificationRecord) { r.SignatureVerified = false }, e.CodeSignatureInvalid},
			{"missing provenance", func(r *sign.VerificationRecord) { r.AttestationVerified = false }, e.CodeSignatureInvalid},
			{"wrong predicate", func(r *sign.VerificationRecord) { r.PredicateType = sbom.CycloneDXPredicateType }, e.CodeSignatureInvalid},
			{"wrong subject", func(r *sign.VerificationRecord) {
				r.Digest = "sha256:" + strings.Repeat("a", 64)
				r.Ref = "registry.example.test/other@" + r.Digest
			}, e.CodeSignatureInvalid},
			{"wrong policy", func(r *sign.VerificationRecord) { r.Identity = ".*" }, e.CodeSignatureInvalid},
			{"wrong identity", func(r *sign.VerificationRecord) { r.ObservedIdentity = "attacker" }, e.CodeUntrustedSigner},
			{"wrong issuer", func(r *sign.VerificationRecord) { r.ObservedIssuer = "attacker" }, e.CodeUntrustedSigner},
			{"dry run", func(r *sign.VerificationRecord) {
				r.DryRun = true
				r.SignatureVerified = false
				r.AttestationVerified = false
				r.Ran = nil
				r.WouldRun = [][]string{{"fixture"}}
			}, e.CodeSignatureInvalid},
		} {
			label := "root/"
			if target == 2 {
				label = "output/"
			}
			t.Run(label+tc.name, func(t *testing.T) {
				v := &authenticityVerifier{mutate: func(call int, r *sign.VerificationRecord) {
					if call == target {
						tc.mutate(r)
					}
				}}
				consumer := &Consumer{verifier: v, inspection: InspectionOptions{LayoutPath: layout}}
				result, err := consumer.Authenticate(t.Context(), "registry.example.test/releases@"+digest, authenticityPolicy())
				assertCode(t, err, tc.code)
				if result != nil || v.calls != target {
					t.Fatalf("result=%v verification calls=%d, want %d", result, v.calls, target)
				}
			})
		}
	}
}

func TestAuthenticityResultIsNarrowDefensiveAndRevocable(t *testing.T) {
	layout, digest := authenticityMetadataFixture(t)
	consumer := &Consumer{verifier: &authenticityVerifier{}, inspection: InspectionOptions{LayoutPath: layout}}
	result, err := consumer.Authenticate(t.Context(), "registry.example.test/releases@"+digest, authenticityPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Close() }()
	description, err := result.Description()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(description)
	if err != nil {
		t.Fatal(err)
	}
	if description.SchemaVersion != AuthenticitySchemaVersion || description.Scope != ScopeAuthenticity || description.PayloadIntegrityVerified || !strings.Contains(string(raw), `"payloadIntegrityVerified":false`) {
		t.Fatalf("misleading authenticity result: %s", raw)
	}
	description.Artifacts[0].Components[0] = "mutated"
	description.Artifacts[0].Reference = "mutated"
	description.PayloadIntegrityVerified = true
	again, err := result.Description()
	if err != nil || again.Artifacts[0].Components[0] == "mutated" || again.Artifacts[0].Reference == "mutated" || again.PayloadIntegrityVerified {
		t.Fatalf("returned description mutated issued result: %+v, %v", again, err)
	}
	copyResult := *result
	if err := copyResult.Close(); err != nil {
		t.Fatal(err)
	}
	for _, closed := range []*AuthenticatedRelease{result, &copyResult, nil, {}} {
		_, err := closed.Description()
		assertCode(t, err, e.CodeGateUnmet)
		if err := closed.Close(); err != nil {
			t.Fatal(err)
		}
	}
	var decoded AuthenticatedRelease
	//nolint:staticcheck // Deliberately prove JSON cannot restore the opaque capability.
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	_, err = decoded.Description()
	assertCode(t, err, e.CodeGateUnmet)
}

func TestAuthenticityCannotSatisfyFullVerification(t *testing.T) {
	policy := authenticityPolicy()
	lightDigest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	full := policy
	full.Scope = ScopeArtifactPublication
	fullDigest, err := full.Digest()
	if err != nil || lightDigest == fullDigest {
		t.Fatalf("scopes share policy identity: %s, %s, %v", lightDigest, fullDigest, err)
	}
	verifier := &authenticityVerifier{}
	consumer := &Consumer{verifier: verifier, materialize: func(context.Context, string) (*delivery.Publication, error) {
		t.Fatal("authenticity reached full payload materialization")
		return nil, nil
	}}
	result, err := consumer.Verify(t.Context(), "registry.example.test/releases@sha256:"+strings.Repeat("a", 64), policy)
	assertCode(t, err, e.CodeEngineCapability)
	if result != nil || verifier.calls != 0 {
		t.Fatalf("full verification accepted authenticity: %v", result)
	}
}

func TestAuthenticityCancellationNeverIssuesResult(t *testing.T) {
	layout, digest := authenticityMetadataFixture(t)
	for _, stage := range []string{"root", "evidence", "output"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			verifier := &authenticityVerifier{mutate: func(call int, _ *sign.VerificationRecord) {
				if stage == "root" && call == 1 || stage == "output" && call == 2 {
					cancel()
				}
			}, evidence: func(context.Context, string, []byte) error {
				if stage == "evidence" {
					cancel()
				}
				return nil
			}}
			consumer := &Consumer{verifier: verifier, inspection: InspectionOptions{LayoutPath: layout}}
			result, err := consumer.Authenticate(ctx, "registry.example.test/releases@"+digest, authenticityPolicy())
			assertCode(t, err, e.CodeTimeout)
			if result != nil {
				t.Fatal("canceled authentication returned a result")
			}
		})
	}
}

func TestAuthenticityEvidenceFailureAndRootSubstitution(t *testing.T) {
	layout, digest := authenticityMetadataFixture(t)
	for _, tc := range []struct {
		name          string
		ref           string
		evidenceError error
		code          e.Code
	}{
		{"wrong root", "registry.example.test/releases@sha256:" + strings.Repeat("a", 64), nil, e.CodeVerifyFailed},
		{"invalid SBOM signature", "registry.example.test/releases@" + digest, e.New(e.CodeSignatureInvalid, "fixture invalid signature"), e.CodeSignatureInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := &authenticityVerifier{evidence: func(context.Context, string, []byte) error { return tc.evidenceError }}
			consumer := &Consumer{verifier: v, inspection: InspectionOptions{LayoutPath: layout}}
			result, err := consumer.Authenticate(t.Context(), tc.ref, authenticityPolicy())
			assertCode(t, err, tc.code)
			if result != nil || v.calls != 1 {
				t.Fatalf("evidence failure returned a result or verified outputs: %v, %d", result, v.calls)
			}
		})
	}
}
