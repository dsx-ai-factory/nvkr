// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

const ociObservedIdentity = "https://github.com/NVIDIA/nke/.github/workflows/nkd-factory.yml@refs/heads/main"

func testOCIBundle(t *testing.T, digest, predicate, identity, issuer string) string {
	t.Helper()
	certificate, err := parseCertificate(testSigningCertificate(t, identity, issuer))
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(cosignAttestationJSON(digest, predicate)), &envelope); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"mediaType": sbom.BundleMediaType, "dsseEnvelope": envelope,
		"verificationMaterial": map[string]any{"certificate": map[string]string{"rawBytes": base64.StdEncoding.EncodeToString(certificate.Raw)}}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestOCIBundleAdmissionBeforeIO(t *testing.T) {
	for _, tc := range []struct {
		name     string
		canceled bool
		pattern  string
		want     e.Code
	}{
		{"canceled", true, ociTestIdentity, e.CodeTimeout}, {"invalid policy", false, "[", e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tc.canceled {
				cancel()
			}
			stub := &cosignStub{}
			_, err := newStubbedSigner(t, stub, Credential{}).Verify(ctx, ociSubject(), VerifyPolicy{IdentityRegexp: tc.pattern})
			requireSignCode(t, err, tc.want)
			if len(stub.calls) > 0 {
				t.Fatal("admission failure performed external work")
			}
		})
	}
}

func TestCosignOutputCaptureIsBounded(t *testing.T) {
	for _, method := range []string{"write", "copy", "string"} {
		t.Run(method, func(t *testing.T) {
			var output cosignOutputBuffer
			raw := strings.Repeat("x", int(maxCosignOutputBytes)+1)
			var err error
			switch method {
			case "write":
				_, err = output.Write([]byte(raw))
			case "copy":
				_, err = io.Copy(&output, strings.NewReader(raw))
			case "string":
				_, err = io.WriteString(&output, raw)
			}
			if err == nil || !output.exceeded || int64(len(output.Bytes())) != maxCosignOutputBytes {
				t.Fatal("output limit was bypassed")
			}
		})
	}
}

func TestCosignOCIBundleVerification(t *testing.T) {
	for _, tc := range []struct {
		name     string
		required bool
		failure  string
		want     e.Code
	}{
		{name: "signature only"}, {name: "signature and provenance", required: true},
		{name: "download failure", failure: "download", want: e.CodeSignatureInvalid},
		{name: "signature crypto failure", failure: "signature", want: e.CodeSignatureInvalid},
		{name: "provenance crypto failure", required: true, failure: "provenance", want: e.CodeSignatureInvalid},
		{name: "changed verified bytes", failure: "mutation", want: e.CodeSignatureInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signature := testOCIBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer)
			provenance := testOCIBundle(t, ociTestDigest, ProvenancePredicateType, ociObservedIdentity, ociTestIssuer)
			stub := &cosignStub{stdout: map[string]string{"version": cosignVersionJSON, "verify-blob-attestation": `{"Subject":"untrusted presentation text"}`}}
			var paths []string
			stub.onCall = func(call recordedCall) error {
				if call.args[0] == "download" {
					if tc.failure == "download" {
						return fmt.Errorf("download failed")
					}
					if call.args[1] != "attestation" || call.args[2] != "--predicate-type" || call.args[4] != ociTestRef {
						t.Fatalf("download argv: %v", call.args)
					}
					stub.stdout["download"] = signature
					if call.args[3] == ProvenancePredicateType {
						stub.stdout["download"] = provenance
					}
				}
				if call.args[0] == "verify-blob-attestation" {
					path := call.args[2]
					paths = append(paths, path)
					info, err := os.Stat(path)
					if err != nil {
						return err
					}
					if info.Mode().Perm() != 0o600 {
						t.Fatal("bundle is not private")
					}
					raw, err := os.ReadFile(path)
					if err != nil {
						return err
					}
					want := signature
					if call.args[4] == ProvenancePredicateType {
						want = provenance
					}
					if string(raw) != want {
						t.Fatal("cosign received different bundle bytes")
					}
					if call.args[5] != "--digest" || call.args[6] != strings.TrimPrefix(ociTestDigest, "sha256:") || call.args[9] != "--certificate-identity-regexp" || call.args[10] != ociTestIdentity || call.args[12] != ociTestIssuer {
						t.Fatalf("verification policy: %v", call.args)
					}
					if tc.failure == "signature" || (tc.failure == "provenance" && call.args[4] == ProvenancePredicateType) {
						return fmt.Errorf("crypto verification failed")
					}
					if tc.failure == "mutation" {
						return os.WriteFile(path, []byte("substituted"), 0o600)
					}
				}
				return nil
			}
			record, err := newStubbedSigner(t, stub, Credential{}).Verify(t.Context(), ociSubject(), VerifyPolicy{RequireAttestation: tc.required})
			if tc.want != "" {
				requireSignCode(t, err, tc.want)
				if record.SignatureVerified || record.AttestationVerified || len(record.Ran) > 0 {
					t.Fatal("returned partial verification success")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				want := 2
				if tc.required {
					want = 4
				}
				if len(record.Ran) != want || !record.SignatureVerified || record.AttestationVerified != tc.required || record.ObservedIdentity != ociObservedIdentity || record.ObservedIssuer != ociTestIssuer {
					t.Fatalf("record: %+v", record)
				}
			}
			for _, path := range paths {
				if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
					t.Fatalf("bundle directory survived: %v", err)
				}
			}
		})
	}
}

