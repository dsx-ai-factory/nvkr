// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"fmt"
	"os"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestReleaseManifestAttestationUsesSeparateTypeAndVerifiesExactBytes(t *testing.T) {
	const predicate = `{"schemaVersion":"nvkr.dev/release-manifest/v1"}`
	for _, tc := range []struct {
		name, failure string
		code          e.Code
	}{
		{name: "verified"},
		{name: "attaching fails", failure: "attest", code: e.CodeSignatureInvalid},
		{name: "crypto fails", failure: "verify-blob-attestation", code: e.CodeSignatureInvalid},
		{name: "substituted manifest", failure: "predicate", code: e.CodeVerifyFailed},
		{name: "qualification substituted", failure: "qualification-type", code: e.CodeVerifyFailed},
		{name: "SLSA substituted", failure: "slsa-type", code: e.CodeVerifyFailed},
		{name: "wrong subject", failure: "subject", code: e.CodeVerifyFailed},
		{name: "builder identity", failure: "identity", code: e.CodeUntrustedSigner},
		{name: "wrong issuer", failure: "issuer", code: e.CodeUntrustedSigner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest, typ, identity, issuer, document := ociTestDigest, ReleaseManifestPredicateType, ociObservedIdentity, ociTestIssuer, predicate
			switch tc.failure {
			case "predicate":
				document = `{"schemaVersion":"nvkr.dev/release-manifest/v1","forged":true}`
			case "qualification-type":
				typ = QualificationPredicateType
			case "slsa-type":
				typ = ProvenancePredicateType
			case "subject":
				digest = "sha256:" + strings.Repeat("b", 64)
			case "identity":
				identity = "https://github.com/other/build.yml@refs/heads/main"
			case "issuer":
				issuer = "https://wrong.example"
			}
			stub := &cosignStub{stdout: map[string]string{"download": qualificationBundle(t, digest, typ, identity, issuer, []byte(document))}}
			stub.onCall = func(call recordedCall) error {
				if call.args[0] == tc.failure {
					return fmt.Errorf("injected failure")
				}
				switch call.args[0] {
				case "attest":
					if strings.Join(call.args[:6], " ") != "attest --yes --new-bundle-format --type "+ReleaseManifestPredicateType+" --predicate" || call.args[len(call.args)-1] != ociTestRef {
						t.Fatalf("bad release-manifest argv: %v", call.args)
					}
					raw, err := os.ReadFile(call.args[6])
					if err != nil {
						return err
					}
					if string(raw) != predicate {
						t.Fatal("changed predicate")
					}
				case "download":
					if call.args[3] != ReleaseManifestPredicateType {
						t.Fatal("release manifest downloaded another evidence type")
					}
				case "verify-blob-attestation":
					if call.args[4] != ReleaseManifestPredicateType {
						t.Fatal("release manifest verified another evidence type")
					}
				case "sign":
					t.Fatal("release manifest unexpectedly signed the subject")
				}
				return nil
			}
			err := newStubbedSigner(t, stub, Credential{}).AttestReleaseManifest(t.Context(), ociSubject(), []byte(predicate))
			if tc.code != "" {
				requireSignCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(stub.calls) != 3 || stub.calls[0].args[0] != "attest" || stub.calls[1].args[0] != "download" || stub.calls[2].args[0] != "verify-blob-attestation" {
				t.Fatalf("calls=%+v", stub.calls)
			}
		})
	}
}

func TestReleaseManifestVerificationRequiresExpectedBytes(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`{broken`)} {
		t.Run(string(raw), func(t *testing.T) {
			stub := &cosignStub{}
			err := newStubbedSigner(t, stub, Credential{}).VerifyReleaseManifest(t.Context(), ociSubject(), raw)
			if err == nil || len(stub.calls) != 0 {
				t.Fatalf("invalid manifest reached subprocess: %v", err)
			}
		})
	}
}

func TestReleaseManifestVerificationAuthenticatesExactBytes(t *testing.T) {
	const predicate = `{"schemaVersion":"nvkr.dev/release-manifest/v1"}`
	stub := &cosignStub{stdout: map[string]string{"download": qualificationBundle(t, ociTestDigest, ReleaseManifestPredicateType, ociObservedIdentity, ociTestIssuer, []byte(predicate))}}
	if err := newStubbedSigner(t, stub, Credential{}).VerifyReleaseManifest(t.Context(), ociSubject(), []byte(predicate)); err != nil {
		t.Fatal(err)
	}
	for _, call := range stub.calls {
		if call.args[0] == "attest" || call.args[0] == "sign" {
			t.Fatalf("verification used signing command %v", call.args)
		}
	}
}
