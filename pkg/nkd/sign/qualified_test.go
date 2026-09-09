package sign

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	nkderr "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/trust"
)

func signCode(err error) nkderr.Code {
	if typed, ok := err.(*nkderr.Error); ok {
		return typed.Code
	}
	return ""
}

func writeMaterial(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testSigningCertificate(t *testing.T, identity, issuer string) []byte {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuerValue, err := asn1.Marshal(issuer)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:    big.NewInt(1),
		Subject:         pkix.Name{CommonName: "test signer"},
		NotBefore:       time.Unix(1, 0),
		NotAfter:        time.Unix(4_102_444_800, 0),
		KeyUsage:        x509.KeyUsageDigitalSignature,
		EmailAddresses:  []string{identity},
		ExtraExtensions: []pkix.Extension{{Id: fulcioOIDCIssuer, Value: issuerValue}},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, privateKey.Public(), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func qualifiedRequest(t *testing.T, identity, issuer string, offline bool) (trust.DocumentVerificationRequest, trust.SignatureMaterial) {
	t.Helper()
	document := writeMaterial(t, "document.snapshot", []byte(`{"name":"signed"}`))
	if err := os.Chmod(document, 0o400); err != nil {
		t.Fatal(err)
	}
	certificate := testSigningCertificate(t, identity, issuer)
	certificateBlock, _ := pem.Decode(certificate)
	bundle := []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json","verificationMaterial":{"certificate":{"rawBytes":"` +
		base64.StdEncoding.EncodeToString(certificateBlock.Bytes) + `"}}}`)
	original := trust.SignatureMaterial{
		SignaturePath:   writeMaterial(t, "document.sig", []byte("signature")),
		CertificatePath: writeMaterial(t, "document.pem", certificate),
		BundlePath:      writeMaterial(t, "document.bundle", bundle),
		TrustedRootPath: writeMaterial(t, "trusted-root.json", []byte(`{"root":"fixture"}`)),
	}
	rootRaw, err := os.ReadFile(original.TrustedRootPath)
	if err != nil {
		t.Fatal(err)
	}
	rootSum := sha256.Sum256(rootRaw)
	return trust.DocumentVerificationRequest{
		SnapshotPath:      document,
		Signature:         original,
		Offline:           offline,
		Role:              trust.RoleValidationVector,
		AllowedIdentities: []string{identity},
		OIDCIssuer:        issuer,
		TrustedRootDigest: "sha256:" + hex.EncodeToString(rootSum[:]),
	}, original
}

func TestQualifiedCosignUsesPrivateMaterialAndCapturesCertificateClaims(t *testing.T) {
	const identity = "release@example.test"
	const issuer = "https://issuer.example.test"
	req, original := qualifiedRequest(t, identity, issuer, true)
	var captured []string
	runner := RunnerFunc(func(_ context.Context, binary string, args []string, _, _ io.Writer) error {
		if binary != "cosign-under-test" {
			t.Fatalf("binary=%q", binary)
		}
		captured = append([]string(nil), args...)
		for _, originalPath := range []string{
			original.SignaturePath, original.CertificatePath, original.BundlePath, original.TrustedRootPath,
		} {
			if containsArg(args, originalPath) {
				t.Fatalf("cosign received mutable caller material path %q", originalPath)
			}
		}
		for _, flag := range []string{"--bundle", "--trusted-root"} {
			path := argAfter(t, args, flag)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm()&0o077 != 0 {
				t.Fatalf("%s path is not private: %o", flag, info.Mode().Perm())
			}
		}
		if containsArg(args, "--signature") || containsArg(args, "--certificate") {
			t.Fatalf("Sigstore v0.3 bundle must not receive incompatible detached flags: %v", args)
		}
		return nil
	})
	verifier := NewCosignVerifier("cosign-under-test", runner)
	evidence, err := verifier.VerifyDocument(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.CertificateIdentities) != 1 || evidence.CertificateIdentities[0] != identity {
		t.Fatalf("identity was not captured from certificate: %+v", evidence)
	}
	if evidence.OIDCIssuer != issuer || !strings.HasPrefix(evidence.CertificateSHA256, "sha256:") {
		t.Fatalf("certificate evidence incomplete: %+v", evidence)
	}
	for label, binding := range map[string]struct {
		path string
		got  string
	}{
		"signature":    {original.SignaturePath, evidence.SignatureSHA256},
		"certificate":  {original.CertificatePath, evidence.CertificateBlobSHA256},
		"bundle":       {original.BundlePath, evidence.BundleSHA256},
		"trusted root": {original.TrustedRootPath, evidence.TrustedRootBlobSHA256},
	} {
		raw, err := os.ReadFile(binding.path)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		want := "sha256:" + hex.EncodeToString(sum[:])
		if binding.got != want {
			t.Fatalf("%s proof digest=%q, want %q", label, binding.got, want)
		}
	}
	joined := strings.Join(captured, " ")
	if strings.Contains(joined, "--insecure-ignore-tlog") || strings.Contains(joined, "--offline") {
		t.Fatalf("qualified args contain an insecure/unsupported offline escape: %s", joined)
	}
	if !strings.Contains(joined, "--new-bundle-format=true") {
		t.Fatalf("trusted-root verification must select the Sigstore bundle format: %s", joined)
	}
}

func TestQualifiedCosignExecutesExactPinnedPrivateSnapshot(t *testing.T) {
	const identity = "release@example.test"
	const issuer = "https://issuer.example.test"
	req, _ := qualifiedRequest(t, identity, issuer, true)
	binaryBytes := []byte("fixture cosign executable bytes\n")
	binary := filepath.Join(t.TempDir(), "cosign")
	if err := os.WriteFile(binary, binaryBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(binaryBytes)
	want := "sha256:" + hex.EncodeToString(sum[:])

	called := false
	verifier, err := newPinnedCosignVerifier(binary, want, RunnerFunc(func(_ context.Context, gotBinary string, _ []string, _, _ io.Writer) error {
		called = true
		if gotBinary == binary {
			t.Fatal("qualified verifier executed the mutable configured path instead of a private snapshot")
		}
		got, err := os.ReadFile(gotBinary)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(binaryBytes) {
			t.Fatalf("private verifier snapshot changed: %q", got)
		}
		info, err := os.Stat(gotBinary)
		if err != nil {
			t.Fatal(err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o111 == 0 {
			t.Fatalf("verifier snapshot is not a private regular executable: %v", info.Mode())
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyDocument(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("pinned verifier runner was not invoked")
	}
}

func TestQualifiedCosignRejectsWrongBinaryPinBeforeRunner(t *testing.T) {
	req, _ := qualifiedRequest(t, "release@example.test", "https://issuer.example.test", true)
	binary := filepath.Join(t.TempDir(), "cosign")
	if err := os.WriteFile(binary, []byte("not the pinned executable\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	called := false
	verifier, err := newPinnedCosignVerifier(binary, "sha256:"+strings.Repeat("0", 64), RunnerFunc(func(context.Context, string, []string, io.Writer, io.Writer) error {
		called = true
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.VerifyDocument(context.Background(), req); signCode(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("wrong verifier binary pin must fail closed: %v", err)
	}
	if called {
		t.Fatal("runner was invoked after verifier binary pin mismatch")
	}
}

func TestQualifiedCosignRejectsAmbientUnpinnedExecRunner(t *testing.T) {
	req, _ := qualifiedRequest(t, "release@example.test", "https://issuer.example.test", true)
	verifier := NewCosignVerifier("cosign", nil)
	if _, err := verifier.VerifyDocument(context.Background(), req); signCode(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("ambient production verifier must require an exact binary pin: %v", err)
	}
}

func TestQualifiedCosignDoesNotCopyExpectedIdentityIntoResult(t *testing.T) {
	const actual = "actual@example.test"
	const expected = "expected@example.test"
	const issuer = "https://issuer.example.test"
	req, _ := qualifiedRequest(t, actual, issuer, false)
	req.AllowedIdentities = []string{expected}
	verifier := NewCosignVerifier("cosign-under-test", RunnerFunc(func(context.Context, string, []string, io.Writer, io.Writer) error {
		return nil // simulate a subprocess that failed to enforce its expected-identity flag
	}))
	_, err := verifier.VerifyDocument(context.Background(), req)
	if signCode(err) != nkderr.CodeUntrustedSigner {
		t.Fatalf("adapter must compare actual certificate SAN, not copy expected identity: %v", err)
	}
}

func TestQualifiedCosignPreservesCancellationAsTimeout(t *testing.T) {
	req, _ := qualifiedRequest(t, "release@example.test", "https://issuer.example.test", true)
	ctx, cancel := context.WithCancel(context.Background())
	verifier := NewCosignVerifier("cosign-under-test", RunnerFunc(func(ctx context.Context, _ string, _ []string, _, _ io.Writer) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}))
	_, err := verifier.VerifyDocument(ctx, req)
	if signCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("verification cancellation must remain a retryable timeout: %v", err)
	}
}

func TestQualifiedCosignRejectsVerificationMaterialMutation(t *testing.T) {
	req, _ := qualifiedRequest(t, "release@example.test", "https://issuer.example.test", true)
	runner := RunnerFunc(func(_ context.Context, _ string, args []string, _, _ io.Writer) error {
		bundle := argAfter(t, args, "--bundle")
		if err := os.Chmod(bundle, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bundle, []byte("swapped after verification"), 0o600); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	_, err := NewCosignVerifier("cosign-under-test", runner).VerifyDocument(context.Background(), req)
	if signCode(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("mutated private bundle must fail closed: %v", err)
	}
}

func TestQualifiedCosignRejectsMismatchedTrustedRootPinBeforeRunner(t *testing.T) {
	req, _ := qualifiedRequest(t, "release@example.test", "https://issuer.example.test", true)
	req.TrustedRootDigest = "sha256:" + strings.Repeat("0", 64)
	called := false
	verifier := NewCosignVerifier("cosign-under-test", RunnerFunc(func(context.Context, string, []string, io.Writer, io.Writer) error {
		called = true
		return nil
	}))
	if _, err := verifier.VerifyDocument(context.Background(), req); signCode(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("mismatched root pin must fail unpinned-input: %v", err)
	}
	if called {
		t.Fatal("root mismatch reached verifier subprocess")
	}
}

func TestQualifiedCosignRejectsDetachedCertificateThatDiffersFromBundle(t *testing.T) {
	req, _ := qualifiedRequest(t, "release@example.test", "https://issuer.example.test", true)
	other := testSigningCertificate(t, "other@example.test", "https://issuer.example.test")
	block, _ := pem.Decode(other)
	bundle := []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json","verificationMaterial":{"certificate":{"rawBytes":"` +
		base64.StdEncoding.EncodeToString(block.Bytes) + `"}}}`)
	if err := os.WriteFile(req.Signature.BundlePath, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	verifier := NewCosignVerifier("cosign-under-test", RunnerFunc(func(context.Context, string, []string, io.Writer, io.Writer) error {
		called = true
		return nil
	}))
	if _, err := verifier.VerifyDocument(context.Background(), req); signCode(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("bundle/detached certificate mismatch must fail signature-invalid: %v", err)
	}
	if called {
		t.Fatal("bundle/detached certificate mismatch reached verifier subprocess")
	}
}

func TestQualifiedCosignPreservesOnlineBundleProtocolSemantics(t *testing.T) {
	const identity = "release@example.test"
	const issuer = "https://issuer.example.test"
	for _, test := range []struct {
		name      string
		legacy    bool
		wantFlags []string
		denyFlags []string
	}{
		{name: "v0.3-uses-embedded-certificate", wantFlags: []string{"--bundle", "--new-bundle-format=true"}, denyFlags: []string{"--certificate", "--signature", "--trusted-root"}},
		{name: "legacy-uses-detached-certificate", legacy: true, wantFlags: []string{"--bundle", "--certificate", "--signature"}, denyFlags: []string{"--new-bundle-format=true", "--trusted-root"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			req, _ := qualifiedRequest(t, identity, issuer, false)
			req.Signature.TrustedRootPath = ""
			req.TrustedRootDigest = ""
			if test.legacy {
				if err := os.WriteFile(req.Signature.BundlePath, []byte(`{"bundle":"legacy"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			verifier := NewCosignVerifier("cosign-under-test", RunnerFunc(func(_ context.Context, _ string, args []string, _, _ io.Writer) error {
				for _, flag := range test.wantFlags {
					if !containsArg(args, flag) {
						t.Fatalf("online args missing %s: %v", flag, args)
					}
				}
				for _, flag := range test.denyFlags {
					if containsArg(args, flag) {
						t.Fatalf("online args contain incompatible %s: %v", flag, args)
					}
				}
				return nil
			}))
			if _, err := verifier.VerifyDocument(context.Background(), req); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQualifiedVerifyArgsOfflineRequiresBundleAndRoot(t *testing.T) {
	base := trust.DocumentVerificationRequest{
		SnapshotPath:      "/private/document",
		Signature:         trust.SignatureMaterial{CertificatePath: "/private/certificate"},
		Offline:           true,
		Role:              trust.RoleValidationVector,
		AllowedIdentities: []string{"release@example.test"},
		OIDCIssuer:        "https://issuer.example.test",
	}
	if _, err := QualifiedVerifyBlobArgs(base); signCode(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("missing offline bundle must fail signature gate: %v", err)
	}
	base.Signature.BundlePath = "/private/bundle"
	if _, err := QualifiedVerifyBlobArgs(base); signCode(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("missing offline root must fail unpinned-input: %v", err)
	}
	base.Signature.TrustedRootPath = "/private/root"
	base.TrustedRootDigest = "sha256:" + strings.Repeat("a", 64)
	args, err := QualifiedVerifyBlobArgs(base)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "insecure-ignore") || strings.Contains(joined, "--offline") {
		t.Fatalf("qualified interface must not offer an insecure/unsupported escape: %s", joined)
	}
	if strings.Contains(joined, "--certificate ") || !strings.Contains(joined, "--new-bundle-format=true") {
		t.Fatalf("Sigstore v0.3 args must use the self-contained bundle protocol: %s", joined)
	}
}

func TestQualifiedVerifierEnvironmentRejectsAmbientAuthorityControls(t *testing.T) {
	got := qualifiedCosignEnvironment([]string{
		"PATH=/usr/bin:/bin",
		"LD_PRELOAD=/tmp/attacker.so",
		"DYLD_INSERT_LIBRARIES=/tmp/attacker.dylib",
		"COSIGN_INSECURE_IGNORE_TLOG=true",
		"COSIGN_TRUSTED_ROOT=/tmp/attacker-root.json",
		"SIGSTORE_ROOT_FILE=/tmp/attacker-root.pem",
		"SIGSTORE_REKOR_PUBLIC_KEY=/tmp/attacker-rekor.pub",
		"TUF_ROOT_JSON=/tmp/attacker-tuf.json",
		"SOURCE_DATE_EPOCH=1",
	})
	if strings.Join(got, ",") != "PATH=/usr/bin:/bin" {
		t.Fatalf("qualified verifier inherited ambient authority controls: %v", got)
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func argAfter(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i := range args {
		if args[i] == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	t.Fatalf("flag %s missing from %v", flag, args)
	return ""
}
