package release

import (
	"context"
	"reflect"
	"sort"
	"strings"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/trust"
)

const (
	ReproductionSchemaVersion  = "nkd.dev/reproduction-result/v1"
	CompatibilitySchemaVersion = "nkd.dev/compatibility-result/v1"
)

// ReceiptRole is a closed trust-policy role passed to the authentication seam.
type ReceiptRole uint8

const (
	RoleReproduction ReceiptRole = iota + 1
	RoleCompatibility
	RoleConformance
)

func (role ReceiptRole) String() string {
	switch role {
	case RoleReproduction:
		return "release-reproduction"
	case RoleCompatibility:
		return "release-compatibility"
	case RoleConformance:
		return "release-conformance"
	default:
		return ""
	}
}

// TrustRole bridges the release-gate receipt role onto the single trust.SignerRole
// vocabulary so the reproduction/compatibility/conformance gates and the exact-byte
// trust documents authenticate under one role system rather than two parallel enums
// (the "Conformance in both enums with no bridge" incoherence). A future production
// ReceiptAuthenticator should authenticate under TrustRole() rather than reintroduce a
// second role space.
func (role ReceiptRole) TrustRole() trust.SignerRole {
	switch role {
	case RoleReproduction:
		return trust.RoleReproduction
	case RoleCompatibility:
		return trust.RoleCompatibility
	case RoleConformance:
		return trust.RoleConformance
	default:
		return ""
	}
}

// ReceiptAuthenticator authenticates canonical bytes and enforces requiredRole.
// A successful return is both signature and role authorization, not an assertion
// supplied by a deserialized receipt.
type ReceiptAuthenticator interface {
	AuthenticateReceipt(ctx context.Context, canonicalPayload, signature []byte, requiredRole ReceiptRole) (verifiedSigner string, err error)
}

// ReproductionResult binds the full exact member tuple required by ADR-16.
type ReproductionResult struct {
	SchemaVersion       string   `json:"schemaVersion"`
	ReleaseSetDigest    string   `json:"releaseSetDigest"`
	Component           string   `json:"component"`
	Role                string   `json:"role,omitempty"`
	Arch                string   `json:"arch"`
	RecipeDigest        string   `json:"recipeDigest"`
	VectorDigest        string   `json:"vectorDigest"`
	ProfileDigest       string   `json:"profileDigest,omitempty"`
	ProfileIndependent  bool     `json:"profileIndependent,omitempty"`
	ResolvedInputDigest string   `json:"resolvedInputDigest"`
	ArtifactDigests     []string `json:"artifactDigests"`
	Verdict             string   `json:"verdict"`
	EffectiveEpoch      int64    `json:"effectiveEpoch"`
	ExpiresEpoch        int64    `json:"expiresEpoch"`
}

func (result ReproductionResult) normalized() ReproductionResult {
	result.ArtifactDigests = sortedStrings(result.ArtifactDigests)
	return result
}

func (result ReproductionResult) Validate() error {
	if result.SchemaVersion != ReproductionSchemaVersion {
		return e.New(e.CodeInvalidCatalog, "reproduction schemaVersion %q is unsupported (want %q)", result.SchemaVersion, ReproductionSchemaVersion)
	}
	if !isDigest(result.ReleaseSetDigest) || !isDigest(result.RecipeDigest) || !isDigest(result.VectorDigest) || !isDigest(result.ResolvedInputDigest) {
		return e.New(e.CodeUnverifiedRef, "reproduction result must bind exact release-set, recipe, vector, and resolved-input digests")
	}
	if strings.TrimSpace(result.Component) == "" || strings.TrimSpace(result.Arch) == "" {
		return e.New(e.CodeInvalidCatalog, "reproduction result requires component and architecture")
	}
	if result.ProfileIndependent == (result.ProfileDigest != "") || (result.ProfileDigest != "" && !isDigest(result.ProfileDigest)) {
		return e.New(e.CodeUnverifiedRef, "reproduction result must bind exactly one builder profile or explicit profile independence")
	}
	if result.Verdict != Reproduced && result.Verdict != ReproductionFailed {
		return e.New(e.CodeGateUnmet, "reproduction result has unknown verdict %q", orNone(result.Verdict))
	}
	if err := validateExactDigestSet("reproduction artifact", result.ArtifactDigests, false); err != nil {
		return err
	}
	if result.EffectiveEpoch <= 0 || result.ExpiresEpoch <= result.EffectiveEpoch {
		return e.New(e.CodeGateUnmet, "reproduction receipt requires positive effective and later expiry epochs")
	}
	return nil
}

