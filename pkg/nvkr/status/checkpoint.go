// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package status

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
)

const (
	// CheckpointSchemaVersion identifies ADR-06's signed complete status snapshot.
	CheckpointSchemaVersion = "nvkr.dev/status-checkpoint/v1"
)

// StatusCheckpoint is the immutable, signed complete status view for one exact
// publication target and stable authority. DeclarationDigests must be a sorted,
// duplicate-free complete set; an empty non-nil slice is meaningful.
type StatusCheckpoint struct {
	SchemaVersion      string   `json:"schemaVersion"`
	Target             string   `json:"target"`
	Authority          string   `json:"authority"`
	Sequence           uint64   `json:"sequence"`
	PreviousCheckpoint string   `json:"previousCheckpoint,omitempty"`
	DeclarationDigests []string `json:"declarationDigests"`
	IssuedEpoch        int64    `json:"issuedEpoch"`
	ValidUntilEpoch    int64    `json:"validUntilEpoch"`
}

// Validate rejects ambiguous, incomplete, or non-canonical checkpoint declarations.
func (checkpoint *StatusCheckpoint) Validate() error {
	if checkpoint == nil {
		return e.New(e.CodeInvalidCatalog, "status checkpoint is required")
	}
	if checkpoint.SchemaVersion != CheckpointSchemaVersion {
		return e.New(e.CodeInvalidCatalog, "status checkpoint schemaVersion %q is unsupported (want %q)", checkpoint.SchemaVersion, CheckpointSchemaVersion)
	}
	if !isDigest(checkpoint.Target) {
		return e.New(e.CodeUnverifiedRef, "status checkpoint target %q is not an exact publication digest", checkpoint.Target)
	}
	if strings.TrimSpace(checkpoint.Authority) == "" || strings.TrimSpace(checkpoint.Authority) != checkpoint.Authority {
		return e.New(e.CodeInvalidCatalog, "status checkpoint authority must be a non-empty canonical identity")
	}
	if checkpoint.Sequence == 0 {
		if checkpoint.PreviousCheckpoint != "" {
			return e.New(e.CodeStatusCheckpoint, "genesis status checkpoint cannot name a predecessor")
		}
	} else if !isDigest(checkpoint.PreviousCheckpoint) {
		return e.New(e.CodeStatusCheckpoint, "status checkpoint sequence %d requires an exact predecessor digest", checkpoint.Sequence)
	}
	if checkpoint.DeclarationDigests == nil {
		return e.New(e.CodeStatusCheckpoint, "status checkpoint must declare a complete digest set; use an empty array when there are no declarations")
	}
	for index, digest := range checkpoint.DeclarationDigests {
		if !isDigest(digest) {
			return e.New(e.CodeUnverifiedRef, "status checkpoint declaration digest %q is not exact", digest)
		}
		if index > 0 && checkpoint.DeclarationDigests[index-1] >= digest {
			return e.New(e.CodeStatusCheckpoint, "status checkpoint declaration digests must be strictly sorted and duplicate-free")
		}
	}
	if checkpoint.IssuedEpoch <= 0 || checkpoint.ValidUntilEpoch <= checkpoint.IssuedEpoch {
		return e.New(e.CodeStatusCheckpoint, "status checkpoint requires a positive issued epoch and a later valid-until epoch")
	}
	return nil
}

// Marshal returns the exact canonical JSON signing payload.
func (checkpoint *StatusCheckpoint) Marshal() ([]byte, error) {
	if err := checkpoint.Validate(); err != nil {
		return nil, err
	}
	return canonical.Marshal(checkpoint)
}

// Digest returns the checkpoint's domain-separated immutable identity.
func (checkpoint *StatusCheckpoint) Digest() (string, error) {
	if err := checkpoint.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindStatusCheckpoint, checkpoint)
}

// CheckpointPolicy binds a stable status authority and its currently authorized
// signing identities to one exact target. Its state is private so a zero value
// cannot authorize a checkpoint.
type CheckpointPolicy struct {
	valid             bool
	target            string
	authority         string
	issuer            string
	authorizedSigners []string
	binding           string
}

