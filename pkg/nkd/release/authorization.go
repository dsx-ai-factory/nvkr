package release

import (
	"bytes"
	"context"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/attest"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/vuln"
)

const AuthorizationSchemaVersion = "nkd.dev/release-authorization/v9"

// ReleaseSetReceipt is an unforgeable-in-data capability: its critical fields are
// private and it can be created only by VerifyAuthenticatedReleaseSet.
type ReleaseSetReceipt struct {
	valid               bool
	set                 ReleaseSet
	digest              string
	publicationIdentity string
	signer              string
	canonicalPayload    string
}

// VerifyAuthenticatedReleaseSet narrows one exact authenticated trust document into
// a release-set capability after strict schema, canonical-byte, semantic, and role
// validation. No parsed structure or signer string is accepted from the caller.
func VerifyAuthenticatedReleaseSet(ctx context.Context, document trust.DocumentReceipt) (ReleaseSetReceipt, error) {
	if err := contextErr(ctx); err != nil {
		return ReleaseSetReceipt{}, err
	}
	if !document.Authenticated() || document.Role() != trust.RoleReleaseSet {
		return ReleaseSetReceipt{}, e.New(e.CodeSignatureInvalid,
			"release set requires an authenticated %s document", trust.RoleReleaseSet)
	}
	if document.SchemaVersion() != ReleaseSetSchemaVersion {
		return ReleaseSetReceipt{}, e.New(e.CodeInvalidCatalog,
			"authenticated release-set schemaVersion %q is unsupported (want %q)",
			document.SchemaVersion(), ReleaseSetSchemaVersion)
	}
	switch decoded := document.Decoded().(type) {
	case ReleaseSet:
	case *ReleaseSet:
		if decoded == nil {
			return ReleaseSetReceipt{}, e.New(e.CodeSignatureInvalid,
				"authenticated release set decoded to nil")
		}
	default:
		return ReleaseSetReceipt{}, e.New(e.CodeSignatureInvalid,
			"authenticated release set has unexpected decoded type")
	}
	raw := document.ExactBytes()
	var set ReleaseSet
	if err := canonical.UnmarshalExact(raw, &set); err != nil {
		return ReleaseSetReceipt{}, err
	}
	if err := set.Validate(); err != nil {
		return ReleaseSetReceipt{}, err
	}
	payload, err := set.Marshal()
	if err != nil {
		return ReleaseSetReceipt{}, err
	}
	if !bytes.Equal(raw, payload) {
		return ReleaseSetReceipt{}, e.New(e.CodeInvalidCatalog,
			"authenticated release set does not use canonical member/artifact ordering")
	}
	signer := strings.TrimSpace(document.SignerIdentity())
	if signer == "" {
		return ReleaseSetReceipt{}, e.New(e.CodeSignatureInvalid,
			"authenticated release set has no signer identity")
	}
	digest, err := set.Digest()
	if err != nil {
		return ReleaseSetReceipt{}, err
	}
	normalized := set.sorted()
	publicationIdentity, err := normalized.PublicationIdentity()
	if err != nil {
		return ReleaseSetReceipt{}, err
	}
	return ReleaseSetReceipt{valid: true, set: *normalized, digest: digest,
		publicationIdentity: publicationIdentity, signer: signer, canonicalPayload: string(payload)}, nil
}

// PublicationIdentity returns ADR-06's domain-separated immutable publication
// target. It is deliberately distinct from the ReleaseSet content digest used by
// reproduction and conformance receipts.
func (receipt ReleaseSetReceipt) PublicationIdentity() (string, error) {
	if !receipt.valid || !isDigest(receipt.publicationIdentity) {
		return "", e.New(e.CodeUnverifiedRef, "release-set receipt has no publication identity")
	}
	recomputed, err := receipt.set.PublicationIdentity()
	if err != nil {
		return "", err
	}
	if recomputed != receipt.publicationIdentity {
		return "", e.New(e.CodeUnverifiedRef,
			"release-set publication identity changed after authentication")
	}
	return receipt.publicationIdentity, nil
}

// ConformanceReceipt proves the exact conformance result was signed by an identity
// distinct from the release signer.
type ConformanceReceipt struct {
	valid            bool
	result           ConformanceResult
	verifiedSigner   string
	releaseSetDigest string
	canonicalPayload string
	digest           string
}

