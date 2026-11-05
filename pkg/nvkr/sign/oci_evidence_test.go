// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// The injected cryptographic runner exercises ownership and exact-byte plumbing;
// these tests do not establish acceptance by an external signing service.
func TestVerifyAndRetainExactEvidence(t *testing.T) {
	for _, scenario := range []string{"complete", "missing provenance", "crypto failure", "policy omits provenance", "reference digest mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newBundleRegistryFixture(t)
			signature := []byte(testOCIBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer))
			provenance := []byte(qualificationBundle(t, ociTestDigest, ProvenancePredicateType, ociObservedIdentity, ociTestIssuer, []byte(ociTestPredicate)))
			descriptors := []v1.Descriptor{fixture.add(t, signature, CosignSignaturePredicateType, nil)}
			if scenario != "missing provenance" {
				descriptors = append(descriptors, fixture.add(t, provenance, ProvenancePredicateType, nil))
			}
			fixture.index(t, descriptors)
			signer := newRegistryDiagnosticSigner(t, func([]byte, string) error {
				if scenario == "crypto failure" {
					return fmt.Errorf("fixture verification failed")
				}
				return nil
			})
			policy := VerifyPolicy{RequireAttestation: scenario != "policy omits provenance", ExpectedPredicate: []byte(ociTestPredicate)}
			subject := fixture.subject
			if scenario == "reference digest mismatch" {
				subject.Digest = "sha256:" + strings.Repeat("a", 64)
			}
			record, evidence, err := signer.VerifyAndRetain(t.Context(), subject, policy)
			if scenario != "complete" {
				want := map[string]e.Code{"missing provenance": e.CodeVerifyFailed, "crypto failure": e.CodeSignatureInvalid, "policy omits provenance": e.CodeTrustPolicyMissing, "reference digest mismatch": e.CodeVerifyFailed}[scenario]
				requireSignCode(t, err, want)
				if evidence != nil || record.SignatureVerified || record.AttestationVerified {
					t.Fatalf("failure returned evidence: %+v %v", record, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			gotSubject, gotSignature, gotProvenance, err := evidence.Bundles()
			if err != nil || gotSubject != subject || !bytes.Equal(gotSignature, signature) || !bytes.Equal(gotProvenance, provenance) {
				t.Fatalf("retained bytes differ from authenticated bundles: %v", err)
			}
			predicate, err := evidence.ProvenancePredicate()
			if err != nil || !bytes.Equal(predicate, []byte(ociTestPredicate)) {
				t.Fatalf("retained authenticated predicate changed: %s %v", predicate, err)
			}
			predicate[0] ^= 1
			again, err := evidence.ProvenancePredicate()
			if err != nil || !bytes.Equal(again, []byte(ociTestPredicate)) {
				t.Fatal("predicate accessor exposed mutable evidence")
			}
			gotSignature[0] ^= 1
			gotProvenance[0] ^= 1
			_, freshSignature, freshProvenance, err := evidence.Bundles()
			if err != nil || !bytes.Equal(freshSignature, signature) || !bytes.Equal(freshProvenance, provenance) {
				t.Fatal("exported slices mutate retained evidence")
			}
			if got := fixture.count("/v2/fixture/output/referrers/" + ociTestDigest); got != 1 {
				t.Fatalf("discovery reads = %d, want 1", got)
			}
		})
	}
}

func TestUnissuedOCIEvidence(t *testing.T) {
	for _, evidence := range []*VerifiedOCIEvidence{nil, {}} {
		_, _, _, err := evidence.Bundles()
		requireSignCode(t, err, e.CodeGateUnmet)
		_, err = evidence.ProvenancePredicate()
		requireSignCode(t, err, e.CodeGateUnmet)
	}
}

func TestVerifySuppliedOCIBundles(t *testing.T) {
	for _, scenario := range []string{"complete", "other subject", "corrupt signature", "missing provenance", "wrong identity", "wrong predicate", "noncanonical subject", "canceled", "crypto failure"} {
		t.Run(scenario, func(t *testing.T) {
			signature := []byte(testOCIBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer))
			provenance := []byte(qualificationBundle(t, ociTestDigest, ProvenancePredicateType, ociObservedIdentity, ociTestIssuer, []byte(ociTestPredicate)))
			signer := newRegistryDiagnosticSigner(t, func([]byte, string) error {
				if scenario == "crypto failure" {
					return fmt.Errorf("fixture crypto rejection")
				}
				return nil
			})
			// Any registry fallback is a test failure, including for invalid inputs.
			signer.bundleSource = func(context.Context, Subject, Credential) (*ociBundleSource, error) {
				t.Fatal("supplied evidence consulted registry discovery")
				return nil, nil
			}
			policy := VerifyPolicy{RequireAttestation: true, ExpectedPredicate: []byte(ociTestPredicate)}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch scenario {
			case "corrupt signature":
				signature[0] = '!'
			case "missing provenance":
				provenance = nil
			case "wrong identity":
				policy.IdentityRegexp = "^different-identity$"
			case "wrong predicate":
				policy.ExpectedPredicate = []byte(`{"different":true}`)
			case "canceled":
				cancel()
			}
			subject := Subject{Ref: "registry.example/fixture@" + ociTestDigest, Digest: ociTestDigest}
			if scenario == "noncanonical subject" {
				subject.Ref = "registry.example/UPPERCASE@" + ociTestDigest
			}
			if scenario == "other subject" {
				subject.Digest = "sha256:" + strings.Repeat("a", 64)
				subject.Ref = "registry.example/fixture@" + subject.Digest
				if err := subject.Validate(); err != nil {
					t.Fatalf("other-subject fixture must reach bundle claims: %v", err)
				}
			}
			record, evidence, err := signer.VerifyBundles(ctx, subject, policy, signature, provenance)
			if scenario != "complete" {
				want := map[string]e.Code{"other subject": e.CodeVerifyFailed, "corrupt signature": e.CodeSignatureInvalid, "missing provenance": e.CodeSignatureInvalid, "wrong identity": e.CodeUntrustedSigner, "wrong predicate": e.CodeVerifyFailed, "noncanonical subject": e.CodeUnverifiedRef, "canceled": e.CodeTimeout, "crypto failure": e.CodeSignatureInvalid}[scenario]
				requireSignCode(t, err, want)
				if evidence != nil || record.SignatureVerified || record.AttestationVerified {
					t.Fatalf("invalid evidence accepted: %+v %v", record, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, sig, prov, err := evidence.Bundles()
			if err != nil || got != subject || !bytes.Equal(sig, signature) || !bytes.Equal(prov, provenance) {
				t.Fatalf("transport changed source proof: %v", err)
			}
		})
	}
}