// NewCheckpointPolicy constructs the policy applied after trust.DocumentReceipt
// has authenticated each signature. The signer list is policy, never a caller
// assertion that verification succeeded.
func NewCheckpointPolicy(target, authority, issuer string, authorizedSigners []string) (CheckpointPolicy, error) {
	if !isDigest(target) {
		return CheckpointPolicy{}, e.New(e.CodeUnverifiedRef, "status checkpoint policy target %q is not an exact digest", target)
	}
	if strings.TrimSpace(authority) == "" || strings.TrimSpace(authority) != authority {
		return CheckpointPolicy{}, e.New(e.CodeTrustPolicyMissing, "status checkpoint policy requires a canonical authority identity")
	}
	if strings.TrimSpace(issuer) == "" || strings.TrimSpace(issuer) != issuer {
		return CheckpointPolicy{}, e.New(e.CodeTrustPolicyMissing, "status checkpoint policy requires the exact authenticated issuer")
	}
	if len(authorizedSigners) == 0 {
		return CheckpointPolicy{}, e.New(e.CodeTrustPolicyMissing, "status checkpoint policy requires at least one authorized signer")
	}
	signers := append([]string(nil), authorizedSigners...)
	sort.Strings(signers)
	for index, signer := range signers {
		if strings.TrimSpace(signer) == "" || strings.TrimSpace(signer) != signer {
			return CheckpointPolicy{}, e.New(e.CodeTrustPolicyMissing, "status checkpoint policy contains a non-canonical signer identity")
		}
		if index > 0 && signers[index-1] == signer {
			return CheckpointPolicy{}, e.New(e.CodeTrustPolicyMissing, "status checkpoint policy repeats signer %q", signer)
		}
	}
	binding, err := canonical.Digest(canonical.KindStatusCheckpointPolicy, struct {
		Target    string   `json:"target"`
		Authority string   `json:"authority"`
		Issuer    string   `json:"issuer"`
		Signers   []string `json:"authorizedSigners"`
	}{Target: target, Authority: authority, Issuer: issuer, Signers: signers})
	if err != nil {
		return CheckpointPolicy{}, err
	}
	return CheckpointPolicy{valid: true, target: target, authority: authority, issuer: issuer,
		authorizedSigners: signers, binding: binding}, nil
}

func (policy CheckpointPolicy) authorizes(signer, issuer string) bool {
	if !policy.valid || issuer != policy.issuer {
		return false
	}
	index := sort.SearchStrings(policy.authorizedSigners, signer)
	return index < len(policy.authorizedSigners) && policy.authorizedSigners[index] == signer
}

// HighWaterMark is the local trust-policy minimum for one target and authority.
// NewHighWaterMark admits policy-pinned values for in-memory verification;
// AcceptDiagnosticCheckpoint owns protected storage and durable monotonic updates.
type HighWaterMark struct {
	valid      bool
	target     string
	authority  string
	sequence   uint64
	checkpoint string
}

// NewHighWaterMark validates a persisted or policy-pinned status minimum. Strict
// checkpoint verification has no stateless constructor: an exact minimum is required.
func NewHighWaterMark(target, authority string, sequence uint64, checkpointDigest string) (HighWaterMark, error) {
	if !isDigest(target) || !isDigest(checkpointDigest) {
		return HighWaterMark{}, e.New(e.CodeStatusCheckpoint, "status high-water mark requires exact target and checkpoint digests")
	}
	if strings.TrimSpace(authority) == "" || strings.TrimSpace(authority) != authority {
		return HighWaterMark{}, e.New(e.CodeStatusCheckpoint, "status high-water mark requires a canonical authority identity")
	}
	return HighWaterMark{valid: true, target: target, authority: authority,
		sequence: sequence, checkpoint: checkpointDigest}, nil
}

func (mark HighWaterMark) validate() error {
	if !mark.valid || !isDigest(mark.target) || !isDigest(mark.checkpoint) ||
		strings.TrimSpace(mark.authority) == "" || strings.TrimSpace(mark.authority) != mark.authority {
		return e.New(e.CodeStatusCheckpoint, "status high-water mark is absent or invalid")
	}
	return nil
}

// CheckpointRequest contains only authenticated document receipts and opaque
// policy state. No signature-success boolean or caller-provided signer claim exists.
type CheckpointRequest struct {
	Policy       CheckpointPolicy
	HighWater    HighWaterMark
	AsOf         AsOf
	Checkpoint   trust.DocumentReceipt
	Declarations []trust.DocumentReceipt
}