func TestOCIBundleRefusesSubstitutedClaims(t *testing.T) {
	for _, tc := range []struct {
		name, digest, predicate, identity, issuer string
		want                                      e.Code
	}{
		{"wrong digest", "sha256:" + strings.Repeat("8", 64), CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer, e.CodeVerifyFailed},
		{"SBOM is not signature", ociTestDigest, sbom.CycloneDXPredicateType, ociObservedIdentity, ociTestIssuer, e.CodeVerifyFailed},
		{"SLSA is not signature", ociTestDigest, ProvenancePredicateType, ociObservedIdentity, ociTestIssuer, e.CodeVerifyFailed},
		{"untrusted identity", ociTestDigest, CosignSignaturePredicateType, "attacker", ociTestIssuer, e.CodeUntrustedSigner},
		{"untrusted issuer", ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, "https://attacker", e.CodeUntrustedSigner},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &cosignStub{stdout: map[string]string{"download": testOCIBundle(t, tc.digest, tc.predicate, tc.identity, tc.issuer)}}
			_, err := newStubbedSigner(t, stub, Credential{}).Verify(t.Context(), ociSubject(), VerifyPolicy{})
			requireSignCode(t, err, tc.want)
			if len(stub.calls) != 1 {
				t.Fatal("invalid bundle reached verification")
			}
		})
	}
}

func TestOCIBundleFailureSummary(t *testing.T) {
	for _, tc := range []struct {
		name             string
		claimsRejected   int
		verificationFail int
	}{
		{"no claims match", 2, 0},
		{"mixed failures", 1, 1},
		{"all crypto fail", 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundles := make([]string, 0, 2)
			for range tc.claimsRejected {
				bundles = append(bundles, `{}`)
			}
			for range tc.verificationFail {
				bundles = append(bundles, testOCIBundle(t, ociTestDigest, CosignSignaturePredicateType, ociObservedIdentity, ociTestIssuer))
			}
			stub := &cosignStub{stdout: map[string]string{"download": strings.Join(bundles, "\n")}}
			attempted := 0
			stub.onCall = func(call recordedCall) error {
				if call.args[0] == "verify-blob-attestation" {
					attempted++
					return fmt.Errorf("crypto verification failed")
				}
				return nil
			}
			record, err := newStubbedSigner(t, stub, Credential{}).Verify(t.Context(), ociSubject(), VerifyPolicy{})
			requireSignCode(t, err, e.CodeSignatureInvalid)
			want := fmt.Sprintf("all 2 OCI bundles failed (%d rejected by claims checks, %d failed cosign verification); last failure:", tc.claimsRejected, tc.verificationFail)
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("missing failure summary %q: %v", want, err)
			}
			if attempted != tc.verificationFail {
				t.Fatalf("cosign attempts = %d, want %d", attempted, tc.verificationFail)
			}
			if record.SignatureVerified || record.AttestationVerified || len(record.Ran) != 0 {
				t.Fatal("returned partial verification success")
			}
		})
	}
}

func TestOCIBundleDownloadRejectsLegacyAndMalformedReports(t *testing.T) {
	for _, raw := range []string{"", "not json", "[]", cosignAttestationJSON(ociTestDigest, CosignSignaturePredicateType), strings.Repeat("{}\n", maxVerificationBundles+1), `{"mediaType":"a","mediaType":"b"}`} {
		t.Run(fmt.Sprintf("%.30s", raw), func(t *testing.T) {
			_, err := newStubbedSigner(t, &cosignStub{stdout: map[string]string{"download": raw}}, Credential{}).Verify(t.Context(), ociSubject(), VerifyPolicy{})
			requireSignCode(t, err, e.CodeSignatureInvalid)
		})
	}
}
