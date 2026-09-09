package release

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/trust"
)

func authenticatedPromotionDocument(t *testing.T, policy PromotionPolicy,
	schemaVersion string, role trust.SignerRole,
) trust.DocumentReceipt {
	t.Helper()
	raw, err := canonical.Marshal(policy.normalized())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "promotion-policy.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	const signer = "promotion-policy-signer"
	const issuer = "https://promotion-policy-issuer.example.test"
	authenticator, err := trust.NewDocumentAuthenticator(
		statusDocumentVerifier{signer: signer, issuer: issuer},
		trust.JSONSchema[PromotionPolicy](schemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	rolePolicy, err := trust.NewRolePolicy(role,
		trust.Store{OperatorIdentities: []string{signer}}, issuer)
	if err != nil {
		t.Fatal(err)
	}
	document, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: role, Policy: rolePolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func authenticatedReleaseSetDocument(t *testing.T, raw []byte, role trust.SignerRole) trust.DocumentReceipt {
	t.Helper()
	path := filepath.Join(t.TempDir(), "release-set.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	const signer = "release-set-signer"
	const issuer = "https://release-set-issuer.example.test"
	authenticator, err := trust.NewDocumentAuthenticator(
		statusDocumentVerifier{signer: signer, issuer: issuer},
		trust.JSONSchema[ReleaseSet](ReleaseSetSchemaVersion),
	)
	if err != nil {
		t.Fatal(err)
	}
	rolePolicy, err := trust.NewRolePolicy(role,
		trust.Store{OperatorIdentities: []string{signer}}, issuer)
	if err != nil {
		t.Fatal(err)
	}
	document, err := authenticator.Authenticate(context.Background(), trust.AuthenticateRequest{
		SourcePath: path, Role: role, Policy: rolePolicy,
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func TestPromotionPolicyCapabilityIsRoleSeparatedAndMutationSafe(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	req := authorizationRequest(t, inv, set)

	without := req
	without.PromotionPolicy = PromotionPolicyReceipt{}
	if _, err := Authorize(context.Background(), without); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("zero promotion-policy capability must fail closed: %v", err)
	}

	mutated := req
	mutated.PromotionPolicy.policy.Rules[0].Evidence[0].Type = EvidenceSBOM
	if _, err := Authorize(context.Background(), mutated); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("post-authentication promotion-policy mutation must fail: %v", err)
	}

	policy := PromotionPolicy{
		SchemaVersion:             PromotionPolicySchemaVersion,
		CompatibilityPolicyDigest: digest('8'), ConformancePolicyDigest: digest('9'),
		Rules:          []PromotionRule{vulnerabilityPromotionRule(ArtifactHostPackage)},
		EffectiveEpoch: 1, ExpiresEpoch: 20,
	}
	wrongRole := authenticatedPromotionDocument(t, policy, PromotionPolicySchemaVersion, trust.RoleConformance)
	if _, err := VerifyPromotionPolicy(context.Background(), wrongRole); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("conformance-authenticated document must not become a promotion-policy capability: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	properRole := authenticatedPromotionDocument(t, policy, PromotionPolicySchemaVersion, trust.RolePromotionPolicy)
	if _, err := VerifyPromotionPolicy(ctx, properRole); code(err) != nkderr.CodeTimeout {
		t.Fatalf("promotion-policy verification must honor cancellation: %v", err)
	}
}

func TestAuthorizeDerivesEvidenceAndGatePoliciesFromAuthenticatedPolicy(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	req := authorizationRequest(t, inv, set)

	missing := req
	missing.VulnerabilityReports = nil
	if _, err := Authorize(context.Background(), missing); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("policy-required vulnerability reports must be enforced: %v", err)
	}

	noEvidence := req
	noEvidence.PromotionPolicy = verifiedPromotionPolicy(t,
		req.PromotionPolicy.policy.CompatibilityPolicyDigest,
		req.PromotionPolicy.policy.ConformancePolicyDigest,
		[]PromotionRule{{ArtifactKind: ArtifactHostPackage}},
	)
	noEvidence.VulnerabilityReports = nil
	if _, err := Authorize(context.Background(), noEvidence); err != nil {
		t.Fatalf("an explicit authenticated no-evidence rule must not inherit hard-coded scanner requirements: %v", err)
	}

	extra := noEvidence
	extra.VulnerabilityReports = req.VulnerabilityReports
	if _, err := Authorize(context.Background(), extra); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("evidence not required by the authenticated policy must be rejected as substitution: %v", err)
	}

	missingKind := req
	missingKind.PromotionPolicy = verifiedPromotionPolicy(t,
		req.PromotionPolicy.policy.CompatibilityPolicyDigest,
		req.PromotionPolicy.policy.ConformancePolicyDigest,
		[]PromotionRule{vulnerabilityPromotionRule(ArtifactOCIImage)},
	)
	if _, err := Authorize(context.Background(), missingKind); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("policy without the release artifact kind must fail closed: %v", err)
	}

	wrongCompatibility := req
	wrongCompatibility.PromotionPolicy = verifiedPromotionPolicy(t, digest('a'),
		req.PromotionPolicy.policy.ConformancePolicyDigest,
		[]PromotionRule{vulnerabilityPromotionRule(ArtifactHostPackage)},
	)
	if _, err := Authorize(context.Background(), wrongCompatibility); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("compatibility policy must be derived from the authenticated promotion policy: %v", err)
	}
}

func TestPromotionPolicyRejectsUnknownSchemaDuplicateRulesAndExpiry(t *testing.T) {
	base := PromotionPolicy{
		SchemaVersion:             PromotionPolicySchemaVersion,
		CompatibilityPolicyDigest: digest('8'), ConformancePolicyDigest: digest('9'),
		Rules:          []PromotionRule{vulnerabilityPromotionRule(ArtifactHostPackage)},
		EffectiveEpoch: 1, ExpiresEpoch: 20,
	}

	unknown := base
	unknown.SchemaVersion = "nkd.dev/promotion-policy/v99"
	document := authenticatedPromotionDocument(t, unknown, unknown.SchemaVersion, trust.RolePromotionPolicy)
	if _, err := VerifyPromotionPolicy(context.Background(), document); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("unknown authenticated promotion-policy schema must fail closed: %v", err)
	}

	duplicate := base
	duplicate.Rules = append(duplicate.Rules, vulnerabilityPromotionRule(ArtifactHostPackage))
	if err := duplicate.Validate(); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("duplicate artifact-kind rule must fail closed: %v", err)
	}

	inv := inventory()
	set := releaseSet(t, inv)
	req := authorizationRequest(t, inv, set)
	expired := base
	expired.ExpiresEpoch = 5
	receipt, err := VerifyPromotionPolicy(context.Background(),
		authenticatedPromotionDocument(t, expired, PromotionPolicySchemaVersion, trust.RolePromotionPolicy))
	if err != nil {
		t.Fatal(err)
	}
	req.PromotionPolicy = receipt
	if _, err := Authorize(context.Background(), req); code(err) != nkderr.CodeGateUnmet {
		t.Fatalf("expired promotion policy must fail closed: %v", err)
	}
}