// CheckpointReceipt is the opaque capability produced after exact-set,
// authorization, freshness, and anti-rollback validation. All accessors validate
// the receipt and return an error for its zero value.
type CheckpointReceipt struct {
	valid                      bool
	checkpoint                 StatusCheckpoint
	checkpointDigest           string
	checkpointSigner           string
	checkpointIssuer           string
	checkpointCertificate      string
	checkpointCanonicalPayload string
	policyBinding              string
	asOfEpoch                  int64
	declarations               []VerifiedDeclaration
}

// VerifyCheckpoint consumes trust.DocumentReceipt capabilities for the checkpoint
// and every declaration in its complete set. It rejects stale views, equivocation,
// broken predecessor linkage, expiry, omissions, extras, and unauthorized signers.
func VerifyCheckpoint(ctx context.Context, request CheckpointRequest) (CheckpointReceipt, error) {
	if err := statusContextErr(ctx); err != nil {
		return CheckpointReceipt{}, err
	}
	if err := request.Policy.validate(); err != nil {
		return CheckpointReceipt{}, err
	}
	if err := request.HighWater.validate(); err != nil {
		return CheckpointReceipt{}, err
	}
	if request.HighWater.target != request.Policy.target || request.HighWater.authority != request.Policy.authority {
		return CheckpointReceipt{}, e.New(e.CodeStatusCheckpoint, "status high-water target/authority does not match checkpoint policy")
	}
	if !request.AsOf.set || request.AsOf.epoch <= 0 {
		return CheckpointReceipt{}, e.New(e.CodeStatusCheckpoint, "strict status checkpoint verification requires an explicit positive as-of epoch")
	}

	authenticated, err := checkpointFromDocumentReceipt(ctx, request.Checkpoint)
	if err != nil {
		return CheckpointReceipt{}, err
	}
	checkpoint := authenticated.checkpoint
	if checkpoint.Target != request.Policy.target || checkpoint.Authority != request.Policy.authority {
		return CheckpointReceipt{}, e.New(e.CodeStatusCheckpoint, "authenticated checkpoint target/authority does not match pinned policy")
	}
	if !request.Policy.authorizes(authenticated.signer, authenticated.issuer) {
		return CheckpointReceipt{}, e.New(e.CodeUntrustedSigner, "authenticated checkpoint signer %q is not currently authorized for status authority %q", authenticated.signer, checkpoint.Authority)
	}
	if request.AsOf.epoch < checkpoint.IssuedEpoch {
		return CheckpointReceipt{}, e.New(e.CodeStatusCheckpoint, "status checkpoint sequence %d was not issued at as-of epoch %d", checkpoint.Sequence, request.AsOf.epoch)
	}
	if request.AsOf.epoch > checkpoint.ValidUntilEpoch {
		return CheckpointReceipt{}, e.New(e.CodeStatusCheckpoint, "status checkpoint sequence %d expired at epoch %d before as-of epoch %d", checkpoint.Sequence, checkpoint.ValidUntilEpoch, request.AsOf.epoch)
	}
	if checkpoint.Sequence < request.HighWater.sequence {
		return CheckpointReceipt{}, e.New(e.CodeStatusCheckpoint, "status checkpoint rollback: candidate sequence %d is below high-water sequence %d", checkpoint.Sequence, request.HighWater.sequence)
	}
	if checkpoint.Sequence == request.HighWater.sequence {
		if authenticated.digest != request.HighWater.checkpoint {
			return CheckpointReceipt{}, e.New(e.CodeStatusCheckpoint, "status checkpoint equivocation at sequence %d: high-water %s, candidate %s", checkpoint.Sequence, request.HighWater.checkpoint, authenticated.digest)
		}
	} else {
		if checkpoint.Sequence != request.HighWater.sequence+1 {
			return CheckpointReceipt{}, e.New(e.CodeStatusCheckpoint, "status checkpoint sequence %d skips high-water sequence %d; every intermediate checkpoint must be authenticated", checkpoint.Sequence, request.HighWater.sequence)
		}
		if checkpoint.PreviousCheckpoint != request.HighWater.checkpoint {
			return CheckpointReceipt{}, e.New(e.CodeStatusCheckpoint, "status checkpoint sequence %d does not descend from high-water checkpoint %s", checkpoint.Sequence, request.HighWater.checkpoint)
		}
	}

	expected := make(map[string]bool, len(checkpoint.DeclarationDigests))
	for _, digest := range checkpoint.DeclarationDigests {
		expected[digest] = true
	}
	byDigest := make(map[string]VerifiedDeclaration, len(request.Declarations))
	for _, documentReceipt := range request.Declarations {
		if err := statusContextErr(ctx); err != nil {
			return CheckpointReceipt{}, err
		}
		declaration, err := DeclarationFromDocumentReceipt(ctx, documentReceipt)
		if err != nil {
			return CheckpointReceipt{}, err
		}
		if !request.Policy.authorizes(declaration.signer, declaration.issuer) {
			return CheckpointReceipt{}, e.New(e.CodeUntrustedSigner, "authenticated status declaration signer %q is not currently authorized for authority %q", declaration.signer, checkpoint.Authority)
		}
		if declaration.declaration.Target != checkpoint.Target {
			return CheckpointReceipt{}, e.New(e.CodeStatusCheckpoint, "checkpoint declaration %s targets %s instead of %s", declaration.digest, declaration.declaration.Target, checkpoint.Target)
		}
		if !expected[declaration.digest] {
			return CheckpointReceipt{}, e.New(e.CodeStatusCheckpoint, "authenticated declaration %s is not named by the checkpoint's complete set", declaration.digest)
		}
		if _, duplicate := byDigest[declaration.digest]; duplicate {
			return CheckpointReceipt{}, e.New(e.CodeStatusCheckpoint, "checkpoint input repeats authenticated declaration %s", declaration.digest)
		}
		byDigest[declaration.digest] = declaration
	}
	accepted := make([]VerifiedDeclaration, 0, len(checkpoint.DeclarationDigests))
	missing := make([]string, 0)
	for _, digest := range checkpoint.DeclarationDigests {
		declaration, found := byDigest[digest]
		if !found {
			missing = append(missing, digest)
			continue
		}
		accepted = append(accepted, declaration)
	}
	if len(missing) != 0 {
		return CheckpointReceipt{}, e.New(e.CodeStatusCheckpoint, "checkpoint's complete declaration set is missing authenticated declaration(s) %s", strings.Join(missing, ", "))
	}
	if err := statusContextErr(ctx); err != nil {
		return CheckpointReceipt{}, err
	}
	return CheckpointReceipt{
		valid: true, checkpoint: cloneCheckpoint(checkpoint), checkpointDigest: authenticated.digest,
		checkpointSigner: authenticated.signer, checkpointIssuer: authenticated.issuer,
		checkpointCertificate:      authenticated.certificateDigest,
		checkpointCanonicalPayload: authenticated.canonicalPayload, policyBinding: request.Policy.binding,
		asOfEpoch: request.AsOf.epoch, declarations: append([]VerifiedDeclaration(nil), accepted...),
	}, nil
}

