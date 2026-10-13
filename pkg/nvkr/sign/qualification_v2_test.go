// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func qualificationBundleV2(t *testing.T, digest, predicateType, identity, issuer string, predicate []byte) string {
	t.Helper()
	var bundle map[string]any
	if err := json.Unmarshal([]byte(testOCIBundle(t, digest, predicateType, identity, issuer)), &bundle); err != nil {
		t.Fatal(err)
	}
	envelope := bundle["dsseEnvelope"].(map[string]any)
	raw, err := base64.StdEncoding.DecodeString(envelope["payload"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var statement map[string]any
	if err := json.Unmarshal(raw, &statement); err != nil {
		t.Fatal(err)
	}
	statement["predicate"] = json.RawMessage(predicate)
	raw, err = json.Marshal(statement)
	if err != nil {
		t.Fatal(err)
	}
	envelope["payload"] = base64.StdEncoding.EncodeToString(raw)
	raw, err = json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestQualificationV2AttestationUsesSeparateTypeAndVerifiesExactBytes(t *testing.T) {
	const predicate = `{"schemaVersion":"nvkr.dev/qualification-evidence/v2","verdict":"pass"}`
	for _, tc := range []struct {
		name, failure string
		code          e.Code
	}{
		{name: "verified"},
		{name: "attaching fails", failure: "attest", code: e.CodeSignatureInvalid},
		{name: "crypto fails", failure: "verify-blob-attestation", code: e.CodeSignatureInvalid},
		{name: "changed predicate file", failure: "mutation", code: e.CodeSignatureInvalid},
		{name: "substituted predicate", failure: "predicate", code: e.CodeVerifyFailed},
		{name: "SLSA substituted", failure: "type", code: e.CodeVerifyFailed},
		{name: "wrong subject", failure: "subject", code: e.CodeVerifyFailed},
		{name: "builder identity", failure: "identity", code: e.CodeUntrustedSigner},
		{name: "wrong issuer", failure: "issuer", code: e.CodeUntrustedSigner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest, typ, identity, issuer, document := ociTestDigest, QualificationPredicateTypeV2, ociObservedIdentity, ociTestIssuer, predicate
			switch tc.failure {
			case "predicate":
				document = `{"verdict":"forged"}`
			case "type":
				typ = ProvenancePredicateType
			case "subject":
				digest = "sha256:" + strings.Repeat("b", 64)
			case "identity":
				identity = "https://github.com/other/build.yml@refs/heads/main"
			case "issuer":
				issuer = "https://wrong.example"
			}
			stub := &cosignStub{stdout: map[string]string{"download": qualificationBundleV2(t, digest, typ, identity, issuer, []byte(document))}}
			stub.onCall = func(call recordedCall) error {
				if call.args[0] == tc.failure {
					return fmt.Errorf("injected failure")
				}
				if call.args[0] == "attest" {
					if strings.Join(call.args[:6], " ") != "attest --yes --new-bundle-format --type "+QualificationPredicateTypeV2+" --predicate" || call.args[len(call.args)-1] != ociTestRef {
						t.Fatalf("bad qualification argv: %v", call.args)
					}
					raw, err := os.ReadFile(call.args[6])
					if err != nil {
						return err
					}
					if string(raw) != predicate {
						t.Fatal("changed predicate")
					}
					if tc.failure == "mutation" {
						return os.WriteFile(call.args[6], []byte("changed"), 0600)
					}
				}
				if call.args[0] == "download" && call.args[3] != QualificationPredicateTypeV2 {
					t.Fatal("qualification downloaded another evidence type")
				}
				if call.args[0] == "verify-blob-attestation" && call.args[4] != QualificationPredicateTypeV2 {
					t.Fatal("qualification verified another evidence type")
				}
				return nil
			}
			err := newStubbedSigner(t, stub, Credential{}).AttestQualificationV2(t.Context(), ociSubject(), []byte(predicate))
			if tc.code != "" {
				requireSignCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.code == "" && (len(stub.calls) != 3 || stub.calls[0].args[0] != "attest" || stub.calls[1].args[0] != "download" || stub.calls[2].args[0] != "verify-blob-attestation") {
				t.Fatalf("calls=%+v", stub.calls)
			}
			for _, call := range stub.calls {
				if call.args[0] == "sign" {
					t.Fatal("qualification unexpectedly signed build signature")
				}
			}
		})
	}
}

func TestQualificationV2VerificationRequiresExpectedEvidence(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`{broken`)} {
		t.Run(string(raw), func(t *testing.T) {
			stub := &cosignStub{}
			err := newStubbedSigner(t, stub, Credential{}).VerifyQualificationV2(t.Context(), ociSubject(), raw)
			requireSignCode(t, err, e.CodeVerifyFailed)
			if len(stub.calls) != 0 {
				t.Fatalf("invalid evidence reached subprocess: %v", err)
			}
		})
	}
}

func TestQualificationV2InvalidPolicyCannotAttach(t *testing.T) {
	stub := &cosignStub{}
	signer := newStubbedSigner(t, stub, Credential{})
	signer.identity = "["
	err := signer.AttestQualificationV2(t.Context(), ociSubject(), []byte(`{"verdict":"pass"}`))
	requireSignCode(t, err, e.CodeTrustPolicyMissing)
	if len(stub.calls) != 0 {
		t.Fatal("invalid verification policy attached an attestation")
	}
}

func TestQualificationV2PassiveVerificationFixedAuthority(t *testing.T) {
	const predicate = `{"schemaVersion":"nvkr.dev/qualification-evidence/v2","verdict":"pass"}`
	for _, tc := range []struct {
		name, kind string
		code       e.Code
	}{
		{name: "verified"},
		{name: "v1 predicate", kind: "type", code: e.CodeVerifyFailed},
		{name: "wrong subject", kind: "subject", code: e.CodeVerifyFailed},
		{name: "wrong exact bytes", kind: "predicate", code: e.CodeVerifyFailed},
		{name: "wrong signer", kind: "identity", code: e.CodeUntrustedSigner},
		{name: "wrong issuer", kind: "issuer", code: e.CodeUntrustedSigner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest, typ, identity, issuer, document := ociTestDigest, QualificationPredicateTypeV2, ociObservedIdentity, ociTestIssuer, predicate
			switch tc.kind {
			case "type":
				typ = QualificationPredicateType
			case "subject":
				digest = "sha256:" + strings.Repeat("b", 64)
			case "predicate":
				document = `{"verdict":"forged"}`
			case "identity":
				identity = "https://github.com/other/build.yml@refs/heads/main"
			case "issuer":
				issuer = "https://wrong.example"
			}
			stub := &cosignStub{stdout: map[string]string{"download": qualificationBundleV2(t, digest, typ, identity, issuer, []byte(document))}}
			err := newStubbedSigner(t, stub, Credential{}).VerifyQualificationV2(t.Context(), ociSubject(), []byte(predicate))
			if tc.code != "" {
				requireSignCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
			for _, call := range stub.calls {
				if call.args[0] == "attest" || call.args[0] == "sign" {
					t.Fatal("passive verification requested signing authority")
				}
			}
			if tc.code == "" && (len(stub.calls) != 2 || stub.calls[0].args[0] != "download" || stub.calls[1].args[0] != "verify-blob-attestation") {
				t.Fatalf("verification calls=%+v", stub.calls)
			}
		})
	}
}
