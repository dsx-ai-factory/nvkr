// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package attest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
)

// PublishedVectorReceipt narrows an authenticated validation-vector trust document
// into the exact semantic and raw-byte identities needed by release publication.
// Its private state cannot be recreated by JSON deserialization.
type PublishedVectorReceipt struct {
	valid            bool
	identity         string
	blobDigest       string
	signer           string
	canonicalPayload string
}

// publishedVectorBinding is the exact authenticated evidence projection consumed
// by the local publisher-evidence comparison.
type publishedVectorBinding struct {
	BuildDigest             string
	SBOMIndexDigest         string
	ProvenanceIndexDigest   string
	PublisherEvidenceDigest string
	Components              []ComponentDigest
}

// VerifyPublishedVectorReceipt requires the dedicated validation-vector role and
// re-parses the exact authenticated bytes with the publishable schema contract.
func VerifyPublishedVectorReceipt(ctx context.Context, document trust.DocumentReceipt) (PublishedVectorReceipt, error) {
	if ctx == nil {
		return PublishedVectorReceipt{}, e.New(e.CodeUsage,
			"published validation-vector authentication requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return PublishedVectorReceipt{}, e.Wrap(e.CodeTimeout, err,
			"authenticating published validation vector")
	}
	if !document.Authenticated() || document.Role() != trust.RoleValidationVector ||
		document.SchemaVersion() != SchemaVersion {
		return PublishedVectorReceipt{}, e.New(e.CodeSignatureInvalid,
			"published validation vector requires an authenticated %s document",
			trust.RoleValidationVector)
	}
	raw := document.ExactBytes()
	manifest, err := Parse(raw)
	if err != nil {
		return PublishedVectorReceipt{}, err
	}
	payload, err := manifest.Marshal()
	if err != nil {
		return PublishedVectorReceipt{}, err
	}
	if !bytes.Equal(raw, payload) {
		return PublishedVectorReceipt{}, e.New(e.CodeSignatureInvalid,
			"authenticated validation-vector bytes are not canonical")
	}
	identity, err := canonical.Digest(canonical.KindValidationVector, manifest)
	if err != nil {
		return PublishedVectorReceipt{}, err
	}
	signer := strings.TrimSpace(document.SignerIdentity())
	if signer == "" || signer != document.SignerIdentity() {
		return PublishedVectorReceipt{}, e.New(e.CodeSignatureInvalid,
			"authenticated validation vector has no canonical signer identity")
	}
	sum := sha256.Sum256(raw)
	snapshot := "sha256:" + hex.EncodeToString(sum[:])
	if document.SnapshotSHA256() != snapshot {
		return PublishedVectorReceipt{}, e.New(e.CodeSignatureInvalid,
			"authenticated validation-vector snapshot does not bind its exact bytes")
	}
	return PublishedVectorReceipt{valid: true, identity: identity,
		blobDigest: snapshot, signer: signer, canonicalPayload: string(raw)}, nil
}

// evidenceBinding revalidates the exact canonical payload and semantic identity
// before exposing the fields used by publisher-evidence comparison.
func (receipt PublishedVectorReceipt) evidenceBinding() (publishedVectorBinding, error) {
	if !receipt.valid || !canonical.IsDigest(receipt.identity) ||
		!canonical.IsDigest(receipt.blobDigest) || strings.TrimSpace(receipt.signer) == "" ||
		receipt.canonicalPayload == "" {
		return publishedVectorBinding{}, e.New(e.CodeSignatureInvalid,
			"published validation-vector receipt is absent or invalid")
	}
	manifest, err := Parse([]byte(receipt.canonicalPayload))
	if err != nil {
		return publishedVectorBinding{}, err
	}
	identityNow, err := manifest.Identity()
	if err != nil {
		return publishedVectorBinding{}, err
	}
	sum := sha256.Sum256([]byte(receipt.canonicalPayload))
	blobNow := "sha256:" + hex.EncodeToString(sum[:])
	if identityNow != receipt.identity || blobNow != receipt.blobDigest {
		return publishedVectorBinding{}, e.New(e.CodeSignatureInvalid,
			"published validation-vector receipt changed after authentication")
	}
	if manifest.Evidence == nil {
		return publishedVectorBinding{}, e.New(e.CodeProvenanceMissing,
			"authenticated validation vector lost publisher evidence aggregates")
	}
	return publishedVectorBinding{
		BuildDigest:             manifest.BuildDigest,
		SBOMIndexDigest:         manifest.Evidence.SBOMDigest,
		ProvenanceIndexDigest:   manifest.Evidence.ProvenanceDigest,
		PublisherEvidenceDigest: manifest.EvidenceDigest,
		Components:              append([]ComponentDigest(nil), manifest.Components...),
	}, nil
}