type ReproductionReceipt struct {
	valid            bool
	result           ReproductionResult
	canonicalPayload string
	digest           string
	verifiedSigner   string
}

func (receipt ReproductionReceipt) Digest() (string, error) {
	if !receipt.valid {
		return "", e.New(e.CodeSignatureInvalid, "reproduction receipt is not authenticated")
	}
	return receipt.digest, nil
}

func VerifyReproduction(ctx context.Context, result ReproductionResult, signature []byte, release ReleaseSetReceipt, authenticator ReceiptAuthenticator) (ReproductionReceipt, error) {
	if err := contextErr(ctx); err != nil {
		return ReproductionReceipt{}, err
	}
	if !release.valid {
		return ReproductionReceipt{}, e.New(e.CodeUnverifiedRef, "reproduction verification requires a verified release-set receipt")
	}
	if authenticator == nil || len(signature) == 0 {
		return ReproductionReceipt{}, e.New(e.CodeSignatureInvalid, "reproduction result requires signature authentication for role %s", RoleReproduction.String())
	}
	result = result.normalized()
	if err := result.Validate(); err != nil {
		return ReproductionReceipt{}, err
	}
	if result.ReleaseSetDigest != release.digest {
		return ReproductionReceipt{}, e.New(e.CodeGateUnmet, "reproduction result is bound to release set %s, not %s", result.ReleaseSetDigest, release.digest)
	}
	exact := false
	for _, member := range release.set.Members {
		if reproductionMatchesMember(result, member) {
			exact = true
			break
		}
	}
	if !exact {
		return ReproductionReceipt{}, e.New(e.CodeGateUnmet, "reproduction result does not bind an exact release member tuple")
	}
	payload, err := canonical.Marshal(result)
	if err != nil {
		return ReproductionReceipt{}, err
	}
	signer, err := authenticator.AuthenticateReceipt(ctx, append([]byte(nil), payload...), append([]byte(nil), signature...), RoleReproduction)
	if ctxErr := contextErr(ctx); ctxErr != nil {
		return ReproductionReceipt{}, ctxErr
	}
	if err != nil {
		return ReproductionReceipt{}, e.Wrap(e.CodeSignatureInvalid, err, "authenticating reproduction receipt")
	}
	signer = strings.TrimSpace(signer)
	if signer == "" {
		return ReproductionReceipt{}, e.New(e.CodeSignatureInvalid, "reproduction authenticator returned no signer identity")
	}
	digest, err := canonical.Digest(canonical.KindReproductionResult, result)
	if err != nil {
		return ReproductionReceipt{}, err
	}
	return ReproductionReceipt{valid: true, result: result, canonicalPayload: string(payload), digest: digest, verifiedSigner: signer}, nil
}

const (
	CompatibilityAccepted = "accepted"
	CompatibilityRejected = "rejected"
)

// CompatibilityResult is the signed compatibility-policy evaluation over the
// exhaustive release identities.
type CompatibilityResult struct {
	SchemaVersion             string              `json:"schemaVersion"`
	ReleaseSetDigest          string              `json:"releaseSetDigest"`
	InventoryDigest           string              `json:"inventoryDigest"`
	CompatibilityPolicyDigest string              `json:"compatibilityPolicyDigest"`
	Target                    CompatibilityTarget `json:"target"`
	RecipeDigests             []string            `json:"recipeDigests"`
	VectorDigests             []string            `json:"vectorDigests"`
	ArtifactDigests           []string            `json:"artifactDigests"`
	Verdict                   string              `json:"verdict"`
	EffectiveEpoch            int64               `json:"effectiveEpoch"`
	ExpiresEpoch              int64               `json:"expiresEpoch"`
}

