// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package trust

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestAuthenticationRejectsMalformedJSONAfterSignatureVerification(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"scalar", "42"},
		{"array", "[]"},
		{"missing schema", `{"name":"test"}`},
		{"null schema", `{"name":"test","schemaVersion":null}`},
		{"empty schema", `{"name":"test","schemaVersion":" "}`},
		{"trailing document", `{"name":"test","schemaVersion":"nvkr.test/document/v1"} {}`},
		{"trailing garbage", `{"name":"test","schemaVersion":"nvkr.test/document/v1"} !`},
		{"truncated array", `{"items":[`},
		{"truncated object", `{"name":"test"`},
		{"missing value", `{"name":}`},
		{"wrong typed item", `{"items":[1],"name":"test","schemaVersion":"nvkr.test/document/v1"}`},
		{"nested duplicate", `{"items":[{"name":"a","name":"b"}],"name":"test","schemaVersion":"nvkr.test/document/v1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			auth := testAuthenticator(t, documentVerifierFunc(func(context.Context, DocumentVerificationRequest) (VerificationEvidence, error) {
				calls++
				return goodEvidence(), nil
			}))
			receipt, err := auth.Authenticate(t.Context(), AuthenticateRequest{SourcePath: writeTestDocument(t, []byte(tc.raw)), Role: RoleValidationVector, Policy: testPolicy(t, RoleValidationVector)})
			if code(err) != e.CodeVerifyFailed || receipt.Authenticated() {
				t.Fatalf("malformed JSON authenticated: %+v, %v", receipt, err)
			}
			if calls != 1 {
				t.Fatalf("signature verification calls = %d", calls)
			}
		})
	}
}

func TestDocumentReceiptPreservesVerifiedTransportBindings(t *testing.T) {
	raw := canonicalTestDocument(t, "bound")
	evidence := goodEvidence()
	evidence.SignatureSHA256 = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("signature")))
	evidence.CertificateBlobSHA256 = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("certificate")))
	evidence.BundleSHA256 = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("bundle")))
	auth := testAuthenticator(t, documentVerifierFunc(func(context.Context, DocumentVerificationRequest) (VerificationEvidence, error) { return evidence, nil }))
	policy := testPolicy(t, RoleValidationVector)
	receipt, err := auth.Authenticate(t.Context(), AuthenticateRequest{SourcePath: writeTestDocument(t, raw), Role: RoleValidationVector, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SchemaVersion() != testSchema || receipt.OIDCIssuer() != testIssuer || receipt.CertificateSHA256() != evidence.CertificateSHA256 || receipt.SignatureSHA256() != evidence.SignatureSHA256 || receipt.CertificateBlobSHA256() != evidence.CertificateBlobSHA256 || receipt.BundleSHA256() != evidence.BundleSHA256 || receipt.SnapshotSHA256() != fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) || receipt.Role() != policy.Role() {
		t.Fatal("receipt lost authenticated schema, subject or transport identity")
	}
}