func (receipt ConformanceReceipt) Digest() (string, error) {
	if !receipt.valid {
		return "", e.New(e.CodeSignatureInvalid, "conformance receipt is not authenticated")
	}
	return receipt.digest, nil
}

// AuthenticateConformance verifies one exhaustive release-level conformance result
// under the dedicated conformance role.
func AuthenticateConformance(ctx context.Context, result ConformanceResult, signature []byte,
	release ReleaseSetReceipt, authenticator ReceiptAuthenticator,
) (ConformanceReceipt, error) {
	if err := contextErr(ctx); err != nil {
		return ConformanceReceipt{}, err
	}
	if !release.valid {
		return ConformanceReceipt{}, e.New(e.CodeUnverifiedRef, "conformance verification requires a verified release-set receipt")
	}
	if authenticator == nil || len(signature) == 0 {
		return ConformanceReceipt{}, e.New(e.CodeSignatureInvalid,
			"conformance result requires signature authentication for role %s", RoleConformance.String())
	}
	result.VectorDigests = sortedStrings(result.VectorDigests)
	if err := result.Validate(); err != nil {
		return ConformanceReceipt{}, err
	}
	_, vectors, _ := releaseIdentitySets(release.set.Members)
	if result.ReleaseSetDigest != release.digest || result.InventoryDigest != release.set.InventoryRef ||
		!equalStrings(result.VectorDigests, vectors) {
		return ConformanceReceipt{}, e.New(e.CodeGateUnmet,
			"conformance result does not exhaustively bind release set, inventory, and validation vectors")
	}
	payload, err := canonical.Marshal(result)
	if err != nil {
		return ConformanceReceipt{}, err
	}
	signer, err := authenticator.AuthenticateReceipt(ctx, append([]byte(nil), payload...),
		append([]byte(nil), signature...), RoleConformance)
	if ctxErr := contextErr(ctx); ctxErr != nil {
		return ConformanceReceipt{}, ctxErr
	}
	if err != nil {
		return ConformanceReceipt{}, e.Wrap(e.CodeSignatureInvalid, err, "authenticating conformance receipt")
	}
	signer = strings.TrimSpace(signer)
	if signer == "" {
		return ConformanceReceipt{}, e.New(e.CodeSignatureInvalid, "conformance verifier returned no signer identity")
	}
	if signer == release.signer {
		return ConformanceReceipt{}, e.New(e.CodeGateUnmet, "conformance signer %q must be distinct from release signer", signer)
	}
	digest, err := canonical.Digest(canonical.KindConformanceResult, result)
	if err != nil {
		return ConformanceReceipt{}, err
	}
	return ConformanceReceipt{
		valid: true, result: result, verifiedSigner: signer,
		releaseSetDigest: release.digest, canonicalPayload: string(payload), digest: digest,
	}, nil
}

// AuthorizationRequest contains documents and verified receipts needed to evaluate a
// release. Receipt values are capabilities; zero values are invalid.
type AuthorizationRequest struct {
	Release                 ReleaseSetReceipt
	Inventory               *Inventory
	Reproduction            []ReproductionReceipt
	Compatibility           CompatibilityReceipt
	Conformance             []ConformanceReceipt
	Target                  CompatibilityTarget
	ExternalEvidence        []sbom.ExternalEvidenceReceipt
	LocalSBOMEvidence       []sbom.LocalEvidenceReceipt
	LocalProvenanceEvidence []provenance.LocalEvidenceReceipt
	ValidationVectors       []attest.PublishedVectorReceipt
	VulnerabilityReports    []vuln.QualifiedReport
	EvidenceWaivers         []EvidenceWaiverReceipt
	Status                  StatusBinding
	PromotionPolicy         PromotionPolicyReceipt
	AsOfEpoch               int64
}

type memberProof struct {
	Component           string   `json:"component"`
	Arch                string   `json:"arch"`
	Version             string   `json:"version,omitempty"`
	BuildDigest         string   `json:"buildDigest"`
	VectorDigest        string   `json:"vectorDigest"`
	ProfileDigest       string   `json:"profileDigest,omitempty"`
	ProfileIndependent  bool     `json:"profileIndependent,omitempty"`
	ResolvedInputDigest string   `json:"resolvedInputDigest"`
	ArtifactDigests     []string `json:"artifactDigests,omitempty"`
	Reproduction        string   `json:"reproduction"`
	VerifiedBy          string   `json:"verifiedBy"`
	ReceiptDigest       string   `json:"receiptDigest"`
	EffectiveEpoch      int64    `json:"effectiveEpoch"`
	ExpiresEpoch        int64    `json:"expiresEpoch"`
}

