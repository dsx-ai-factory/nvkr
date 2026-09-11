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
		code             e.Code
	}{
		{name: "verified"}, {name: "substituted subject", substitute: true, code: e.CodeVerifyFailed}, {name: "verification failure", fail: true, code: e.CodeSignatureInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			bundle, err := signer.AttestSBOM(t.Context(), ociTestDigest, document)
			if tc.code != "" {
				requireSignCode(t, err, tc.code)
				if bundle != nil {
					t.Fatal("failed attestation returned bundle")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(calls, ",") != "attest-blob,verify-blob-attestation" {
				t.Fatal("missing verification", calls)
			}
		})
	}
}