func (policy CheckpointPolicy) validate() error {
	if !policy.valid || !isDigest(policy.target) || policy.authority == "" || policy.issuer == "" ||
		len(policy.authorizedSigners) == 0 || !isDigest(policy.binding) {
		return e.New(e.CodeTrustPolicyMissing, "status checkpoint policy is absent or invalid")
	}
	return nil
}

type authenticatedCheckpoint struct {
	checkpoint        StatusCheckpoint
	digest            string
	signer            string
	issuer            string
	certificateDigest string
	canonicalPayload  string
}

func checkpointFromDocumentReceipt(ctx context.Context, receipt trust.DocumentReceipt) (authenticatedCheckpoint, error) {
	raw, signer, issuer, certificateDigest, err := authenticatedStatusDocument(ctx, receipt, CheckpointSchemaVersion)
	if err != nil {
		return authenticatedCheckpoint{}, err
	}
	switch decoded := receipt.Decoded().(type) {
	case StatusCheckpoint:
	case *StatusCheckpoint:
		if decoded == nil {
			return authenticatedCheckpoint{}, e.New(e.CodeSignatureInvalid, "authenticated status checkpoint decoded as nil")
		}
	default:
		return authenticatedCheckpoint{}, e.New(e.CodeSignatureInvalid, "authenticated %s document decoded as %T", CheckpointSchemaVersion, receipt.Decoded())
	}
	var checkpoint StatusCheckpoint
	if err := json.Unmarshal(raw, &checkpoint); err != nil {
		return authenticatedCheckpoint{}, e.Wrap(e.CodeSignatureInvalid, err, "rehydrating authenticated status checkpoint bytes")
	}
	canonicalPayload, err := checkpoint.Marshal()
	if err != nil {
		return authenticatedCheckpoint{}, err
	}
	if !bytes.Equal(raw, canonicalPayload) {
		return authenticatedCheckpoint{}, e.New(e.CodeSignatureInvalid, "authenticated checkpoint bytes are not the canonical status-checkpoint representation")
	}
	digest, err := checkpoint.Digest()
	if err != nil {
		return authenticatedCheckpoint{}, err
	}
	return authenticatedCheckpoint{checkpoint: cloneCheckpoint(checkpoint), digest: digest,
		signer: signer, issuer: issuer, certificateDigest: certificateDigest, canonicalPayload: string(canonicalPayload)}, nil
}