func (result CompatibilityResult) normalized() CompatibilityResult {
	result.RecipeDigests = sortedStrings(result.RecipeDigests)
	result.VectorDigests = sortedStrings(result.VectorDigests)
	result.ArtifactDigests = sortedStrings(result.ArtifactDigests)
	return result
}

func (result CompatibilityResult) Validate() error {
	if result.SchemaVersion != CompatibilitySchemaVersion {
		return e.New(e.CodeInvalidCatalog, "compatibility schemaVersion %q is unsupported (want %q)", result.SchemaVersion, CompatibilitySchemaVersion)
	}
	if !isDigest(result.ReleaseSetDigest) || !isDigest(result.InventoryDigest) || !isDigest(result.CompatibilityPolicyDigest) {
		return e.New(e.CodeUnverifiedRef, "compatibility result must bind exact release-set, inventory, and policy digests")
	}
	if err := validateExactDigestSet("compatibility recipe", result.RecipeDigests, false); err != nil {
		return err
	}
	if err := validateExactDigestSet("compatibility vector", result.VectorDigests, false); err != nil {
		return err
	}
	if err := validateExactDigestSet("compatibility artifact", result.ArtifactDigests, false); err != nil {
		return err
	}
	if result.Verdict != CompatibilityAccepted && result.Verdict != CompatibilityRejected {
		return e.New(e.CodeGateUnmet, "compatibility result has unknown verdict %q", result.Verdict)
	}
	if result.EffectiveEpoch <= 0 || result.ExpiresEpoch <= result.EffectiveEpoch {
		return e.New(e.CodeGateUnmet, "compatibility receipt requires positive effective and later expiry epochs")
	}
	return nil
}

type CompatibilityReceipt struct {
	valid            bool
	result           CompatibilityResult
	canonicalPayload string
	digest           string
	verifiedSigner   string
}

func (receipt CompatibilityReceipt) Digest() (string, error) {
	if !receipt.valid {
		return "", e.New(e.CodeSignatureInvalid, "compatibility receipt is not authenticated")
	}
	return receipt.digest, nil
}

func VerifyCompatibility(ctx context.Context, result CompatibilityResult, signature []byte, release ReleaseSetReceipt, inventory *Inventory, authenticator ReceiptAuthenticator) (CompatibilityReceipt, error) {
	if err := contextErr(ctx); err != nil {
		return CompatibilityReceipt{}, err
	}
	if !release.valid {
		return CompatibilityReceipt{}, e.New(e.CodeUnverifiedRef, "compatibility verification requires a verified release-set receipt")
	}
	if authenticator == nil || len(signature) == 0 {
		return CompatibilityReceipt{}, e.New(e.CodeSignatureInvalid, "compatibility result requires signature authentication for role %s", RoleCompatibility.String())
	}
	result = result.normalized()
	if err := result.Validate(); err != nil {
		return CompatibilityReceipt{}, err
	}
	invDigest, err := inventory.Digest()
	if err != nil {
		return CompatibilityReceipt{}, err
	}
	if result.ReleaseSetDigest != release.digest || result.InventoryDigest != invDigest || release.set.InventoryRef != invDigest {
		return CompatibilityReceipt{}, e.New(e.CodeGateUnmet, "compatibility result is not bound to the exact release set and inventory")
	}
	recipes, vectors, artifacts := releaseIdentitySets(release.set.Members)
	if !reflect.DeepEqual(result.RecipeDigests, recipes) || !reflect.DeepEqual(result.VectorDigests, vectors) || !reflect.DeepEqual(result.ArtifactDigests, artifacts) {
		return CompatibilityReceipt{}, e.New(e.CodeGateUnmet, "compatibility result does not exhaustively bind release recipes, vectors, and artifacts")
	}
	if result.Verdict == CompatibilityAccepted {
		if err := CheckCompatibility(inventory, result.Target); err != nil {
			return CompatibilityReceipt{}, err
		}
		if err := checkMemberCompatibility(inventory, &release.set); err != nil {
			return CompatibilityReceipt{}, err
		}
	}
	payload, err := canonical.Marshal(result)
	if err != nil {
		return CompatibilityReceipt{}, err
	}
	signer, err := authenticator.AuthenticateReceipt(ctx, append([]byte(nil), payload...), append([]byte(nil), signature...), RoleCompatibility)
	if ctxErr := contextErr(ctx); ctxErr != nil {
		return CompatibilityReceipt{}, ctxErr
	}
	if err != nil {
		return CompatibilityReceipt{}, e.Wrap(e.CodeSignatureInvalid, err, "authenticating compatibility receipt")
	}
	signer = strings.TrimSpace(signer)
	if signer == "" {
		return CompatibilityReceipt{}, e.New(e.CodeSignatureInvalid, "compatibility authenticator returned no signer identity")
	}
	digest, err := canonical.Digest(canonical.KindCompatibilityResult, result)
	if err != nil {
		return CompatibilityReceipt{}, err
	}
	return CompatibilityReceipt{valid: true, result: result, canonicalPayload: string(payload), digest: digest, verifiedSigner: signer}, nil
}

