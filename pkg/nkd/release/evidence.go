package release

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/status"
)

const EvidenceWaiverSchemaVersion = "nkd.dev/evidence-waiver/v3"

// EvidenceType is a closed set. Its JSON form is a human-readable string, but Go
// callers cannot accidentally turn an arbitrary string into a passing requirement.
type EvidenceType uint8

const (
	EvidenceSBOM EvidenceType = iota + 1
	EvidenceProvenance
	EvidenceVulnerability
)

func (evidenceType EvidenceType) String() string {
	switch evidenceType {
	case EvidenceSBOM:
		return "sbom"
	case EvidenceProvenance:
		return "provenance"
	case EvidenceVulnerability:
		return "vulnerability"
	default:
		return ""
	}
}

func (evidenceType EvidenceType) valid() bool { return evidenceType.String() != "" }

func (evidenceType EvidenceType) MarshalJSON() ([]byte, error) {
	if !evidenceType.valid() {
		return nil, e.New(e.CodeInvalidCatalog, "unknown evidence type %d", evidenceType)
	}
	return json.Marshal(evidenceType.String())
}

func (evidenceType *EvidenceType) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "decoding evidence type")
	}
	switch value {
	case "sbom":
		*evidenceType = EvidenceSBOM
	case "provenance":
		*evidenceType = EvidenceProvenance
	case "vulnerability":
		*evidenceType = EvidenceVulnerability
	default:
		return e.New(e.CodeInvalidCatalog, "unknown evidence type %q", value)
	}
	return nil
}

// EvidenceGapReason records why a signed Inventory requires a waiver. It never
// grants a waiver by itself.
type EvidenceGapReason uint8

const (
	EvidenceNotDeclared EvidenceGapReason = iota + 1
	EvidenceNone
)

func (reason EvidenceGapReason) String() string {
	switch reason {
	case EvidenceNotDeclared:
		return "not-declared"
	case EvidenceNone:
		return "evidence:none"
	default:
		return ""
	}
}

func (reason EvidenceGapReason) valid() bool { return reason.String() != "" }

func (reason EvidenceGapReason) MarshalJSON() ([]byte, error) {
	if !reason.valid() {
		return nil, e.New(e.CodeInvalidCatalog, "unknown evidence gap reason %d", reason)
	}
	return json.Marshal(reason.String())
}

func (reason *EvidenceGapReason) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "decoding evidence gap reason")
	}
	switch value {
	case "not-declared":
		*reason = EvidenceNotDeclared
	case "evidence:none":
		*reason = EvidenceNone
	default:
		return e.New(e.CodeInvalidCatalog, "unknown evidence gap reason %q", value)
	}
	return nil
}

// EvidenceGapDeclaration is policy data in the digest-bound Inventory. It names
// an exact artifact and closed set of missing evidence classes. It is not an
// authorization capability; only a matching EvidenceWaiverReceipt can permit it.
type EvidenceGapDeclaration struct {
	ArtifactDigest string            `json:"artifactDigest"`
	MissingTypes   []EvidenceType    `json:"missingTypes"`
	Reason         EvidenceGapReason `json:"reason"`
}

// ProfileBinding makes profile independence explicit instead of overloading an
// empty digest. Its state is private so zero values fail closed.
type ProfileBinding struct {
	valid       bool
	independent bool
	digest      string
}

func BindBuilderProfile(digest string) (ProfileBinding, error) {
	if !isDigest(digest) {
		return ProfileBinding{}, e.New(e.CodeUnverifiedRef,
			"builder-profile binding %q is not an exact digest", digest)
	}
	return ProfileBinding{valid: true, digest: digest}, nil
}

func BindProfileIndependent() ProfileBinding {
	return ProfileBinding{valid: true, independent: true}
}