type conformanceProof struct {
	ReceiptDigest     string   `json:"receiptDigest"`
	ReleaseSetDigest  string   `json:"releaseSetDigest"`
	InventoryDigest   string   `json:"inventoryDigest"`
	VectorDigests     []string `json:"vectorDigests"`
	Suite             string   `json:"suite"`
	SuitePolicyDigest string   `json:"suitePolicyDigest"`
	ResultsDigest     string   `json:"resultsDigest"`
	Verdict           string   `json:"verdict"`
	VerifiedSigner    string   `json:"verifiedSigner"`
	EffectiveEpoch    int64    `json:"effectiveEpoch"`
	ExpiresEpoch      int64    `json:"expiresEpoch"`
}

type compatibilityProof struct {
	ReceiptDigest             string              `json:"receiptDigest"`
	CompatibilityPolicyDigest string              `json:"compatibilityPolicyDigest"`
	Target                    CompatibilityTarget `json:"target"`
	BuildDigests              []string            `json:"buildDigests"`
	VectorDigests             []string            `json:"vectorDigests"`
	ArtifactDigests           []string            `json:"artifactDigests"`
	Verdict                   string              `json:"verdict"`
	VerifiedSigner            string              `json:"verifiedSigner"`
	EffectiveEpoch            int64               `json:"effectiveEpoch"`
	ExpiresEpoch              int64               `json:"expiresEpoch"`
}

type authorizationRecord struct {
	SchemaVersion             string                  `json:"schemaVersion"`
	ReleaseSetDigest          string                  `json:"releaseSetDigest"`
	ReleaseVersion            string                  `json:"releaseVersion"`
	PublicationIdentity       string                  `json:"publicationIdentity"`
	InventoryDigest           string                  `json:"inventoryDigest"`
	ReleaseSigner             string                  `json:"releaseSigner"`
	Target                    CompatibilityTarget     `json:"compatibilityTarget"`
	Members                   []memberProof           `json:"members"`
	ValidationVectors         []validationVectorProof `json:"validationVectors"`
	Compatibility             compatibilityProof      `json:"compatibility"`
	Conformance               conformanceProof        `json:"conformance"`
	Evidence                  []evidenceProof         `json:"evidence"`
	Waivers                   []waiverProof           `json:"waivers,omitempty"`
	ArtifactDigests           []string                `json:"artifactDigests"`
	EvidenceDigests           []string                `json:"evidenceDigests"`
	PromotionPolicyDigest     string                  `json:"promotionPolicyDigest,omitempty"`
	PromotionPolicySigner     string                  `json:"promotionPolicySigner"`
	PromotionPolicyEffective  int64                   `json:"promotionPolicyEffectiveEpoch"`
	PromotionPolicyExpires    int64                   `json:"promotionPolicyExpiresEpoch"`
	CompatibilityPolicyDigest string                  `json:"compatibilityPolicyDigest"`
	ConformancePolicyDigest   string                  `json:"conformancePolicyDigest"`
	StatusCheckpointDigest    string                  `json:"statusCheckpointDigest,omitempty"`
	StatusPolicyBinding       string                  `json:"statusPolicyBinding"`
	StatusTimeAuthority       string                  `json:"statusTimeAuthority"`
	StatusTimeBinding         string                  `json:"statusTimeBinding"`
	AsOfEpoch                 int64                   `json:"asOfEpoch,omitempty"`
	Publication               PublicationManifest     `json:"publicationManifest"`
}

// Authorization is the capability issued after all current checks pass. It has no
// public gate fields and cannot be populated by JSON deserialization.
type Authorization struct {
	valid            bool
	record           authorizationRecord
	clearanceSigner  string
	canonicalPayload string
}

