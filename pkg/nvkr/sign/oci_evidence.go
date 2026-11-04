// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// VerifiedOCIEvidence retains the exact signature and provenance bundles that
// passed the existing OCI verifier together. Its bytes may be transported to an
// object store without re-signing or changing their original subject. Consumers
// must authenticate those bytes independently under their own policy.
type VerifiedOCIEvidence struct {
	issued     bool
	subject    Subject
	signature  []byte
	provenance []byte
}

// VerifyAndRetain uses the normal registry discovery, subject/predicate checks,
// pinned cosign execution and identity policy. Failed or partial verification
// returns no evidence capability, even if a signature passed before provenance
// failed. No second discovery or re-signing operation is introduced.
func (s *CosignOCISigner) VerifyAndRetain(ctx context.Context, subject Subject, policy VerifyPolicy) (VerificationRecord, *VerifiedOCIEvidence, error) {
	return s.verifyRetained(ctx, subject, policy, nil)
}

// VerifyBundles independently authenticates transported evidence under the
// caller's policy using the same claims and pinned cosign checks as registry
// verification. It neither consults a registry nor trusts the storage location.
func (s *CosignOCISigner) VerifyBundles(ctx context.Context, subject Subject, policy VerifyPolicy, signature, provenance []byte) (VerificationRecord, *VerifiedOCIEvidence, error) {
	if len(signature) == 0 || len(provenance) == 0 || int64(len(signature)) > maxCosignOutputBytes || int64(len(provenance)) > maxCosignOutputBytes {
		return VerificationRecord{}, nil, e.New(e.CodeSignatureInvalid, "supplied OCI evidence requires bounded signature and provenance bundles")
	}
	// Snapshot before verification so caller mutation cannot substitute proof.
	proofs := [][]byte{bytes.Clone(signature), bytes.Clone(provenance)}
	return s.verifyRetained(ctx, subject, policy, func() *ociBundleSource {
		source := &ociBundleSource{close: func() {}}
		byDigest := make(map[string][]byte, len(proofs))
		for _, proof := range proofs {
			hash := v1.Hash{Algorithm: "sha256", Hex: digestBytes(proof)[len("sha256:"):]}
			source.descriptors = append(source.descriptors, v1.Descriptor{Digest: hash})
			byDigest[hash.String()] = proof
		}
		source.load = func(ctx context.Context, descriptor v1.Descriptor) ([]byte, error) {
			if err := contextError(ctx); err != nil {
				return nil, err
			}
			return byDigest[descriptor.Digest.String()], nil
		}
		return source
	})
}

func (s *CosignOCISigner) verifyRetained(ctx context.Context, subject Subject, policy VerifyPolicy, supplied func() *ociBundleSource) (VerificationRecord, *VerifiedOCIEvidence, error) {
	if !policy.RequireAttestation {
		return VerificationRecord{}, nil, e.New(e.CodeTrustPolicyMissing, "portable OCI evidence requires signature and provenance verification")
	}
	evidence := &VerifiedOCIEvidence{subject: subject}
	record, err := s.verifyAndCapture(ctx, subject, policy, func(predicate string, raw []byte) {
		switch predicate {
		case CosignSignaturePredicateType:
			evidence.signature = bytes.Clone(raw)
		case ProvenancePredicateType:
			evidence.provenance = bytes.Clone(raw)
		}
	}, supplied)
	if err != nil {
		return VerificationRecord{}, nil, err
	}
	if !record.SignatureVerified || !record.AttestationVerified || record.DryRun || len(evidence.signature) == 0 || len(evidence.provenance) == 0 {
		return VerificationRecord{}, nil, e.New(e.CodeSignatureInvalid, "portable OCI evidence requires both authenticated bundles")
	}
	evidence.issued = true
	return record, evidence, nil
}

// Bundles exports defensive copies bound to the original exact subject. These
// are source proof bytes, not a delivery-completion receipt or a new signature
// over an extracted raw file.
func (evidence *VerifiedOCIEvidence) Bundles() (Subject, []byte, []byte, error) {
	if evidence == nil || !evidence.issued || len(evidence.signature) == 0 || len(evidence.provenance) == 0 {
		return Subject{}, nil, nil, e.New(e.CodeGateUnmet, "verified OCI evidence is unavailable")
	}
	return evidence.subject, bytes.Clone(evidence.signature), bytes.Clone(evidence.provenance), nil
}

// ProvenancePredicate returns the exact predicate carried by the already
// authenticated original bundle. It performs no discovery or signing and never
// accepts a caller-provided statement as an evidence capability.
func (evidence *VerifiedOCIEvidence) ProvenancePredicate() ([]byte, error) {
	if _, _, _, err := evidence.Bundles(); err != nil {
		return nil, err
	}
	var bundle struct {
		DSSE struct {
			Payload string `json:"payload"`
		} `json:"dsseEnvelope"`
	}
	if err := json.Unmarshal(evidence.provenance, &bundle); err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "reading retained provenance bundle")
	}
	payload, err := base64.StdEncoding.DecodeString(bundle.DSSE.Payload)
	if err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "reading retained provenance statement")
	}
	var statement inTotoStatement
	if err := json.Unmarshal(payload, &statement); err != nil || statement.PredicateType != ProvenancePredicateType || len(statement.Predicate) == 0 {
		return nil, e.New(e.CodeSignatureInvalid, "retained provenance has no authenticated production predicate")
	}
	return bytes.Clone(statement.Predicate), nil
}
