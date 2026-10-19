// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"slices"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestCosignVerifyBindsExpectedPredicateOnlyToProvenance(t *testing.T) {
	for _, tc := range []struct {
		name, predicate string
		want            e.Code
	}{
		{name: "exact provenance", predicate: ociTestPredicate},
		{name: "substituted provenance", predicate: `{"buildDefinition":{"run":"other"}}`, want: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signature := qualificationBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer, []byte(`{}`))
			provenance := qualificationBundle(t, ociTestDigest, ProvenancePredicateType, ociObservedIdentity, ociTestIssuer, []byte(tc.predicate))
			stub := &cosignStub{stdout: map[string]string{"version": cosignVersionJSON, "download": signature + "\n" + provenance}}
			var phases []string
			stub.onCall = func(call recordedCall) error {
				switch call.args[0] {
				case "verify-blob-attestation":
					phases = append(phases, "verify "+call.args[4])
				}
				return nil
			}
			record, err := newStubbedSigner(t, stub, Credential{}).Verify(t.Context(), ociSubject(), VerifyPolicy{
				RequireAttestation: true,
				ExpectedPredicate:  []byte(ociTestPredicate),
			})
			if tc.want != "" {
				requireSignCode(t, err, tc.want)
				if record.SignatureVerified || record.AttestationVerified || len(record.Ran) != 0 {
					t.Fatal("substituted provenance returned partial verification success")
				}
			} else if err != nil {
				t.Fatal(err)
			} else if !record.SignatureVerified || !record.AttestationVerified {
				t.Fatalf("missing verified evidence: %+v", record)
			}
			wantPhases := []string{
				"verify " + CosignSignaturePredicateType,
			}
			if tc.want == "" {
				wantPhases = append(wantPhases, "verify "+ProvenancePredicateType)
			}
			if !slices.Equal(phases, wantPhases) {
				t.Fatalf("verification phases = %v, want %v", phases, wantPhases)
			}
		})
	}
}

func TestOCIBundleBindsExpectedPredicate(t *testing.T) {
	raw := []byte(testOCIBundle(t, ociTestDigest, ProvenancePredicateType, ociObservedIdentity, ociTestIssuer))
	for _, tc := range []struct {
		name, expected string
		valid          bool
	}{
		{"matching", `{"buildDefinition":{}}`, true},
		{"whitespace", `{ "buildDefinition" : {} }`, true},
		{"different run", `{"buildDefinition":{"run":"other"}}`, false},
		{"missing field", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := ociBundleClaims(raw, ociTestDigest, ProvenancePredicateType, VerifyPolicy{IdentityRegexp: ociTestIdentity, Issuer: ociTestIssuer, RequireAttestation: true, ExpectedPredicate: []byte(tc.expected)})
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireSignCode(t, err, e.CodeVerifyFailed)
			}
		})
	}
}

func TestExpectedPredicateAdmissionBeforeIO(t *testing.T) {
	for _, tc := range []struct {
		name   string
		policy VerifyPolicy
	}{
		{"no attestation", VerifyPolicy{ExpectedPredicate: []byte(`{}`)}},
		{"ambiguous JSON", VerifyPolicy{RequireAttestation: true, ExpectedPredicate: []byte(`{"x":1,"x":2}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &cosignStub{}
			_, err := newStubbedSigner(t, stub, Credential{}).Verify(t.Context(), ociSubject(), tc.policy)
			requireSignCode(t, err, e.CodeTrustPolicyMissing)
			if len(stub.calls) > 0 {
				t.Fatal("invalid predicate policy performed IO")
			}
		})
	}
}
