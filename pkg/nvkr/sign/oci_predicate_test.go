// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

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