// StatusBinding is the deliberately narrow release projection of an opaque,
// authenticated checkpoint capability observed at policy-pinned trusted time. A
// raw digest or caller-selected as-of epoch can never create this binding.
type StatusBinding struct {
	valid            bool
	checkpointDigest string
	target           string
	policyBinding    string
	asOfEpoch        int64
	timeAuthority    string
	timeBinding      string
	receipt          status.CheckpointReceipt
}

// BindVerifiedStatusCheckpoint adapts ADR-06's opaque authenticated, fresh,
// anti-rollback-checked checkpoint capability into the release boundary.
func BindVerifiedStatusCheckpoint(receipt status.CheckpointReceipt) (StatusBinding, error) {
	target, err := receipt.Target()
	if err != nil {
		return StatusBinding{}, err
	}
	checkpointDigest, err := receipt.CheckpointDigest()
	if err != nil {
		return StatusBinding{}, err
	}
	policyBinding, err := receipt.PolicyBinding()
	if err != nil {
		return StatusBinding{}, err
	}
	asOfEpoch, err := receipt.AsOfEpoch()
	if err != nil {
		return StatusBinding{}, err
	}
	timeAuthority, err := receipt.TrustedTimeAuthority()
	if err != nil {
		return StatusBinding{}, err
	}
	timeBinding, err := receipt.TrustedTimeBinding()
	if err != nil {
		return StatusBinding{}, err
	}
	if _, err := receipt.Decision(); err != nil {
		return StatusBinding{}, err
	}
	return StatusBinding{valid: true, checkpointDigest: checkpointDigest, target: target,
		policyBinding: policyBinding, asOfEpoch: asOfEpoch, timeAuthority: timeAuthority,
		timeBinding: timeBinding, receipt: receipt}, nil
}

func (binding StatusBinding) validate() error {
	if !binding.valid || !isDigest(binding.checkpointDigest) || !isDigest(binding.target) ||
		!isDigest(binding.policyBinding) || binding.asOfEpoch <= 0 ||
		strings.TrimSpace(binding.timeAuthority) == "" ||
		strings.TrimSpace(binding.timeAuthority) != binding.timeAuthority ||
		!isDigest(binding.timeBinding) {
		return e.New(e.CodeStatusCheckpoint, "release status checkpoint binding is absent or invalid")
	}
	target, err := binding.receipt.Target()
	if err != nil || target != binding.target {
		return e.New(e.CodeStatusCheckpoint, "release status checkpoint target changed after binding")
	}
	digest, err := binding.receipt.CheckpointDigest()
	if err != nil || digest != binding.checkpointDigest {
		return e.New(e.CodeStatusCheckpoint, "release status checkpoint digest changed after binding")
	}
	policy, err := binding.receipt.PolicyBinding()
	if err != nil || policy != binding.policyBinding {
		return e.New(e.CodeStatusCheckpoint, "release status checkpoint policy changed after binding")
	}
	asOf, err := binding.receipt.AsOfEpoch()
	if err != nil || asOf != binding.asOfEpoch {
		return e.New(e.CodeStatusCheckpoint, "release status checkpoint freshness epoch changed after binding")
	}
	timeAuthority, err := binding.receipt.TrustedTimeAuthority()
	if err != nil || timeAuthority != binding.timeAuthority {
		return e.New(e.CodeStatusCheckpoint,
			"release status checkpoint time authority changed after binding")
	}
	timeBinding, err := binding.receipt.TrustedTimeBinding()
	if err != nil || timeBinding != binding.timeBinding {
		return e.New(e.CodeStatusCheckpoint,
			"release status checkpoint time observation changed after binding")
	}
	if _, err := binding.receipt.Decision(); err != nil {
		return err
	}
	return nil
}

// EvidenceWaiverRequest is the exact exception document submitted to the waiver
// authority. MissingTypes uses a closed enum and Profile/Status are typed bindings;
// there is no boolean or free-form string that can authorize absence.
type EvidenceWaiverRequest struct {
	Component             string
	BuildDigest           string
	ArtifactDigest        string
	Profile               ProfileBinding
	Status                StatusBinding
	MissingTypes          []EvidenceType
	Reason                EvidenceGapReason
	PromotionPolicyDigest string
	ApproverIdentity      string
	IssuedEpoch           int64
	ExpiresEpoch          int64
}