// Authorize validates identity, completeness, compatibility, reproduction receipts,
// and independently signed conformance receipts in one locality.
func Authorize(ctx context.Context, req AuthorizationRequest) (*Authorization, error) {
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	if !req.Release.valid {
		return nil, e.New(e.CodeUnverifiedRef, "release authorization requires a verified release-set receipt")
	}
	recomputedSetDigest, err := req.Release.set.Digest()
	if err != nil {
		return nil, err
	}
	if recomputedSetDigest != req.Release.digest {
		return nil, e.New(e.CodeUnverifiedRef, "release-set receipt no longer matches its verified digest")
	}
	recomputedSetPayload, err := req.Release.set.Marshal()
	if err != nil {
		return nil, err
	}
	if string(recomputedSetPayload) != req.Release.canonicalPayload {
		return nil, e.New(e.CodeSignatureInvalid,
			"release-set receipt no longer matches its authenticated canonical bytes")
	}
	if err := req.Inventory.Validate(); err != nil {
		return nil, err
	}
	invDigest, err := req.Inventory.Digest()
	if err != nil {
		return nil, err
	}
	if req.Release.set.InventoryRef != invDigest {
		return nil, e.New(e.CodeInventoryUnpinned, "release set inventoryRef %s does not match supplied inventory %s", req.Release.set.InventoryRef, invDigest)
	}
	if err := req.Release.set.CompletenessGate(req.Inventory); err != nil {
		return nil, err
	}
	if err := CheckCompatibility(req.Inventory, req.Target); err != nil {
		return nil, err
	}
	if err := checkMemberCompatibility(req.Inventory, &req.Release.set); err != nil {
		return nil, err
	}
	if req.AsOfEpoch <= 0 {
		return nil, e.New(e.CodeGateUnmet,
			"release authorization requires a positive as-of epoch")
	}
	promotionPolicy, err := req.PromotionPolicy.validate(req.AsOfEpoch)
	if err != nil {
		return nil, err
	}
	promotionPolicyDigest, err := req.PromotionPolicy.Digest()
	if err != nil {
		return nil, err
	}
	if err := req.Status.validate(); err != nil {
		return nil, err
	}
	publicationIdentity, err := req.Release.PublicationIdentity()
	if err != nil {
		return nil, err
	}
	if req.Status.target != publicationIdentity || req.Status.asOfEpoch != req.AsOfEpoch {
		return nil, e.New(e.CodeStatusCheckpoint,
			"status checkpoint target %s is not release publication %s at authorization as-of epoch %d",
			req.Status.target, publicationIdentity, req.AsOfEpoch)
	}
	validationVectors, err := verifyAuthenticatedVectorChain(ctx, req)
	if err != nil {
		return nil, err
	}
	qualifiedEvidence, err := evaluateQualifiedEvidence(ctx, req, promotionPolicy, promotionPolicyDigest)
	if err != nil {
		return nil, err
	}

	reproduction := map[string]ReproductionReceipt{}
	for _, receipt := range req.Reproduction {
		if !receipt.valid || receipt.verifiedSigner == "" || receipt.result.ReleaseSetDigest != req.Release.digest {
			return nil, e.New(e.CodeGateUnmet,
				"authorization contains reproduction not authenticated for release set %s", req.Release.digest)
		}
		if err := verifyStoredReceipt(receipt.result, receipt.canonicalPayload, receipt.digest,
			canonical.KindReproductionResult); err != nil {
			return nil, err
		}
		if err := receiptCurrent(receipt.result.EffectiveEpoch, receipt.result.ExpiresEpoch,
			req.AsOfEpoch, "reproduction"); err != nil {
			return nil, err
		}
		if receipt.result.Verdict != Reproduced {
			return nil, e.New(e.CodeGateUnmet,
				"reproduction receipt for %s/%s has non-passing verdict %q",
				receipt.result.Component, receipt.result.Arch, receipt.result.Verdict)
		}
		key := receipt.result.Component + "\x00" + receipt.result.Arch
		if _, duplicate := reproduction[key]; duplicate {
			return nil, e.New(e.CodeGateUnmet,
				"authorization repeats reproduction receipt for %s/%s",
				receipt.result.Component, receipt.result.Arch)
		}
		reproduction[key] = receipt
	}
	if len(reproduction) != len(req.Release.set.Members) {
		return nil, e.New(e.CodeGateUnmet,
			"authenticated reproduction receipt count %d does not match release member count %d",
			len(reproduction), len(req.Release.set.Members))
	}

	compatibility := req.Compatibility
	if !compatibility.valid || compatibility.verifiedSigner == "" {
		return nil, e.New(e.CodeGateUnmet, "release authorization requires an authenticated compatibility receipt")
	}
	if err := verifyStoredReceipt(compatibility.result, compatibility.canonicalPayload, compatibility.digest,
		canonical.KindCompatibilityResult); err != nil {
		return nil, err
	}
	if err := receiptCurrent(compatibility.result.EffectiveEpoch, compatibility.result.ExpiresEpoch,
		req.AsOfEpoch, "compatibility"); err != nil {
		return nil, err
	}
	builds, vectors, expectedArtifacts := releaseIdentitySets(req.Release.set.Members)
	if compatibility.result.ReleaseSetDigest != req.Release.digest ||
		compatibility.result.InventoryDigest != invDigest ||
		compatibility.result.CompatibilityPolicyDigest != promotionPolicy.CompatibilityPolicyDigest ||
		compatibility.result.Target != req.Target || compatibility.result.Verdict != CompatibilityAccepted ||
		!equalStrings(compatibility.result.BuildDigests, builds) ||
		!equalStrings(compatibility.result.VectorDigests, vectors) ||
		!equalStrings(compatibility.result.ArtifactDigests, expectedArtifacts) {
		return nil, e.New(e.CodeGateUnmet,
			"compatibility receipt does not accept the exact release, inventory, target, policy, and identity sets")
	}

	if len(req.Conformance) != 1 {
		return nil, e.New(e.CodeGateUnmet,
			"release authorization requires exactly one exhaustive conformance receipt (got %d)", len(req.Conformance))
	}
	conformance := req.Conformance[0]
	if !conformance.valid || conformance.verifiedSigner == "" ||
		conformance.releaseSetDigest != req.Release.digest {
		return nil, e.New(e.CodeGateUnmet,
			"authorization contains conformance not authenticated for release set %s", req.Release.digest)
	}
	if err := verifyStoredReceipt(conformance.result, conformance.canonicalPayload, conformance.digest,
		canonical.KindConformanceResult); err != nil {
		return nil, err
	}
	if err := receiptCurrent(conformance.result.EffectiveEpoch, conformance.result.ExpiresEpoch,
		req.AsOfEpoch, "conformance"); err != nil {
		return nil, err
	}
	if conformance.result.ReleaseSetDigest != req.Release.digest ||
		conformance.result.InventoryDigest != invDigest ||
		conformance.result.SuitePolicyDigest != promotionPolicy.ConformancePolicyDigest ||
		conformance.result.Verdict != ConformancePassed ||
		!equalStrings(conformance.result.VectorDigests, vectors) {
		return nil, e.New(e.CodeGateUnmet,
			"conformance receipt does not pass the exact release, inventory, vector set, and suite policy")
	}

	artifacts := map[string]bool{}
	record := authorizationRecord{
		SchemaVersion: AuthorizationSchemaVersion, ReleaseSetDigest: req.Release.digest,
		ReleaseVersion:      req.Release.set.Version,
		PublicationIdentity: publicationIdentity,
		InventoryDigest:     invDigest, ReleaseSigner: req.Release.signer, Target: req.Target,
		ValidationVectors: validationVectors,
		Evidence:          qualifiedEvidence.proofs, Waivers: qualifiedEvidence.waivers,
		EvidenceDigests:           append([]string(nil), qualifiedEvidence.digests...),
		PromotionPolicyDigest:     qualifiedEvidence.promotionPolicyDigest,
		PromotionPolicySigner:     req.PromotionPolicy.verifiedSigner,
		PromotionPolicyEffective:  promotionPolicy.EffectiveEpoch,
		PromotionPolicyExpires:    promotionPolicy.ExpiresEpoch,
		CompatibilityPolicyDigest: promotionPolicy.CompatibilityPolicyDigest,
		ConformancePolicyDigest:   promotionPolicy.ConformancePolicyDigest,
		StatusCheckpointDigest:    qualifiedEvidence.statusCheckpointDigest,
		StatusPolicyBinding:       req.Status.policyBinding,
		StatusTimeAuthority:       req.Status.timeAuthority,
		StatusTimeBinding:         req.Status.timeBinding,
		AsOfEpoch:                 qualifiedEvidence.asOfEpoch,
		Compatibility: compatibilityProof{
			ReceiptDigest:             compatibility.digest,
			CompatibilityPolicyDigest: compatibility.result.CompatibilityPolicyDigest,
			Target:                    compatibility.result.Target,
			BuildDigests:              append([]string(nil), compatibility.result.BuildDigests...),
			VectorDigests:             append([]string(nil), compatibility.result.VectorDigests...),
			ArtifactDigests:           append([]string(nil), compatibility.result.ArtifactDigests...),
			Verdict:                   compatibility.result.Verdict, VerifiedSigner: compatibility.verifiedSigner,
			EffectiveEpoch: compatibility.result.EffectiveEpoch, ExpiresEpoch: compatibility.result.ExpiresEpoch,
		},
		Conformance: conformanceProof{
			ReceiptDigest: conformance.digest, ReleaseSetDigest: conformance.result.ReleaseSetDigest,
			InventoryDigest: conformance.result.InventoryDigest,
			VectorDigests:   append([]string(nil), conformance.result.VectorDigests...),
			Suite:           conformance.result.Suite, SuitePolicyDigest: conformance.result.SuitePolicyDigest,
			ResultsDigest: conformance.result.ResultsDigest, Verdict: conformance.result.Verdict,
			VerifiedSigner: conformance.verifiedSigner,
			EffectiveEpoch: conformance.result.EffectiveEpoch, ExpiresEpoch: conformance.result.ExpiresEpoch,
		},
	}
	for _, member := range req.Release.set.sorted().Members {
		receipt, ok := reproduction[memberKey(member)]
		if !ok || !reproductionMatchesMember(receipt.result, member) {
			return nil, e.New(e.CodeGateUnmet,
				"member %s/%s lacks a reproduction receipt bound to its exact build, vector, profile, resolved inputs, artifacts, and verdict",
				member.Component, member.Arch)
		}
		proof := memberProof{
			Component: member.Component, Arch: member.Arch, Version: member.Version,
			BuildDigest: member.BuildDigest, VectorDigest: member.VectorDigest,
			ProfileDigest: member.ProfileDigest, ProfileIndependent: member.ProfileDigest == "",
			ResolvedInputDigest: member.ResolvedInputDigest,
			ArtifactDigests:     append([]string(nil), member.ArtifactDigests...),
			Reproduction:        receipt.result.Verdict, VerifiedBy: receipt.verifiedSigner,
			ReceiptDigest: receipt.digest, EffectiveEpoch: receipt.result.EffectiveEpoch,
			ExpiresEpoch: receipt.result.ExpiresEpoch,
		}
		record.Members = append(record.Members, proof)
		for _, digest := range member.ArtifactDigests {
			artifacts[digest] = true
		}
	}
	record.ArtifactDigests = sortedKeys(artifacts)
	publication, err := buildPublicationManifest(req, publicationIdentity,
		record.ArtifactDigests, record.Evidence)
	if err != nil {
		return nil, err
	}
	record.Publication = publication
	payload, err := canonical.Marshal(record)
	if err != nil {
		return nil, err
	}
	return &Authorization{valid: true, record: record, canonicalPayload: string(payload)}, nil
}

