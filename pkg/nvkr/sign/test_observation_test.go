// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func observationBundle(t *testing.T, digest, predicateType, identity, issuer string, predicate []byte) string {
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

func TestObservationAttestationUsesSeparateTypeAndVerifiesExactBytes(t *testing.T) {
	const predicate = `{"schemaVersion":"nvkr.dev/test-observation/v1","verdict":"fail"}`
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
		{name: "qualification substituted", failure: "qualification-type", code: e.CodeVerifyFailed},
		{name: "unknown kind", failure: "unknown-type", code: e.CodeVerifyFailed},
		{name: "wrong subject", failure: "subject", code: e.CodeVerifyFailed},
		{name: "builder identity", failure: "identity", code: e.CodeUntrustedSigner},
		{name: "wrong issuer", failure: "issuer", code: e.CodeUntrustedSigner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			digest, typ, identity, issuer, document := ociTestDigest, TestObservationPredicateType, ociObservedIdentity, ociTestIssuer, predicate
			switch tc.failure {
			case "predicate":
				document = `{"verdict":"forged"}`
			case "type":
				typ = ProvenancePredicateType
			case "qualification-type":
				typ = QualificationPredicateType
			case "unknown-type":
				typ = "https://example.invalid/unknown"
			case "subject":
				digest = "sha256:" + strings.Repeat("b", 64)
			case "identity":
				identity = "https://github.com/other/build.yml@refs/heads/main"
			case "issuer":
				issuer = "https://wrong.example"
			}
			stub := &cosignStub{stdout: map[string]string{"download": observationBundle(t, digest, typ, identity, issuer, []byte(document))}}
			stub.onCall = func(call recordedCall) error {
				configDir := ""
				for _, entry := range call.env {
					if strings.HasPrefix(entry, "DOCKER_CONFIG=") {
						configDir = strings.TrimPrefix(entry, "DOCKER_CONFIG=")
					}
				}
				if configDir == "" {
					t.Fatal("credential-free observation can consult ambient Docker keychain")
				}
				configBytes, configErr := os.ReadFile(filepath.Join(configDir, "config.json"))
				if configErr != nil || string(configBytes) != `{"auths":{}}` {
					t.Fatal("observation did not select explicit empty credential config", configErr)
				}
				if call.args[0] == tc.failure {
					return fmt.Errorf("injected failure")
				}
				if call.args[0] == "attest" {
					if strings.Join(call.args[:6], " ") != "attest --yes --new-bundle-format --type "+TestObservationPredicateType+" --predicate" || call.args[len(call.args)-1] != ociTestRef {
						t.Fatalf("bad test observation argv: %v", call.args)
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
				if call.args[0] == "download" && call.args[3] != TestObservationPredicateType {
					t.Fatal("test observation downloaded another evidence type")
				}
				if call.args[0] == "verify-blob-attestation" && call.args[4] != TestObservationPredicateType {
					t.Fatal("test observation verified another evidence type")
				}
				return nil
			}
			err := newStubbedSigner(t, stub, Credential{}).AttestTestObservation(t.Context(), ociSubject(), []byte(predicate))
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
					t.Fatal("test observation unexpectedly signed build signature")
				}
			}
		})
	}
}

func TestObservationVerificationRequiresExpectedEvidence(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte(`{broken`)} {
		t.Run(string(raw), func(t *testing.T) {
			stub := &cosignStub{}
			err := newStubbedSigner(t, stub, Credential{}).VerifyTestObservation(t.Context(), ociSubject(), raw)
			if err == nil || len(stub.calls) != 0 {
				t.Fatalf("invalid evidence reached subprocess: %v", err)
			}
		})
	}
}

func TestObservationInvalidPolicyCannotAttach(t *testing.T) {
	stub := &cosignStub{}
	signer := newStubbedSigner(t, stub, Credential{})
	signer.identity = "["
	err := signer.AttestTestObservation(t.Context(), ociSubject(), []byte(`{"verdict":"fail"}`))
	requireSignCode(t, err, e.CodeTrustPolicyMissing)
	if len(stub.calls) != 0 {
		t.Fatal("invalid verification policy attached an attestation")
	}
}

func TestObservationAdmissionAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		predicate []byte
		cancel    bool
		code      e.Code
	}{
		{"empty", nil, false, e.CodeVerifyFailed},
		{"broken", []byte(`{broken`), false, e.CodeVerifyFailed},
		{"canceled", []byte(`{"verdict":"fail"}`), true, e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &cosignStub{}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			err := newStubbedSigner(t, stub, Credential{}).AttestTestObservation(ctx, ociSubject(), tc.predicate)
			requireSignCode(t, err, tc.code)
			if len(stub.calls) != 0 {
				t.Fatal("invalid observation reached subprocess")
			}
		})
	}
}

func TestObservationVerificationExactBytesAndNoMintingAuthority(t *testing.T) {
	const predicate = `{"schemaVersion":"nvkr.dev/test-observation/v1","verdict":"fail"}`
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "fixture-token")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "https://fixture.invalid")
	for _, tc := range []struct {
		name     string
		expected []byte
		code     e.Code
	}{
		{"exact failed observation", []byte(predicate), ""},
		{"reformatted equivalent", []byte(`{ "schemaVersion":"nvkr.dev/test-observation/v1","verdict":"fail"}`), e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &cosignStub{stdout: map[string]string{"download": observationBundle(t, ociTestDigest, TestObservationPredicateType, ociObservedIdentity, ociTestIssuer, []byte(predicate))}}
			stub.onCall = func(call recordedCall) error {
				for _, entry := range call.env {
					if strings.HasPrefix(entry, "ACTIONS_ID_TOKEN_REQUEST_") {
						t.Fatal("verification inherited OIDC minting authority")
					}
				}
				return nil
			}
			err := newStubbedSigner(t, stub, Credential{}).VerifyTestObservation(t.Context(), ociSubject(), tc.expected)
			if tc.code != "" {
				requireSignCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
			for _, call := range stub.calls {
				if call.args[0] == "attest" || call.args[0] == "sign" {
					t.Fatal("verification attempted signing")
				}
			}
		})
	}
}

func TestObservationCanceledAfterAttachmentNeverSucceeds(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stub := &cosignStub{}
	stub.onCall = func(call recordedCall) error {
		if call.args[0] == "attest" {
			cancel()
		}
		return nil
	}
	err := newStubbedSigner(t, stub, Credential{}).AttestTestObservation(ctx, ociSubject(), []byte(`{"verdict":"fail"}`))
	requireSignCode(t, err, e.CodeTimeout)
}

func TestObservationStockAdmissionBeforePathIO(t *testing.T) {
	signer, err := NewCosignOCISigner(CosignOCIOptions{Binary: "/does/not/exist/cosign", Identity: ociTestIdentity, Issuer: ociTestIssuer})
	if err != nil {
		t.Fatal(err)
	}
	want := e.CodeUnpinnedInput
	if runtime.GOOS != "linux" {
		want = e.CodeEngineCapability
	}
	for _, operation := range []struct {
		name string
		run  func(context.Context, Subject, []byte) error
	}{
		{"attest", signer.AttestTestObservation}, {"verify", signer.VerifyTestObservation},
	} {
		t.Run(operation.name, func(t *testing.T) {
			requireSignCode(t, operation.run(t.Context(), ociSubject(), []byte(`{"verdict":"fail"}`)), want)
		})
	}
}