// EvidenceWaiverVerifier is the trust-policy seam. Implementations authenticate
// the signature and enforce the configured waiver-approver role before returning
// the verified signer identity.
type EvidenceWaiverVerifier interface {
	VerifyEvidenceWaiver(ctx context.Context, canonicalPayload, signature []byte) (verifiedApprover string, err error)
}

type evidenceWaiverDocument struct {
	SchemaVersion          string         `json:"schemaVersion"`
	Component              string         `json:"component"`
	BuildDigest            string         `json:"buildDigest"`
	ArtifactDigest         string         `json:"artifactDigest"`
	ProfileScope           string         `json:"profileScope"`
	ProfileDigest          string         `json:"profileDigest,omitempty"`
	StatusCheckpointDigest string         `json:"statusCheckpointDigest"`
	MissingTypes           []EvidenceType `json:"missingTypes"`
	Reason                 string         `json:"reason"`
	PromotionPolicyDigest  string         `json:"promotionPolicyDigest"`
	ApproverIdentity       string         `json:"approverIdentity"`
	IssuedEpoch            int64          `json:"issuedEpoch"`
	ExpiresEpoch           int64          `json:"expiresEpoch"`
}

// EvidenceWaiverReceipt is an opaque authenticated capability. Its critical
// fields and signed canonical payload are private and are rechecked by Authorize.
type EvidenceWaiverReceipt struct {
	valid            bool
	document         evidenceWaiverDocument
	canonicalPayload string
	digest           string
	verifiedApprover string
}

// VerifyEvidenceWaiver canonicalizes the exact exception request and delegates
// signature/role enforcement to the authority seam before issuing a receipt.
func VerifyEvidenceWaiver(ctx context.Context, request EvidenceWaiverRequest, signature []byte,
	verifier EvidenceWaiverVerifier,
) (EvidenceWaiverReceipt, error) {
	if err := contextErr(ctx); err != nil {
		return EvidenceWaiverReceipt{}, err
	}
	if verifier == nil || len(signature) == 0 {
		return EvidenceWaiverReceipt{}, e.New(e.CodeSignatureInvalid,
			"evidence waiver requires an authority verifier and signature bytes")
	}
	document, err := waiverDocument(request)
	if err != nil {
		return EvidenceWaiverReceipt{}, err
	}
	payload, err := canonical.Marshal(document)
	if err != nil {
		return EvidenceWaiverReceipt{}, err
	}
	approver, err := verifier.VerifyEvidenceWaiver(ctx,
		append([]byte(nil), payload...), append([]byte(nil), signature...))
	if ctxErr := contextErr(ctx); ctxErr != nil {
		return EvidenceWaiverReceipt{}, ctxErr
	}
	if err != nil {
		return EvidenceWaiverReceipt{}, e.Wrap(e.CodeSignatureInvalid, err,
			"authenticating evidence waiver")
	}
	approver = strings.TrimSpace(approver)
	if approver == "" || approver != document.ApproverIdentity {
		return EvidenceWaiverReceipt{}, e.New(e.CodeSignatureInvalid,
			"evidence waiver signer %q does not match bound approver %q", approver, document.ApproverIdentity)
	}
	digest, err := canonical.Digest(canonical.KindEvidenceWaiver, document)
	if err != nil {
		return EvidenceWaiverReceipt{}, err
	}
	return EvidenceWaiverReceipt{valid: true, document: document,
		canonicalPayload: string(payload), digest: digest, verifiedApprover: approver}, nil
}