func reproductionMatchesMember(result ReproductionResult, member Member) bool {
	profileMatches := (member.ProfileDigest == "" && result.ProfileIndependent && result.ProfileDigest == "") || (member.ProfileDigest != "" && !result.ProfileIndependent && result.ProfileDigest == member.ProfileDigest)
	return result.Component == member.Component && result.Role == member.Role && result.Arch == member.Arch && result.RecipeDigest == member.RecipeDigest && result.VectorDigest == member.VectorDigest && result.ResolvedInputDigest == member.ResolvedInputDigest && profileMatches && reflect.DeepEqual(result.ArtifactDigests, sortedStrings(member.ArtifactDigests))
}

func releaseIdentitySets(members []Member) (recipes, vectors, artifacts []string) {
	recipeSet, vectorSet, artifactSet := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, member := range members {
		recipeSet[member.RecipeDigest] = true
		vectorSet[member.VectorDigest] = true
		for _, artifact := range member.ArtifactDigests {
			artifactSet[artifact] = true
		}
	}
	return sortedKeys(recipeSet), sortedKeys(vectorSet), sortedKeys(artifactSet)
}

func validateExactDigestSet(name string, values []string, allowEmpty bool) error {
	if !allowEmpty && len(values) == 0 {
		return e.New(e.CodeGateUnmet, "%s set is empty", name)
	}
	seen := map[string]bool{}
	for _, value := range values {
		if !isDigest(value) {
			return e.New(e.CodeUnverifiedRef, "%s identity %q is not an exact digest", name, value)
		}
		if seen[value] {
			return e.New(e.CodeGateUnmet, "%s set repeats %s", name, value)
		}
		seen[value] = true
	}
	return nil
}

func sortedStrings(values []string) []string {
	copy := append([]string(nil), values...)
	sort.Strings(copy)
	return copy
}

func receiptCurrent(effectiveEpoch, expiresEpoch, asOfEpoch int64, kind string) error {
	if asOfEpoch <= 0 || asOfEpoch < effectiveEpoch || asOfEpoch > expiresEpoch {
		return e.New(e.CodeGateUnmet, "%s receipt is not valid at as-of epoch %d", kind, asOfEpoch)
	}
	return nil
}

func verifyStoredReceipt(document any, canonicalPayload, digest, kind string) error {
	payload, err := canonical.Marshal(document)
	if err != nil {
		return err
	}
	if string(payload) != canonicalPayload {
		return e.New(e.CodeSignatureInvalid, "authenticated receipt payload changed after verification")
	}
	recomputed, err := canonical.Digest(kind, document)
	if err != nil {
		return err
	}
	if recomputed != digest {
		return e.New(e.CodeSignatureInvalid, "authenticated receipt digest changed after verification")
	}
	return nil
}
