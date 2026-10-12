// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
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

func TestPromotionPolicyAuthenticationRequiresExactRoleAndContext(t *testing.T) {
	policy := PromotionPolicy{
		SchemaVersion:             PromotionPolicySchemaVersion,
		CompatibilityPolicyDigest: digest('8'), ConformancePolicyDigest: digest('9'),
		Rules:          []PromotionRule{{ArtifactKind: ArtifactHostPackage, Evidence: []EvidenceRequirement{{Type: EvidenceVulnerability}}}},
		EffectiveEpoch: 1, ExpiresEpoch: 20,
	}
	wrongRole := authenticatedPromotionDocument(t, policy, PromotionPolicySchemaVersion, trust.RoleConformance)
	if _, err := VerifyPromotionPolicy(context.Background(), wrongRole); code(err) != nvkrerr.CodeSignatureInvalid {
		t.Fatalf("conformance-authenticated document must not authenticate promotion policy: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	properRole := authenticatedPromotionDocument(t, policy, PromotionPolicySchemaVersion, trust.RolePromotionPolicy)
	wantDigest, err := policy.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if gotDigest, err := VerifyPromotionPolicy(context.Background(), properRole); err != nil || gotDigest != wantDigest {
		t.Fatalf("exact promotion-policy identity = %q, %v; want %q", gotDigest, err, wantDigest)
	}
	if _, err := VerifyPromotionPolicy(ctx, properRole); code(err) != nvkrerr.CodeTimeout {
		t.Fatalf("promotion-policy verification must honor cancellation: %v", err)
	}
}

func TestPromotionPolicyRejectsUnknownSchemaAndDuplicateRules(t *testing.T) {
	base := PromotionPolicy{
		SchemaVersion:             PromotionPolicySchemaVersion,
		CompatibilityPolicyDigest: digest('8'), ConformancePolicyDigest: digest('9'),
		Rules:          []PromotionRule{{ArtifactKind: ArtifactHostPackage, Evidence: []EvidenceRequirement{{Type: EvidenceVulnerability}}}},
		EffectiveEpoch: 1, ExpiresEpoch: 20,
	}

	unknown := base
	unknown.SchemaVersion = "nvkr.dev/promotion-policy/v99"
	document := authenticatedPromotionDocument(t, unknown, unknown.SchemaVersion, trust.RolePromotionPolicy)
	if _, err := VerifyPromotionPolicy(context.Background(), document); code(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("unknown authenticated promotion-policy schema must fail closed: %v", err)
	}

	duplicate := base
	duplicate.Rules = append(duplicate.Rules, PromotionRule{ArtifactKind: ArtifactHostPackage, Evidence: []EvidenceRequirement{{Type: EvidenceVulnerability}}})
	if err := duplicate.Validate(); code(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("duplicate artifact-kind rule must fail closed: %v", err)
	}

}

func TestInventoryArtifactKindsAreExactAndClosed(t *testing.T) {
	inv := inventory()
	inv.Components[0].ArtifactKinds = nil
	if err := inv.Validate(); code(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("unclassified required artifact must fail closed: %v", err)
	}

	inv = inventory()
	inv.Components[0].ArtifactKinds[0].Kind = ArtifactKind(255)
	if err := inv.Validate(); code(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("unknown artifact kind must fail closed: %v", err)
	}

	inv = inventory()
	inv.Components[0].ArtifactKinds = append(inv.Components[0].ArtifactKinds,
		inv.Components[0].ArtifactKinds[0])
	if err := inv.Validate(); code(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("duplicate artifact classification must fail closed: %v", err)
	}
}

func TestReleaseSetAuthenticationRequiresExactBytesRoleAndContext(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	raw, err := set.Marshal()
	if err != nil {
		t.Fatal(err)
	}

	wrongRole := authenticatedReleaseSetDocument(t, raw, trust.RolePromotionPolicy)
	if err := VerifyAuthenticatedReleaseSet(context.Background(), wrongRole); code(err) != nvkrerr.CodeSignatureInvalid {
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
	if err := VerifyAuthenticatedReleaseSet(context.Background(), unsortedDocument); code(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("authenticated but semantically noncanonical member ordering must fail: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	proper := authenticatedReleaseSetDocument(t, raw, trust.RoleReleaseSet)
	if err := VerifyAuthenticatedReleaseSet(context.Background(), proper); err != nil {
		t.Fatalf("exact release-set document rejected: %v", err)
	}
	if err := VerifyAuthenticatedReleaseSet(ctx, proper); code(err) != nvkrerr.CodeTimeout {
		t.Fatalf("release-set verification must honor cancellation: %v", err)
	}
}