func waiverDocument(request EvidenceWaiverRequest) (evidenceWaiverDocument, error) {
	if strings.TrimSpace(request.Component) == "" {
		return evidenceWaiverDocument{}, e.New(e.CodeInvalidCatalog,
			"evidence waiver component is required")
	}
	if !isDigest(request.BuildDigest) || !isDigest(request.ArtifactDigest) ||
		!isDigest(request.PromotionPolicyDigest) {
		return evidenceWaiverDocument{}, e.New(e.CodeUnverifiedRef,
			"evidence waiver must bind exact build, artifact, and promotion-policy digests")
	}
	if !request.Profile.valid || (request.Profile.independent && request.Profile.digest != "") ||
		(!request.Profile.independent && !isDigest(request.Profile.digest)) {
		return evidenceWaiverDocument{}, e.New(e.CodeUnverifiedRef,
			"evidence waiver requires an explicit profile or profile-independent binding")
	}
	if err := request.Status.validate(); err != nil {
		return evidenceWaiverDocument{}, err
	}
	missing, err := normalizeEvidenceTypes(request.MissingTypes)
	if err != nil {
		return evidenceWaiverDocument{}, err
	}
	if !request.Reason.valid() {
		return evidenceWaiverDocument{}, e.New(e.CodeInvalidCatalog,
			"evidence waiver has an unknown gap reason")
	}
	approver := strings.TrimSpace(request.ApproverIdentity)
	if approver == "" {
		return evidenceWaiverDocument{}, e.New(e.CodeSignatureInvalid,
			"evidence waiver must name its approver identity")
	}
	if request.IssuedEpoch <= 0 || request.ExpiresEpoch <= request.IssuedEpoch {
		return evidenceWaiverDocument{}, e.New(e.CodeGateUnmet,
			"evidence waiver expiry must be later than its positive issued epoch")
	}
	profileScope := "builder-profile"
	if request.Profile.independent {
		profileScope = "profile-independent"
	}
	return evidenceWaiverDocument{
		SchemaVersion: EvidenceWaiverSchemaVersion,
		Component:     strings.TrimSpace(request.Component), BuildDigest: request.BuildDigest, ArtifactDigest: request.ArtifactDigest,
		ProfileScope: profileScope, ProfileDigest: request.Profile.digest,
		StatusCheckpointDigest: request.Status.checkpointDigest,
		MissingTypes:           missing, Reason: request.Reason.String(),
		PromotionPolicyDigest: request.PromotionPolicyDigest,
		ApproverIdentity:      approver, IssuedEpoch: request.IssuedEpoch, ExpiresEpoch: request.ExpiresEpoch,
	}, nil
}

func normalizeEvidenceTypes(types []EvidenceType) ([]EvidenceType, error) {
	if len(types) == 0 {
		return nil, e.New(e.CodeInvalidCatalog, "evidence gap must name at least one missing type")
	}
	seen := make(map[EvidenceType]bool, len(types))
	copy := append([]EvidenceType(nil), types...)
	for _, evidenceType := range copy {
		if !evidenceType.valid() {
			return nil, e.New(e.CodeInvalidCatalog, "evidence gap contains unknown type %d", evidenceType)
		}
		if seen[evidenceType] {
			return nil, e.New(e.CodeInvalidCatalog, "evidence gap repeats type %s", evidenceType.String())
		}
		seen[evidenceType] = true
	}
	sort.Slice(copy, func(i, j int) bool { return copy[i] < copy[j] })
	return copy, nil
}

func evidenceTypesKey(types []EvidenceType) string {
	parts := make([]string, len(types))
	for index, evidenceType := range types {
		parts[index] = evidenceType.String()
	}
	return strings.Join(parts, ",")
}

// Digest returns the immutable identity of the authenticated waiver receipt.
func (receipt EvidenceWaiverReceipt) Digest() (string, error) {
	if !receipt.valid {
		return "", e.New(e.CodeSignatureInvalid, "evidence waiver is not authenticated")
	}
	return receipt.digest, nil
}
