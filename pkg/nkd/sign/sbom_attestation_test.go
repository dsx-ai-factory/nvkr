// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

func testSBOMBundle(t *testing.T, digest string, document []byte) []byte {
	t.Helper()
	statement, err := json.Marshal(map[string]any{"_type": "https://in-toto.io/Statement/v1", "predicateType": sbom.CycloneDXPredicateType,
		"subject": []any{map[string]any{"name": "artifact", "digest": map[string]string{"sha256": strings.TrimPrefix(digest, "sha256:")}}}, "predicate": json.RawMessage(document)})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := json.Marshal(map[string]any{"mediaType": sbom.BundleMediaType, "verificationMaterial": map[string]any{},
		"dsseEnvelope": map[string]any{"payloadType": "application/vnd.in-toto+json", "payload": base64.StdEncoding.EncodeToString(statement), "signatures": []any{map[string]string{"sig": "Zml4dHVyZQ=="}}}})
	if err != nil {
		t.Fatal(err)
	}
	return bundle
}

func TestSBOMAttestationCreationRequiresExactPredicateAndVerification(t *testing.T) {
	bom, err := sbom.NewSubject(ociTestDigest, "component", ociTestDigest)
	if err != nil {
		t.Fatal(err)
	}
	document, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name             string
		substitute, fail bool
		padding          int
		code             e.Code
	}{
		{name: "verified"}, {name: "substituted subject", substitute: true, code: e.CodeVerifyFailed}, {name: "verification failure", fail: true, code: e.CodeSignatureInvalid},
		{name: "large complete inventory", padding: 25 << 20},
		{name: "large inventory signed and verified", padding: sbom.MaxDocumentBytes - 4096},
		{name: "inventory over document limit", padding: sbom.MaxDocumentBytes, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			predicate := document
			if tc.padding != 0 {
				large := *bom
				large.Metadata.Props = append(append([]sbom.Property(nil), bom.Metadata.Props...), sbom.Property{Name: "nkd:zpayload", Value: strings.Repeat("x", tc.padding)})
				predicate, err = json.Marshal(&large)
				if err != nil {
					t.Fatal(err)
				}
				if tc.code == "" {
					predicate, err = large.Marshal()
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			var calls []string
			runner := EnvRunnerFunc(func(_ context.Context, _ string, args, env []string, _, _ io.Writer) error {
				calls = append(calls, args[0])
				flag := func(name string) string {
					for i, a := range args {
						if a == name && i+1 < len(args) {
							return args[i+1]
						}
					}
					return ""
				}
				switch args[0] {
				case "attest-blob":
					if flag("--type") != "cyclonedx" || flag("--hash") != strings.TrimPrefix(ociTestDigest, "sha256:") || !strings.Contains(strings.Join(args, " "), "--new-bundle-format") {
						t.Fatal("wrong signing arguments", args)
					}
					raw, err := os.ReadFile(flag("--predicate"))
					if err != nil {
						return err
					}
					digest := ociTestDigest
					if tc.substitute {
						digest = "sha256:" + strings.Repeat("a", 64)
					}
					return os.WriteFile(flag("--bundle"), testSBOMBundle(t, digest, raw), 0600)
				case "verify-blob-attestation":
					if flag("--digest") != strings.TrimPrefix(ociTestDigest, "sha256:") || flag("--certificate-identity-regexp") != ociTestIdentity || flag("--certificate-oidc-issuer") != ociTestIssuer {
						t.Fatal("wrong verification policy", args)
					}
					if tc.fail {
						return io.ErrUnexpectedEOF
					}
				default:
					t.Fatalf("unexpected operation %s", args[0])
				}
				return nil
			})
			signer, err := NewCosignOCISigner(CosignOCIOptions{Identity: ociTestIdentity, Issuer: ociTestIssuer, Runner: runner})
			if err != nil {
				t.Fatal(err)
			}
			bundle, err := signer.AttestSBOM(t.Context(), ociTestDigest, predicate)
			if tc.code != "" {
				requireSignCode(t, err, tc.code)
				if bundle != nil {
					t.Fatal("failed attestation returned bundle")
				}
				if tc.padding > 0 && len(calls) != 0 {
					t.Fatal("oversized inventory reached signer")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.padding == 25<<20 && (len(predicate) > sbom.MaxDocumentBytes || len(bundle) <= sbom.MaxDocumentBytes || len(bundle) > sbom.MaxBundleBytes) {
				t.Fatalf("fixture must separate document and bundle ceilings: document=%d bundle=%d", len(predicate), len(bundle))
			}
			if strings.Join(calls, ",") != "attest-blob,verify-blob-attestation" {
				t.Fatal("missing verification", calls)
			}
			if tc.padding == sbom.MaxDocumentBytes-4096 && len(bundle) <= sbom.MaxDocumentBytes {
				t.Fatal("fixture did not exercise expanded bundle")
			}
		})
	}
}

func TestSBOMAttestationDocumentPolicyBeforeCosign(t *testing.T) {
	for _, test := range []struct {
		name       string
		size       int
		raw        string
		provenance bool
		code       e.Code
		calls      int
	}{
		{name: "empty inventory", code: e.CodeVerifyFailed},
		{name: "malformed inventory", raw: "{not JSON", code: e.CodeVerifyFailed},
		{name: "inventory exceeds former provenance ceiling", size: (4 << 20) + 1, code: e.CodeSignatureInvalid, calls: 1},
		{name: "inventory at document ceiling", size: sbom.MaxDocumentBytes, code: e.CodeSignatureInvalid, calls: 1},
		{name: "inventory one byte over ceiling", size: sbom.MaxDocumentBytes + 1, code: e.CodeVerifyFailed},
		{name: "provenance one byte over its independent ceiling", size: (4 << 20) + 1, provenance: true, code: e.CodeProvenanceMissing},
	} {
		t.Run(test.name, func(t *testing.T) {
			predicate := []byte(test.raw)
			if test.size != 0 {
				predicate = []byte(`{"payload":"` + strings.Repeat("x", test.size-len(`{"payload":""}`)) + `"}`)
				if len(predicate) != test.size {
					t.Fatal("fixture has wrong byte size")
				}
			}
			calls := 0
			runner := EnvRunnerFunc(func(_ context.Context, _ string, _, _ []string, _, _ io.Writer) error {
				calls++
				return io.ErrUnexpectedEOF // Stop after admission; no signing claim.
			})
			signer, err := NewCosignOCISigner(CosignOCIOptions{Identity: ociTestIdentity, Issuer: ociTestIssuer, Runner: runner})
			if err != nil {
				t.Fatal(err)
			}
			if test.provenance {
				_, err = signer.SignAndAttest(t.Context(), ociSubject(), predicate)
			} else {
				bundle, attestErr := signer.AttestSBOM(t.Context(), ociTestDigest, predicate)
				err = attestErr
				if bundle != nil {
					t.Fatal("failed operation exposed a bundle")
				}
			}
			requireSignCode(t, err, test.code)
			if calls != test.calls {
				t.Fatalf("cosign calls=%d, want %d", calls, test.calls)
			}
		})
	}
}

func TestSBOMVerificationBundlePolicyBeforeCosign(t *testing.T) {
	calls := 0
	runner := EnvRunnerFunc(func(_ context.Context, _ string, _, _ []string, _, _ io.Writer) error { calls++; return nil })
	signer, err := NewCosignOCISigner(CosignOCIOptions{Identity: ociTestIdentity, Issuer: ociTestIssuer, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	err = signer.VerifySBOM(t.Context(), ociTestDigest, []byte(strings.Repeat(" ", sbom.MaxBundleBytes+1)))
	requireSignCode(t, err, e.CodeVerifyFailed)
	if calls != 0 {
		t.Fatal("oversized bundle reached cosign")
	}
}