// Digest is the immutable identity of the authorization record.
func (authorization *Authorization) Digest() (string, error) {
	if err := authorization.validateStored(); err != nil {
		return "", e.Wrap(e.CodeGateUnmet, err, "invalid release authorization")
	}
	return canonical.Digest(canonical.KindClearance, authorization.record)
}

// Marshal returns the canonical authorization document. Deserializing these bytes
// does not recreate the capability; verification must be rerun.
func (authorization *Authorization) Marshal() ([]byte, error) {
	if err := authorization.validateStored(); err != nil {
		return nil, e.Wrap(e.CodeGateUnmet, err, "invalid release authorization")
	}
	return append([]byte(nil), authorization.canonicalPayload...), nil
}

// ReleaseSetDigest returns the exact release identity authorized by this capability.
func (authorization *Authorization) ReleaseSetDigest() (string, error) {
	if err := authorization.validateStored(); err != nil {
		return "", e.Wrap(e.CodeGateUnmet, err, "invalid release authorization")
	}
	return authorization.record.ReleaseSetDigest, nil
}

// InventoryDigest returns the exact Inventory identity rehydrated and checked by
// this authorization capability.
func (authorization *Authorization) InventoryDigest() (string, error) {
	if err := authorization.validateStored(); err != nil {
		return "", e.Wrap(e.CodeGateUnmet, err, "invalid release authorization")
	}
	return authorization.record.InventoryDigest, nil
}

