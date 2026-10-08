// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sign"
)

type outputSigner struct {
	fakeOCISigner
	signed, verified                                  []sign.Subject
	failRef                                           string
	missingSignature, missingAttestation, substituted bool
}

func (s *outputSigner) SignAndAttest(ctx context.Context, subject sign.Subject, predicate []byte) (sign.SigningRecord, error) {
	s.signed = append(s.signed, subject)
	if subject.Ref == s.failRef {
		return sign.SigningRecord{}, e.New(e.CodeVerifyFailed, "signing refused")
	}
	return s.fakeOCISigner.SignAndAttest(ctx, subject, predicate)
}
func (s *outputSigner) Verify(_ context.Context, subject sign.Subject, policy sign.VerifyPolicy) (sign.VerificationRecord, error) {
	s.verified = append(s.verified, subject)
	if subject.Ref == s.failRef {
		return sign.VerificationRecord{}, e.New(e.CodeVerifyFailed, "verification refused")
	}
	if !policy.RequireAttestation {
		return sign.VerificationRecord{}, e.New(e.CodeVerifyFailed, "attestation required")
	}
	r := sign.VerificationRecord{SchemaVersion: sign.VerificationRecordSchemaVersion, Ref: subject.Ref, Digest: subject.Digest,
		Identity: policy.IdentityRegexp, Issuer: policy.Issuer, PredicateType: sign.ProvenancePredicateType,
		SignatureVerified: !s.missingSignature, AttestationVerified: !s.missingAttestation,
		ObservedIdentity: "local", ObservedIssuer: policy.Issuer, Ran: [][]string{{"cosign", "verify", subject.Ref}}}
	if s.substituted {
		r.Ref = "example.com/other@" + subject.Digest
	}
	return r, nil
}

func TestDeliverableSigningAndVerification(t *testing.T) {
	// The routing seam receives inspected metadata; byte/closure admission is
	// exercised by the executor and delivery integration tests separately.
	digest := "sha256:" + strings.Repeat("a", 64)
	inspection := delivery.Inspection{RootDigest: digest, Root: delivery.PublicationRoot{
		SchemaVersion: delivery.PublicationRootSchema, Qualification: delivery.QualificationDiagnostic}}
	var expected []sign.Subject
	for i, name := range []string{"cwe", "nke-helm", "nkectl"} {
		d := "sha256:" + strings.Repeat(string(rune('b'+i)), 64)
		inspection.Root.Objects = append(inspection.Root.Objects, delivery.PublishedObject{Name: name, Kind: "artifact",
			ArtifactType: delivery.MediaOCIManifest, DescriptorDigest: d, SemanticDigest: d, BlobDigest: d,
			Output: &delivery.OutputBinding{Repository: name}})
		expected = append(expected, sign.Subject{Ref: "example.com/product/" + name + "@" + d, Digest: d})
	}
	ref := "example.com/product/releases@" + digest
	for _, tc := range []struct {
		name                                                    string
		fail, missingSignature, missingAttestation, substituted bool
	}{
		{name: "all outputs"}, {name: "child failure", fail: true}, {name: "missing signature", missingSignature: true},
		{name: "missing attestation", missingAttestation: true}, {name: "substituted subject", substituted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signer := &outputSigner{fakeOCISigner: fakeOCISigner{identity: "^local$", issuer: "https://nkd.dev/local"},
				missingSignature: tc.missingSignature, missingAttestation: tc.missingAttestation, substituted: tc.substituted}
			if tc.fail {
				signer.failRef = expected[1].Ref
			}
			profile := dryRunSigningProfile()
			err := signPublicationOutputs(t.Context(), signer, ref, inspection, []byte("{}\n"), profile, io.Discard)
			if tc.fail {
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			} else if err != nil {
				t.Fatal(err)
			}
			if !tc.fail && !reflect.DeepEqual(signer.signed, expected) {
				t.Fatalf("signed subjects: %+v", signer.signed)
			}
			profile.Mode = SigningModeCosign
			err = verifyPublicationOutputs(t.Context(), signer, ref, inspection, profile, io.Discard)
			if tc.fail || tc.missingSignature || tc.missingAttestation || tc.substituted {
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(signer.verified, expected) {
					t.Fatalf("verified subjects: %+v", signer.verified)
				}
			}
		})
	}
}
