package trust

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

const (
	testSchema = "nkd.test/document/v1"
	testSigner = "release@example.test"
	testIssuer = "https://issuer.example.test"
)

type testDocument struct {
	SchemaVersion string   `json:"schemaVersion"`
	Name          string   `json:"name"`
	Items         []string `json:"items,omitempty"`
}

type documentVerifierFunc func(context.Context, DocumentVerificationRequest) (VerificationEvidence, error)

func (f documentVerifierFunc) VerifyDocument(ctx context.Context, req DocumentVerificationRequest) (VerificationEvidence, error) {
	return f(ctx, req)
}

func goodEvidence() VerificationEvidence {
	return VerificationEvidence{
		CertificateIdentities: []string{testSigner},
		OIDCIssuer:            testIssuer,
		CertificateSHA256:     "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
}

func testPolicy(t *testing.T, role SignerRole) RolePolicy {
	t.Helper()
	policy, err := NewRolePolicy(role, Store{OperatorIdentities: []string{testSigner}}, testIssuer)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func testAuthenticator(t *testing.T, verifier DocumentVerifier) *DocumentAuthenticator {
	t.Helper()
	auth, err := NewDocumentAuthenticator(verifier, JSONSchema[testDocument](testSchema))
	if err != nil {
		t.Fatal(err)
	}
	return auth
}

func writeTestDocument(t *testing.T, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "document.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func canonicalTestDocument(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := canonical.Marshal(testDocument{SchemaVersion: testSchema, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAuthenticateRejectsNilOrCanceledContextBeforeFilesystemIO(t *testing.T) {
	calls := 0
	auth := testAuthenticator(t, documentVerifierFunc(func(context.Context, DocumentVerificationRequest) (VerificationEvidence, error) {
		calls++
		return goodEvidence(), nil
	}))
	request := AuthenticateRequest{
		SourcePath: filepath.Join(t.TempDir(), "must-not-be-opened.json"),
		Signature: SignatureMaterial{
			BundlePath:      "must-not-be-opened.bundle",
			TrustedRootPath: "must-not-be-opened.root",
		},
		Offline: true,
		Role:    RoleValidationVector,
		Policy:  testPolicy(t, RoleValidationVector),
	}
	if _, err := auth.Authenticate(nil, request); code(err) != nkderr.CodeUsage { //nolint:staticcheck // intentional nil-context admission test
		t.Fatalf("nil context returned %v, want usage before filesystem I/O", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := auth.Authenticate(ctx, request); code(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled context returned %v, want timeout before filesystem I/O", err)
	}
	if calls != 0 {
		t.Fatalf("invalid context reached verifier %d time(s)", calls)
	}
}

func TestAuthenticateUsesOnePrivateSnapshotWhenSourcePathChanges(t *testing.T) {
	original := canonicalTestDocument(t, "original")
	changed := canonicalTestDocument(t, "changed")
	sourcePath := writeTestDocument(t, original)

	verifier := documentVerifierFunc(func(_ context.Context, req DocumentVerificationRequest) (VerificationEvidence, error) {
		if req.SnapshotPath == sourcePath {
			t.Fatal("verifier received mutable source path instead of a private snapshot")
		}
		got, err := os.ReadFile(req.SnapshotPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, original) {
			t.Fatalf("snapshot changed before verification: %s", got)
		}
		if info, err := os.Stat(req.SnapshotPath); err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("snapshot must be private: info=%v err=%v", info, err)
		}
		if err := os.WriteFile(sourcePath, changed, 0o600); err != nil {
			t.Fatal(err)
		}
		return goodEvidence(), nil
	})
	auth := testAuthenticator(t, verifier)
	receipt, err := auth.Authenticate(context.Background(), AuthenticateRequest{
		SourcePath: sourcePath,
		Role:       RoleValidationVector,
		Policy:     testPolicy(t, RoleValidationVector),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Authenticated() || !bytes.Equal(receipt.ExactBytes(), original) {
		t.Fatalf("receipt did not retain exact authenticated snapshot: %+v", receipt)
	}
	decoded, ok := receipt.Decoded().(testDocument)
	if !ok || decoded.Name != "original" {
		t.Fatalf("decoded value came from changed path: %#v", receipt.Decoded())
	}
	if receipt.SignerIdentity() != testSigner || receipt.Role() != RoleValidationVector {
		t.Fatalf("receipt lost authenticated signer role: signer=%q role=%q", receipt.SignerIdentity(), receipt.Role())
	}
}

func TestAuthenticateRejectsUnsafeOrOversizedSourcePathsBeforeVerifier(t *testing.T) {
	calls := 0
	auth := testAuthenticator(t, documentVerifierFunc(func(context.Context, DocumentVerificationRequest) (VerificationEvidence, error) {
		calls++
		return goodEvidence(), nil
	}))
	tests := []struct {
		name  string
		setup func(*testing.T) string
	}{
		{
			name: "symlink",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				target := filepath.Join(dir, "target.json")
				if err := os.WriteFile(target, canonicalTestDocument(t, "target"), 0o600); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(dir, "document.json")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
		},
		{
			name: "fifo",
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "document.pipe")
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "oversized",
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "document.json")
				file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Truncate(MaxAuthenticatedDocumentBytes + 1); err != nil {
					_ = file.Close()
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := auth.Authenticate(context.Background(), AuthenticateRequest{
				SourcePath: test.setup(t), Role: RoleValidationVector,
				Policy: testPolicy(t, RoleValidationVector),
			})
			if code(err) != nkderr.CodeVerifyFailed {
				t.Fatalf("error = %v, want %s", err, nkderr.CodeVerifyFailed)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("unsafe source path reached verifier %d time(s)", calls)
	}
}

func TestAuthenticateRejectsUnsafeTrustedRootPathsBeforeVerifier(t *testing.T) {
	rootRaw := []byte(`{"root":"operator"}`)
	rootSum := sha256.Sum256(rootRaw)
	rootDigest := "sha256:" + hex.EncodeToString(rootSum[:])
	policy, err := testPolicy(t, RoleValidationVector).WithTrustedRootDigest(rootDigest)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	auth := testAuthenticator(t, documentVerifierFunc(func(context.Context, DocumentVerificationRequest) (VerificationEvidence, error) {
		calls++
		return goodEvidence(), nil
	}))
	tests := []struct {
		name  string
		setup func(*testing.T) string
	}{
		{
			name: "symlink",
			setup: func(t *testing.T) string {
				dir := t.TempDir()
				target := filepath.Join(dir, "root.json")
				if err := os.WriteFile(target, rootRaw, 0o600); err != nil {
					t.Fatal(err)
				}
				link := filepath.Join(dir, "trusted-root.json")
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
				return link
			},
		},
		{
			name: "fifo",
			setup: func(t *testing.T) string {
				path := filepath.Join(t.TempDir(), "trusted-root.pipe")
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := auth.Authenticate(context.Background(), AuthenticateRequest{
				SourcePath: writeTestDocument(t, canonicalTestDocument(t, "offline")),
				Signature:  SignatureMaterial{BundlePath: "bundle.sigstore", TrustedRootPath: test.setup(t)},
				Offline:    true, Role: RoleValidationVector, Policy: policy,
			})
			if code(err) != nkderr.CodeUnpinnedInput {
				t.Fatalf("error = %v, want %s", err, nkderr.CodeUnpinnedInput)
			}
		})
	}
	if calls != 0 {
		t.Fatalf("unsafe trusted-root path reached verifier %d time(s)", calls)
	}
}

func TestAuthenticateRejectsPrivateSnapshotMutation(t *testing.T) {
	sourcePath := writeTestDocument(t, canonicalTestDocument(t, "original"))
	verifier := documentVerifierFunc(func(_ context.Context, req DocumentVerificationRequest) (VerificationEvidence, error) {
		if err := os.Chmod(req.SnapshotPath, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(req.SnapshotPath, canonicalTestDocument(t, "attacker"), 0o600); err != nil {
			t.Fatal(err)
		}
		return goodEvidence(), nil
	})
	auth := testAuthenticator(t, verifier)
	_, err := auth.Authenticate(context.Background(), AuthenticateRequest{
		SourcePath: sourcePath, Role: RoleValidationVector, Policy: testPolicy(t, RoleValidationVector),
	})
	if code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("mutated verified snapshot must fail signature gate, got %v", err)
	}
}

func TestAuthenticateRejectsPrivateSnapshotReplacementWithNonRegularPath(t *testing.T) {
	tests := []struct {
		name    string
		replace func(*testing.T, string)
	}{
		{
			name: "symlink",
			replace: func(t *testing.T, path string) {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("attacker", path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "fifo",
			replace: func(t *testing.T, path string) {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(path, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			auth := testAuthenticator(t, documentVerifierFunc(func(_ context.Context, req DocumentVerificationRequest) (VerificationEvidence, error) {
				test.replace(t, req.SnapshotPath)
				return goodEvidence(), nil
			}))
			_, err := auth.Authenticate(context.Background(), AuthenticateRequest{
				SourcePath: writeTestDocument(t, canonicalTestDocument(t, "original")),
				Role:       RoleValidationVector, Policy: testPolicy(t, RoleValidationVector),
			})
			if code(err) != nkderr.CodeSignatureInvalid {
				t.Fatalf("error = %v, want %s", err, nkderr.CodeSignatureInvalid)
			}
		})
	}
}

func TestAuthenticateRejectsNonCanonicalUnknownDuplicateAndUnknownSchema(t *testing.T) {
	unknownField, err := canonical.Marshal(map[string]any{
		"schemaVersion": testSchema, "name": "ok", "surprise": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	unknownSchema, err := canonical.Marshal(map[string]any{
		"schemaVersion": "nkd.test/unknown/v1", "name": "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "noncanonical", raw: []byte(`{"schemaVersion":"nkd.test/document/v1", "name":"ok"}`)},
		{name: "unknown-field", raw: unknownField},
		{name: "duplicate-field", raw: []byte(`{"name":"first","name":"second","schemaVersion":"nkd.test/document/v1"}`)},
		{name: "unknown-schema", raw: unknownSchema},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			verifier := documentVerifierFunc(func(context.Context, DocumentVerificationRequest) (VerificationEvidence, error) {
				calls++
				return goodEvidence(), nil
			})
			auth := testAuthenticator(t, verifier)
			_, err := auth.Authenticate(context.Background(), AuthenticateRequest{
				SourcePath: writeTestDocument(t, tc.raw),
				Role:       RoleValidationVector,
				Policy:     testPolicy(t, RoleValidationVector),
			})
			if code(err) != nkderr.CodeVerifyFailed {
				t.Fatalf("invalid authenticated JSON must fail closed, got %v", err)
			}
			if calls != 1 {
				t.Fatalf("signature verification must precede parsing; calls=%d", calls)
			}
		})
	}
}

func TestAuthenticateOfflineRequiresBundleAndExplicitRootBeforeVerifier(t *testing.T) {
	calls := 0
	verifier := documentVerifierFunc(func(context.Context, DocumentVerificationRequest) (VerificationEvidence, error) {
		calls++
		return goodEvidence(), nil
	})
	auth := testAuthenticator(t, verifier)
	base := AuthenticateRequest{
		SourcePath: writeTestDocument(t, canonicalTestDocument(t, "offline")),
		Offline:    true,
		Role:       RoleValidationVector,
		Policy:     testPolicy(t, RoleValidationVector),
	}
	if _, err := auth.Authenticate(context.Background(), base); code(err) != nkderr.CodeSignatureInvalid {
		t.Fatalf("offline without bundle must fail signature gate, got %v", err)
	}
	base.Signature.BundlePath = "bundle.sigstore"
	if _, err := auth.Authenticate(context.Background(), base); code(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("offline without root policy pin must fail unpinned-input, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("invalid offline request reached verifier %d time(s)", calls)
	}
}

func TestAuthenticateBindsOfflineRootToImmutableRolePolicy(t *testing.T) {
	rootRaw := []byte(`{"root":"operator"}`)
	rootPath := filepath.Join(t.TempDir(), "trusted-root.json")
	if err := os.WriteFile(rootPath, rootRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	rootSum := sha256.Sum256(rootRaw)
	rootDigest := "sha256:" + hex.EncodeToString(rootSum[:])
	calls := 0
	auth := testAuthenticator(t, documentVerifierFunc(func(_ context.Context, req DocumentVerificationRequest) (VerificationEvidence, error) {
		calls++
		if req.TrustedRootDigest != rootDigest {
			t.Fatalf("verifier root pin=%q want=%q", req.TrustedRootDigest, rootDigest)
		}
		return goodEvidence(), nil
	}))
	request := AuthenticateRequest{
		SourcePath: writeTestDocument(t, canonicalTestDocument(t, "offline-root")),
		Signature:  SignatureMaterial{BundlePath: "bundle.sigstore", TrustedRootPath: rootPath},
		Offline:    true, Role: RoleValidationVector, Policy: testPolicy(t, RoleValidationVector),
	}
	if _, err := auth.Authenticate(context.Background(), request); code(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("path-only trusted root must fail closed: %v", err)
	}
	wrongPolicy, err := request.Policy.WithTrustedRootDigest("sha256:" + strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	request.Policy = wrongPolicy
	if _, err := auth.Authenticate(context.Background(), request); code(err) != nkderr.CodeUnpinnedInput {
		t.Fatalf("mismatched root pin must fail closed: %v", err)
	}
	pinnedPolicy, err := testPolicy(t, RoleValidationVector).WithTrustedRootDigest(rootDigest)
	if err != nil {
		t.Fatal(err)
	}
	request.Policy = pinnedPolicy
	receipt, err := auth.Authenticate(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.TrustedRootSHA256() != rootDigest {
		t.Fatalf("receipt root pin=%q", receipt.TrustedRootSHA256())
	}
	if calls != 1 {
		t.Fatalf("verifier calls=%d want=1", calls)
	}
}

func TestAuthenticateAuthorizesActualSignerAndRole(t *testing.T) {
	sourcePath := writeTestDocument(t, canonicalTestDocument(t, "policy"))
	tests := []struct {
		name     string
		role     SignerRole
		policy   RolePolicy
		evidence VerificationEvidence
		want     nkderr.Code
	}{
		{
			name: "wrong-vector-signer", role: RoleValidationVector,
			policy:   testPolicy(t, RoleValidationVector),
			evidence: VerificationEvidence{CertificateIdentities: []string{"attacker@example.test"}, OIDCIssuer: testIssuer, CertificateSHA256: goodEvidence().CertificateSHA256},
			want:     nkderr.CodeUntrustedSigner,
		},
		{
			name: "wrong-profile-signer", role: RoleBuilderProfile,
			policy:   testPolicy(t, RoleBuilderProfile),
			evidence: VerificationEvidence{CertificateIdentities: []string{"attacker@example.test"}, OIDCIssuer: testIssuer, CertificateSHA256: goodEvidence().CertificateSHA256},
			want:     nkderr.CodeUntrustedProfile,
		},
		{
			name: "wrong-issuer", role: RoleStatus,
			policy:   testPolicy(t, RoleStatus),
			evidence: VerificationEvidence{CertificateIdentities: []string{testSigner}, OIDCIssuer: "https://attacker.example", CertificateSHA256: goodEvidence().CertificateSHA256},
			want:     nkderr.CodeUntrustedSigner,
		},
		{
			name: "noncanonical-certificate-digest", role: RoleStatus,
			policy: testPolicy(t, RoleStatus),
			evidence: VerificationEvidence{CertificateIdentities: []string{testSigner}, OIDCIssuer: testIssuer,
				CertificateSHA256: "sha256:" + strings.Repeat("A", 64)},
			want: nkderr.CodeSignatureInvalid,
		},
		{
			name: "cross-role-policy", role: RoleConformance,
			policy: testPolicy(t, RoleValidationVector), evidence: goodEvidence(),
			want: nkderr.CodeTrustPolicyMissing,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auth := testAuthenticator(t, documentVerifierFunc(func(context.Context, DocumentVerificationRequest) (VerificationEvidence, error) {
				return tc.evidence, nil
			}))
			_, err := auth.Authenticate(context.Background(), AuthenticateRequest{
				SourcePath: sourcePath, Role: tc.role, Policy: tc.policy,
			})
			if code(err) != tc.want {
				t.Fatalf("authorization code=%q want=%q: %v", code(err), tc.want, err)
			}
		})
	}
}

func TestDocumentReceiptZeroValueCannotClaimAuthentication(t *testing.T) {
	var receipt DocumentReceipt
	if receipt.Authenticated() || receipt.SignerIdentity() != "" || len(receipt.ExactBytes()) != 0 {
		t.Fatalf("zero receipt must not be usable as authenticated: %+v", receipt)
	}
}

func TestDocumentReceiptDecodedReturnsMutationIsolatedValue(t *testing.T) {
	raw, err := canonical.Marshal(testDocument{
		SchemaVersion: testSchema, Name: "immutable", Items: []string{"first", "second"},
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := testAuthenticator(t, documentVerifierFunc(func(context.Context, DocumentVerificationRequest) (VerificationEvidence, error) {
		return goodEvidence(), nil
	}))
	receipt, err := auth.Authenticate(context.Background(), AuthenticateRequest{
		SourcePath: writeTestDocument(t, raw), Role: RolePromotionPolicy,
		Policy: testPolicy(t, RolePromotionPolicy),
	})
	if err != nil {
		t.Fatal(err)
	}
	first, ok := receipt.Decoded().(testDocument)
	if !ok {
		t.Fatalf("unexpected decoded type: %T", receipt.Decoded())
	}
	first.Name = "attacker"
	first.Items[0] = "mutated"
	second, ok := receipt.Decoded().(testDocument)
	if !ok || second.Name != "immutable" || len(second.Items) != 2 || second.Items[0] != "first" {
		t.Fatalf("decoded mutation leaked into receipt capability: %#v", second)
	}
}