// PublicationIdentity returns ADR-06's immutable lifecycle identity for the
// exact normalized ReleaseSet carried by this authorization.
func (authorization *Authorization) PublicationIdentity() (string, error) {
	if err := authorization.validateStored(); err != nil {
		return "", e.Wrap(e.CodeGateUnmet, err, "invalid release authorization")
	}
	return authorization.record.PublicationIdentity, nil
}

// ArtifactDigests returns a defensive copy of the exact cleared artifact set.
func (authorization *Authorization) ArtifactDigests() ([]string, error) {
	if err := authorization.validateStored(); err != nil {
		return nil, e.Wrap(e.CodeGateUnmet, err, "invalid release authorization")
	}
	return append([]string(nil), authorization.record.ArtifactDigests...), nil
}

// EvidenceDigests returns a defensive copy of evidence identities checked by
// completeness and member verification.
func (authorization *Authorization) EvidenceDigests() ([]string, error) {
	if err := authorization.validateStored(); err != nil {
		return nil, e.Wrap(e.CodeGateUnmet, err, "invalid release authorization")
	}
	return append([]string(nil), authorization.record.EvidenceDigests...), nil
}

// PublicationManifest returns a defensive copy of the exact artifact/referrer
// closure authorized for transport.
func (authorization *Authorization) PublicationManifest() (PublicationManifest, error) {
	if err := authorization.validateStored(); err != nil {
		return PublicationManifest{}, e.Wrap(e.CodeGateUnmet, err, "invalid release authorization")
	}
	manifest := authorization.record.Publication
	manifest.ArtifactDigests = make([]string, len(authorization.record.Publication.ArtifactDigests))
	copy(manifest.ArtifactDigests, authorization.record.Publication.ArtifactDigests)
	manifest.Referrers = make([]PublicationReferrer, len(authorization.record.Publication.Referrers))
	copy(manifest.Referrers, authorization.record.Publication.Referrers)
	if err := manifest.Validate(); err != nil {
		return PublicationManifest{}, err
	}
	return manifest, nil
}