// DeclarationFromDocumentReceipt adapts the authenticated trust module's opaque
// RoleStatus receipt into a verified declaration without accepting any caller-
// asserted signature state or signer identity.
func DeclarationFromDocumentReceipt(ctx context.Context, receipt trust.DocumentReceipt) (VerifiedDeclaration, error) {
	raw, signer, issuer, certificateDigest, err := authenticatedStatusDocument(ctx, receipt, SchemaVersion)
	if err != nil {
		return VerifiedDeclaration{}, err
	}
	switch decoded := receipt.Decoded().(type) {
	case StatusDeclaration:
	case *StatusDeclaration:
		if decoded == nil {
			return VerifiedDeclaration{}, e.New(e.CodeSignatureInvalid, "authenticated status declaration decoded as nil")
		}
	default:
		return VerifiedDeclaration{}, e.New(e.CodeSignatureInvalid, "authenticated %s document decoded as %T", SchemaVersion, receipt.Decoded())
	}
	var declaration StatusDeclaration
	if err := json.Unmarshal(raw, &declaration); err != nil {
		return VerifiedDeclaration{}, e.Wrap(e.CodeSignatureInvalid, err, "rehydrating authenticated status declaration bytes")
	}
	canonicalPayload, err := declaration.Marshal()
	if err != nil {
		return VerifiedDeclaration{}, err
	}
	if !bytes.Equal(raw, canonicalPayload) {
		return VerifiedDeclaration{}, e.New(e.CodeSignatureInvalid, "authenticated declaration bytes are not the canonical status-declaration representation")
	}
	digest, err := declaration.Digest()
	if err != nil {
		return VerifiedDeclaration{}, err
	}
	return VerifiedDeclaration{valid: true, documentAuthenticated: true, declaration: declaration,
		signer: signer, issuer: issuer, certificateDigest: certificateDigest, canonicalPayload: string(canonicalPayload), digest: digest}, nil
}

func authenticatedStatusDocument(ctx context.Context, receipt trust.DocumentReceipt, schemaVersion string) ([]byte, string, string, string, error) {
	if err := statusContextErr(ctx); err != nil {
		return nil, "", "", "", err
	}
	if !receipt.Authenticated() || receipt.Role() != trust.RoleStatus {
		return nil, "", "", "", e.New(e.CodeSignatureInvalid, "status input requires an authenticated RoleStatus document receipt")
	}
	if receipt.SchemaVersion() != schemaVersion {
		return nil, "", "", "", e.New(e.CodeSignatureInvalid, "authenticated status document schema %q does not match required %q", receipt.SchemaVersion(), schemaVersion)
	}
	signer := strings.TrimSpace(receipt.SignerIdentity())
	if signer == "" || signer != receipt.SignerIdentity() {
		return nil, "", "", "", e.New(e.CodeSignatureInvalid, "authenticated status document has no canonical signer identity")
	}
	issuer := strings.TrimSpace(receipt.OIDCIssuer())
	if issuer == "" || issuer != receipt.OIDCIssuer() {
		return nil, "", "", "", e.New(e.CodeSignatureInvalid, "authenticated status document has no canonical verified issuer")
	}
	certificateDigest := receipt.CertificateSHA256()
	if !isDigest(certificateDigest) {
		return nil, "", "", "", e.New(e.CodeSignatureInvalid, "authenticated status document has no exact certificate digest")
	}
	raw := receipt.ExactBytes()
	if len(raw) == 0 {
		return nil, "", "", "", e.New(e.CodeSignatureInvalid, "authenticated status document has no exact bytes")
	}
	sum := sha256.Sum256(raw)
	if receipt.SnapshotSHA256() != "sha256:"+hex.EncodeToString(sum[:]) {
		return nil, "", "", "", e.New(e.CodeSignatureInvalid, "authenticated status document snapshot digest does not bind its exact bytes")
	}
	return raw, signer, issuer, certificateDigest, statusContextErr(ctx)
}