func TestInventoryArtifactKindsAreExactAndClosed(t *testing.T) {
	inv := inventory()
	inv.Components[0].ArtifactKinds = nil
	if err := inv.Validate(); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("unclassified required artifact must fail closed: %v", err)
	}

	inv = inventory()
	inv.Components[0].ArtifactKinds[0].Kind = ArtifactKind(255)
	if err := inv.Validate(); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("unknown artifact kind must fail closed: %v", err)
	}

	inv = inventory()
	inv.Components[0].ArtifactKinds = append(inv.Components[0].ArtifactKinds,
		inv.Components[0].ArtifactKinds[0])
	if err := inv.Validate(); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("duplicate artifact classification must fail closed: %v", err)
	}
}

func TestReleaseSetCapabilityRequiresExactAuthenticatedBytesAndRole(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	raw, err := set.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	wrongRole := authenticatedReleaseSetDocument(t, raw, trust.RolePromotionPolicy)
	if _, err := VerifyAuthenticatedReleaseSet(context.Background(), wrongRole); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("promotion-policy role must not authenticate a release set: %v", err)
	}

	// Syntactically canonical JSON with a noncanonical member order is still a
	// different signed byte string and must not be normalized after authentication.
	unsorted, err := canonical.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	if string(unsorted) == string(raw) {
		t.Fatal("fixture unexpectedly uses canonical release member order")
	}
	unsortedDocument := authenticatedReleaseSetDocument(t, unsorted, trust.RoleReleaseSet)
	if _, err := VerifyAuthenticatedReleaseSet(context.Background(), unsortedDocument); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("authenticated but semantically noncanonical member ordering must fail: %v", err)
	}

	req := authorizationRequest(t, inv, set)
	mutated := req
	mutated.Release.set.Members[0].ArtifactDigests[0] = digest('9')
	if _, err := Authorize(context.Background(), mutated); code(err) != nkderr.CodeUnverifiedRef {
		t.Fatalf("post-authentication release-set mutation must fail exact identity: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	proper := authenticatedReleaseSetDocument(t, raw, trust.RoleReleaseSet)
	if _, err := VerifyAuthenticatedReleaseSet(ctx, proper); code(err) != nkderr.CodeTimeout {
		t.Fatalf("release-set narrowing must honor cancellation: %v", err)
	}
}
