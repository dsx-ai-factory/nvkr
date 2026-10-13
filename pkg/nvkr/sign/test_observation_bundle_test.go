// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestObservationDetachedBundleCreationUsesNoRegistryCredential(t *testing.T) {
	const predicate = `{"state":"failed"}`
	t.Setenv("NVKR_REGISTRY_PASSWORD", "PRIVATE_MUST_NOT_REACH_COSIGN")
	bundle := []byte(observationBundle(t, ociTestDigest, TestObservationPredicateType, ociObservedIdentity, ociTestIssuer, []byte(predicate)))
	stub := &cosignStub{}
	stub.onCall = func(call recordedCall) error {
		for _, entry := range call.env {
			if strings.Contains(entry, "PRIVATE_MUST_NOT_REACH_COSIGN") {
				t.Fatal("registry credential entered offline cosign")
			}
		}
		switch call.args[0] {
		case "attest-blob":
			raw, err := os.ReadFile(call.args[4])
			if err != nil {
				return err
			}
			var statement inTotoStatement
			if err := json.Unmarshal(raw, &statement); err != nil {
				return err
			}
			if statement.PredicateType != TestObservationPredicateType || statement.Subject[0].Digest["sha256"] != strings.TrimPrefix(ociTestDigest, "sha256:") || string(statement.Predicate) != predicate {
				t.Fatal("statement differs")
			}
			return os.WriteFile(call.args[6], bundle, 0600)
		case "verify-blob-attestation":
			for _, entry := range call.env {
				if strings.HasPrefix(entry, "ACTIONS_ID_TOKEN_REQUEST_") {
					t.Fatal("verifier received minting authority")
				}
			}
		default:
			t.Fatalf("offline operation requested registry command: %v", call.args)
		}
		return nil
	}
	signer := newStubbedSigner(t, stub, Credential{Registry: "registry.example.test", Username: "fixture", Password: "PRIVATE_MUST_NOT_REACH_COSIGN"})
	raw, err := signer.CreateTestObservationBundle(t.Context(), observationOCISubject(), []byte(predicate))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(bundle) || len(stub.calls) != 2 {
		t.Fatalf("bundle/calls differ: %v", stub.calls)
	}
}

func TestObservationDetachedVerificationPolicyAndSubject(t *testing.T) {
	const predicate = `{"state":"failed"}`
	for _, tc := range []struct {
		name, kind string
		code       e.Code
	}{
		{name: "verified"}, {name: "subject name", kind: "name", code: e.CodeVerifyFailed}, {name: "statement version", kind: "version", code: e.CodeVerifyFailed}, {name: "subject", kind: "subject", code: e.CodeVerifyFailed}, {name: "type", kind: "type", code: e.CodeVerifyFailed}, {name: "predicate", kind: "predicate", code: e.CodeVerifyFailed}, {name: "identity", kind: "identity", code: e.CodeUntrustedSigner}, {name: "issuer", kind: "issuer", code: e.CodeUntrustedSigner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, typ, id, iss, pred := ociTestDigest, TestObservationPredicateType, ociObservedIdentity, ociTestIssuer, predicate
			switch tc.kind {
			case "subject":
				d = "sha256:" + strings.Repeat("b", 64)
			case "type":
				typ = ProvenancePredicateType
			case "predicate":
				pred = `{"state":"forged"}`
			case "identity":
				id = "https://untrusted.example/signer"
			case "issuer":
				iss = "https://untrusted.example"
			}
			raw := []byte(observationBundle(t, d, typ, id, iss, []byte(pred)))
			if tc.kind == "name" || tc.kind == "version" {
				var document map[string]any
				if err := json.Unmarshal(raw, &document); err != nil {
					t.Fatal(err)
				}
				envelope := document["dsseEnvelope"].(map[string]any)
				payload, err := base64.StdEncoding.DecodeString(envelope["payload"].(string))
				if err != nil {
					t.Fatal(err)
				}
				var statement map[string]any
				if err := json.Unmarshal(payload, &statement); err != nil {
					t.Fatal(err)
				}
				if tc.kind == "name" {
					statement["subject"].([]any)[0].(map[string]any)["name"] = "other.example/image"
				} else {
					statement["_type"] = "https://in-toto.io/Statement/v0.1"
				}
				payload, err = json.Marshal(statement)
				if err != nil {
					t.Fatal(err)
				}
				envelope["payload"] = base64.StdEncoding.EncodeToString(payload)
				raw, err = json.Marshal(document)
				if err != nil {
					t.Fatal(err)
				}
			}
			stub := &cosignStub{}
			err := newStubbedSigner(t, stub, Credential{}).VerifyTestObservationBundle(t.Context(), observationOCISubject(), []byte(predicate), raw)
			if tc.code != "" {
				requireSignCode(t, err, tc.code)
				if len(stub.calls) != 0 {
					t.Fatal("invalid claim reached crypto")
				}
			} else if err != nil || len(stub.calls) != 1 {
				t.Fatalf("verification=%v calls=%d", err, len(stub.calls))
			}
		})
	}
}

func TestLegacyObservationOCIRejectsCredentialAndPlaintextBeforeIO(t *testing.T) {
	for _, method := range []string{"attest", "verify"} {
		for _, kind := range []string{"credential", "private-IP", "localhost"} {
			t.Run(method+kind, func(t *testing.T) {
				stub := &cosignStub{}
				credential := Credential{}
				subject := observationOCISubject()
				switch kind {
				case "credential":
					credential = Credential{Registry: "registry.example.test", Username: "fixture", Password: "secret"}
				case "private-IP":
					subject.Ref = "10.0.0.1/image@" + subject.Digest
				case "localhost":
					subject.Ref = "localhost:5000/image@" + subject.Digest
				}
				signer := newStubbedSigner(t, stub, credential)
				var err error
				if method == "attest" {
					err = signer.AttestTestObservation(t.Context(), subject, []byte(`{}`))
				} else {
					err = signer.VerifyTestObservation(t.Context(), subject, []byte(`{}`))
				}
				requireSignCode(t, err, e.CodeEngineCapability)
				if len(stub.calls) != 0 {
					t.Fatal("unsafe legacy operation reached cosign")
				}
			})
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stub := &cosignStub{}
	_, err := newStubbedSigner(t, stub, Credential{}).CreateTestObservationBundle(ctx, observationOCISubject(), []byte(`{}`))
	requireSignCode(t, err, e.CodeTimeout)
}