func (receipt CheckpointReceipt) validate() error {
	if !receipt.valid || !isDigest(receipt.checkpointDigest) || !isDigest(receipt.policyBinding) || receipt.asOfEpoch <= 0 {
		return e.New(e.CodeStatusCheckpoint, "status checkpoint receipt is absent or invalid")
	}
	if err := receipt.checkpoint.Validate(); err != nil {
		return err
	}
	canonicalPayload, err := receipt.checkpoint.Marshal()
	if err != nil {
		return err
	}
	if string(canonicalPayload) != receipt.checkpointCanonicalPayload {
		return e.New(e.CodeStatusCheckpoint, "status checkpoint receipt payload changed after authentication")
	}
	digest, err := receipt.checkpoint.Digest()
	if err != nil {
		return err
	}
	if digest != receipt.checkpointDigest || receipt.checkpointSigner == "" || receipt.checkpointIssuer == "" || !isDigest(receipt.checkpointCertificate) {
		return e.New(e.CodeStatusCheckpoint, "status checkpoint receipt lost its authenticated identity binding")
	}
	if receipt.asOfEpoch < receipt.checkpoint.IssuedEpoch || receipt.asOfEpoch > receipt.checkpoint.ValidUntilEpoch {
		return e.New(e.CodeStatusCheckpoint, "status checkpoint receipt is not fresh at its recorded as-of epoch")
	}
	if len(receipt.declarations) != len(receipt.checkpoint.DeclarationDigests) {
		return e.New(e.CodeStatusCheckpoint, "status checkpoint receipt lost its complete declaration set")
	}
	for index, declaration := range receipt.declarations {
		if !declaration.documentAuthenticated || !declaration.valid || declaration.digest != receipt.checkpoint.DeclarationDigests[index] {
			return e.New(e.CodeStatusCheckpoint, "status checkpoint receipt contains an unauthenticated or out-of-set declaration")
		}
		canonicalDeclaration, err := declaration.declaration.Marshal()
		if err != nil {
			return err
		}
		if string(canonicalDeclaration) != declaration.canonicalPayload {
			return e.New(e.CodeStatusCheckpoint, "status declaration changed after checkpoint acceptance")
		}
		digest, err := declaration.declaration.Digest()
		if err != nil {
			return err
		}
		if digest != declaration.digest || declaration.declaration.Target != receipt.checkpoint.Target ||
			declaration.signer == "" || declaration.issuer != receipt.checkpointIssuer || !isDigest(declaration.certificateDigest) {
			return e.New(e.CodeStatusCheckpoint, "status declaration lost its authenticated checkpoint binding")
		}
	}
	return nil
}

func (receipt CheckpointReceipt) Target() (string, error) {
	if err := receipt.validate(); err != nil {
		return "", err
	}
	return receipt.checkpoint.Target, nil
}

func (receipt CheckpointReceipt) Authority() (string, error) {
	if err := receipt.validate(); err != nil {
		return "", err
	}
	return receipt.checkpoint.Authority, nil
}

func (receipt CheckpointReceipt) Sequence() (uint64, error) {
	if err := receipt.validate(); err != nil {
		return 0, err
	}
	return receipt.checkpoint.Sequence, nil
}

func (receipt CheckpointReceipt) CheckpointDigest() (string, error) {
	if err := receipt.validate(); err != nil {
		return "", err
	}
	return receipt.checkpointDigest, nil
}

// Decision evaluates only declarations in the authenticated complete checkpoint
// set at the exact as-of epoch recorded by the receipt.
func (receipt CheckpointReceipt) Decision() (*Decision, error) {
	if err := receipt.validate(); err != nil {
		return nil, err
	}
	return decisionFromAccepted(receipt.checkpoint.Target, receipt.declarations,
		AsOf{set: true, epoch: receipt.asOfEpoch})
}

func cloneCheckpoint(checkpoint StatusCheckpoint) StatusCheckpoint {
	clone := checkpoint
	clone.DeclarationDigests = make([]string, len(checkpoint.DeclarationDigests))
	copy(clone.DeclarationDigests, checkpoint.DeclarationDigests)
	return clone
}
