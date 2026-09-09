package attest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/trust"
)

// PublishedVectorReceipt narrows an authenticated validation-vector trust document
// into the exact semantic and raw-byte identities needed by release publication.
// Its private state cannot be recreated by JSON deserialization.
type PublishedVectorReceipt struct {
	valid            bool
	manifest         Manifest
	identity         string
	blobDigest       string
	signer           string
	canonicalPayload string
}

// PublishedVectorBinding is the defensive release-facing projection of an
// authenticated vector. It binds the exact signed recipe/profile/artifact set and
// publisher evidence aggregates; possessing this data value alone is not authority.
type PublishedVectorBinding struct {
	IdentityDigest          string
	BlobDigest              string
	Signer                  string
	RecipeDigest            string
	ProfileDigest           string
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
	copyManifest := *manifest
	copyManifest.Components = append([]ComponentDigest(nil), manifest.Components...)
	return PublishedVectorReceipt{valid: true, manifest: copyManifest, identity: identity,
		blobDigest: snapshot, signer: signer, canonicalPayload: string(raw)}, nil
}

// PublicationBinding returns defensive publication metadata after revalidating the
// stored canonical payload and semantic identity.
func (receipt PublishedVectorReceipt) PublicationBinding() (identity, blobDigest, signer string,
	components []ComponentDigest, err error,
) {
	if !receipt.valid || !canonical.IsDigest(receipt.identity) ||
		!canonical.IsDigest(receipt.blobDigest) || strings.TrimSpace(receipt.signer) == "" ||
		receipt.canonicalPayload == "" {
		return "", "", "", nil, e.New(e.CodeSignatureInvalid,
			"published validation-vector receipt is absent or invalid")
	}
	manifest, parseErr := Parse([]byte(receipt.canonicalPayload))
	if parseErr != nil {
		return "", "", "", nil, parseErr
	}
	identityNow, identityErr := manifest.Identity()
	if identityErr != nil {
		return "", "", "", nil, identityErr
	}
	sum := sha256.Sum256([]byte(receipt.canonicalPayload))
	blobNow := "sha256:" + hex.EncodeToString(sum[:])
	if identityNow != receipt.identity || blobNow != receipt.blobDigest {
		return "", "", "", nil, e.New(e.CodeSignatureInvalid,
			"published validation-vector receipt changed after authentication")
	}
	return receipt.identity, receipt.blobDigest, receipt.signer,
		append([]ComponentDigest(nil), manifest.Components...), nil
}

// ReleaseBinding exposes every signed field needed to prove the transport-
// independent ReleaseSet -> ValidationVector -> publisher-evidence chain. The
// receipt and its canonical payload are revalidated on every call.
func (receipt PublishedVectorReceipt) ReleaseBinding() (PublishedVectorBinding, error) {
	identity, blob, signer, components, err := receipt.PublicationBinding()
	if err != nil {
		return PublishedVectorBinding{}, err
	}
	manifest, err := Parse([]byte(receipt.canonicalPayload))
	if err != nil {
		return PublishedVectorBinding{}, err
	}
	if manifest.Evidence == nil {
		return PublishedVectorBinding{}, e.New(e.CodeProvenanceMissing,
			"authenticated validation vector lost publisher evidence aggregates")
	}
	return PublishedVectorBinding{
		IdentityDigest: identity, BlobDigest: blob, Signer: signer,
		RecipeDigest: manifest.RecipeDigest, ProfileDigest: manifest.ProfileDigest,
		SBOMIndexDigest:         manifest.Evidence.SBOMDigest,
		ProvenanceIndexDigest:   manifest.Evidence.ProvenanceDigest,
		PublisherEvidenceDigest: manifest.EvidenceDigest,
		Components:              append([]ComponentDigest(nil), components...),
	}, nil
}

// ExactBytes returns a defensive copy of the authenticated canonical document.
func (receipt PublishedVectorReceipt) ExactBytes() ([]byte, error) {
	if _, _, _, _, err := receipt.PublicationBinding(); err != nil {
		return nil, err
	}
	return append([]byte(nil), receipt.canonicalPayload...), nil
}