// RequirePublicationCommit fails closed until NKD has an authenticated,
// non-circular publication-root protocol that carries the exact RoleClearance
// document, detached signature material, and discovery mapping for
// PublicationIdentity. Authorization alone clears content; it does not claim that
// this missing cross-process commit protocol exists.
func (authorization *Authorization) RequirePublicationCommit() error {
	if err := authorization.validateStored(); err != nil {
		return e.New(e.CodeGateUnmet,
			"release publication requires a valid authorization capability")
	}
	// This is a deliberate two-stage gate, not vestigial ordering. A freshly minted
	// Authorization is a content-clearance (every gate passed) but NOT a signed clearance:
	// clearanceSigner is empty until the record is signed under trust.RoleClearance and
	// rehydrated through VerifyAuthenticatedAuthorization. So an in-process fresh
	// authorization fails first with CodeGateUnmet — it has not proven a signed clearance
	// authority — while a signed+reauthenticated clearance passes that stage and then
	// fails closed with CodeEngineCapability because the cross-process publication-commit
	// protocol (authenticated root, detached signature, publication-identity discovery)
	// is not yet implemented. The two errors are distinct on purpose: "you have no signed
	// clearance" vs "your signed clearance is real but the commit protocol is missing".
	if strings.TrimSpace(authorization.clearanceSigner) == "" {
		return e.New(e.CodeGateUnmet,
			"release publication requires a signed, reauthenticated %s clearance, not an in-process authorization",
			trust.RoleClearance)
	}
	return e.New(e.CodeEngineCapability,
		"qualified release publication is unavailable until the authenticated %s root, detached signature, and publication-identity discovery protocol is implemented",
		trust.RoleClearance)
}

func sortedKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeTimeout, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "release authorization canceled")
	}
	return nil
}
