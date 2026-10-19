// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/testutil"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
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
			if tc.padding > 0 && tc.code == "" {
				testutil.RequireExtended(t)
			}
			predicate := document
			if tc.padding != 0 {
				large := *bom
				large.Metadata.Props = append(append([]sbom.Property(nil), bom.Metadata.Props...), sbom.Property{Name: "nvkr:zpayload", Value: strings.Repeat("x", tc.padding)})
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

func TestSBOMAttestationRekorConflictRecovery(t *testing.T) {
	const conflict = `Error: creating bundle: signing bundle: error signing bundle: [POST /api/v1/log/entries][409] createLogEntryConflict {"code":409,"message":"an equivalent entry already exists in the transparency log with UUID 108e9186e8c5677a0b166fafffd96f895b290621a11c737c3c628334d37ef48feb7f3de8a13932b4"}`
	for _, tc := range []struct {
		name     string
		mode     string
		failures int
		attempts int
		verifies int
		elapsed  time.Duration
		code     e.Code
	}{
		{name: "fresh invocation succeeds", failures: 1, attempts: 2, verifies: 1, elapsed: time.Second},
		{name: "third invocation succeeds", failures: 2, attempts: 3, verifies: 1, elapsed: 3 * time.Second},
		{name: "conflict bound", failures: 3, attempts: 3, elapsed: 3 * time.Second, code: e.CodeSignatureInvalid},
		{name: "unrelated failure", mode: "unrelated", failures: 1, attempts: 1, code: e.CodeSignatureInvalid},
		{name: "registry conflict", mode: "registry", failures: 1, attempts: 1, code: e.CodeSignatureInvalid},
		{name: "oversized stderr", mode: "oversized", failures: 1, attempts: 1, code: e.CodeSignatureInvalid},
		{name: "mutation on failed invocation", mode: "mutate input", failures: 1, attempts: 1, code: e.CodeSignatureInvalid},
		{name: "caller mutation cannot replace snapshot", mode: "mutate caller", failures: 1, attempts: 2, verifies: 1, elapsed: time.Second},
		{name: "failed output cannot satisfy later attempt", mode: "stale output", failures: 1, attempts: 2, elapsed: time.Second, code: e.CodeSignatureInvalid},
		{name: "verification conflict is terminal", mode: "verify conflict", attempts: 1, verifies: 1, code: e.CodeSignatureInvalid},
		{name: "backoff cancellation", mode: "cancel", failures: 1, attempts: 1, elapsed: 100 * time.Millisecond, code: e.CodeTimeout},
		{name: "overall deadline", mode: "deadline", attempts: 1, elapsed: 3 * time.Minute, code: e.CodeTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				bom, err := sbom.NewSubject(ociTestDigest, "component", ociTestDigest)
				if err != nil {
					t.Fatal(err)
				}
				predicate, err := bom.Marshal()
				if err != nil {
					t.Fatal(err)
				}
				expected := bytes.Clone(predicate)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				started := time.Now()
				attempts, verifies := 0, 0
				outputs := make(map[string]bool)
				runner := EnvRunnerFunc(func(ctx context.Context, _ string, args, _ []string, _, stderr io.Writer) error {
					flag := func(name string) string {
						for i, arg := range args {
							if arg == name && i+1 < len(args) {
								return args[i+1]
							}
						}
						return ""
					}
					deadline, ok := ctx.Deadline()
					if !ok || !deadline.Equal(started.Add(3*time.Minute)) {
						t.Fatalf("invocation does not share the overall deadline: %v", deadline)
					}
					if args[0] == "verify-blob-attestation" {
						verifies++
						if flag("--certificate-identity-regexp") != ociTestIdentity || flag("--certificate-oidc-issuer") != ociTestIssuer || flag("--digest") != strings.TrimPrefix(ociTestDigest, "sha256:") {
							t.Fatal("recovery weakened verification policy", args)
						}
						if tc.mode == "verify conflict" {
							_, _ = io.WriteString(stderr, conflict)
							return io.ErrUnexpectedEOF
						}
						return nil
					}
					if args[0] != "attest-blob" {
						t.Fatalf("unexpected operation: %s", args[0])
					}
					attempts++
					output := flag("--bundle")
					if outputs[output] {
						t.Fatal("retry reused a prior bundle output")
					}
					outputs[output] = true
					raw, err := os.ReadFile(flag("--predicate"))
					if err != nil {
						return err
					}
					if !bytes.Equal(raw, expected) {
						t.Fatal("retry changed the predicate")
					}
					if tc.mode == "deadline" {
						<-ctx.Done()
						return ctx.Err()
					}
					if attempts <= tc.failures {
						diagnostic := conflict
						switch tc.mode {
						case "unrelated":
							diagnostic = "Error: certificate verification failed"
						case "registry":
							diagnostic = strings.Replace(conflict, "/api/v1/log/entries", "/v2/example/manifests/latest", 1)
						case "oversized":
							diagnostic += strings.Repeat("x", int(maxCosignOutputBytes))
						case "mutate input":
							if err := os.WriteFile(flag("--predicate"), []byte(`{}`), 0600); err != nil {
								return err
							}
						case "mutate caller":
							predicate[0] = '!'
						case "stale output":
							if err := os.WriteFile(output, testSBOMBundle(t, ociTestDigest, raw), 0600); err != nil {
								return err
							}
						case "cancel":
							time.AfterFunc(100*time.Millisecond, cancel)
						}
						_, _ = io.WriteString(stderr, diagnostic)
						return io.ErrUnexpectedEOF
					}
					if tc.mode == "stale output" {
						return nil
					}
					return os.WriteFile(output, testSBOMBundle(t, ociTestDigest, raw), 0600)
				})
				signer, err := NewCosignOCISigner(CosignOCIOptions{Identity: ociTestIdentity, Issuer: ociTestIssuer, Runner: runner})
				if err != nil {
					t.Fatal(err)
				}
				bundle, err := signer.AttestSBOM(ctx, ociTestDigest, predicate)
				if tc.code != "" {
					requireSignCode(t, err, tc.code)
					if bundle != nil {
						t.Fatal("failed recovery returned a bundle")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					document, err := sbom.AttestedDocument(bundle, ociTestDigest)
					if err != nil || !bytes.Equal(document, expected) {
						t.Fatalf("recovery did not preserve the admitted predicate: %v", err)
					}
				}
				if attempts != tc.attempts || verifies != tc.verifies || time.Since(started) != tc.elapsed {
					t.Fatalf("attempts=%d verification=%d elapsed=%v; want %d, %d, %v", attempts, verifies, time.Since(started), tc.attempts, tc.verifies, tc.elapsed)
				}
			})
		})
	}
}
