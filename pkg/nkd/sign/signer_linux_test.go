//go:build linux

package sign

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	heldSignSignature = "held-detached-signature"
	heldSignBundle    = `{"bundle":"held-fixture"}`
)

func runHeldCosignSignHelper() int {
	for _, name := range []string{
		"LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "COSIGN_INSECURE_IGNORE_TLOG",
		"SIGSTORE_ROOT_FILE", "TUF_ROOT_JSON", "SOURCE_DATE_EPOCH",
	} {
		if os.Getenv(name) != "" {
			return 41
		}
	}
	if !strings.HasPrefix(os.Args[0], "/proc/self/fd/") {
		return 42
	}
	args := os.Args[1:]
	if len(args) == 0 || args[len(args)-1] != "-" {
		return 43
	}
	if processArgAfter(args, "--oidc-issuer") != "https://issuer.example.test" {
		return 55
	}
	for _, flag := range []string{"--output-signature", "--output-certificate"} {
		path := processArgAfter(args, flag)
		if path == "" || !strings.HasPrefix(path, "/proc/self/fd/") {
			return 44
		}
	}
	bundlePath := processArgAfter(args, "--bundle")
	if bundlePath != "" && !strings.HasPrefix(bundlePath, "/proc/self/fd/") {
		return 45
	}
	if err := os.WriteFile(os.Getenv("NKD_COSIGN_SIGN_READY"), []byte("ready"), 0o600); err != nil {
		return 46
	}
	if !waitForTestPath(os.Getenv("NKD_COSIGN_SIGN_CONTINUE")) {
		return 47
	}
	document, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 48
	}
	if err := os.WriteFile(os.Getenv("NKD_COSIGN_SIGN_CAPTURED"), document, 0o600); err != nil {
		return 49
	}
	certificate, err := base64.StdEncoding.DecodeString(os.Getenv("NKD_COSIGN_SIGN_CERTIFICATE"))
	if err != nil {
		return 50
	}
	if err := os.WriteFile(processArgAfter(args, "--output-signature"), []byte(heldSignSignature), 0o600); err != nil {
		return 51
	}
	if err := os.WriteFile(processArgAfter(args, "--output-certificate"), certificate, 0o600); err != nil {
		return 52
	}
	if bundlePath != "" {
		if err := os.WriteFile(bundlePath, []byte(heldSignBundle), 0o600); err != nil {
			return 53
		}
	}
	if !waitForTestPath(os.Getenv("NKD_COSIGN_SIGN_FINISH")) {
		return 54
	}
	return 0
}

func newPinnedHeldSignerForTest(t *testing.T) (*CosignSigner, string) {
	t.Helper()
	binaryBytes := heldCosignHelperBytes(t)
	binary := filepath.Join(t.TempDir(), "cosign")
	if err := os.WriteFile(binary, binaryBytes, 0o700); err != nil {
		t.Fatal(err)
	}
	binarySum := sha256.Sum256(binaryBytes)
	signer, err := NewPinnedCosignSigner(binary, "sha256:"+hex.EncodeToString(binarySum[:]))
	if err != nil {
		t.Fatal(err)
	}
	return signer, binary
}

func TestProductionSignerConsumesHeldBytesAndCapturesAnonymousOutputsDuringRenameRestore(t *testing.T) {
	const identity = "publisher@example.test"
	const issuer = "https://issuer.example.test"
	document := []byte(`{"kind":"nkd.validation-vector","schemaVersion":"v1"}`)
	wantDocument := append([]byte(nil), document...)
	certificate := testSigningCertificate(t, identity, issuer)

	signer, binary := newPinnedHeldSignerForTest(t)

	coordination := t.TempDir()
	ready := filepath.Join(coordination, "ready")
	continuePath := filepath.Join(coordination, "continue")
	captured := filepath.Join(coordination, "captured")
	finish := filepath.Join(coordination, "finish")
	t.Setenv("NKD_COSIGN_SIGN_HELD_HELPER", "1")
	t.Setenv("NKD_COSIGN_SIGN_READY", ready)
	t.Setenv("NKD_COSIGN_SIGN_CONTINUE", continuePath)
	t.Setenv("NKD_COSIGN_SIGN_CAPTURED", captured)
	t.Setenv("NKD_COSIGN_SIGN_FINISH", finish)
	t.Setenv("NKD_COSIGN_SIGN_CERTIFICATE", base64.StdEncoding.EncodeToString(certificate))
	t.Setenv("LD_PRELOAD", "/tmp/attacker.so")
	t.Setenv("DYLD_INSERT_LIBRARIES", "/tmp/attacker.dylib")
	t.Setenv("COSIGN_INSECURE_IGNORE_TLOG", "true")
	t.Setenv("SIGSTORE_ROOT_FILE", "/tmp/attacker-root.pem")
	t.Setenv("TUF_ROOT_JSON", "/tmp/attacker-tuf.json")
	t.Setenv("SOURCE_DATE_EPOCH", "1")

	attackErr := make(chan error, 1)
	go func() {
		if !waitForTestPath(ready) {
			attackErr <- fmt.Errorf("held signer never became ready")
			return
		}
		if err := os.Rename(binary, binary+".valid"); err != nil {
			attackErr <- err
			return
		}
		if err := os.WriteFile(binary, []byte("attacker"), 0o700); err != nil {
			attackErr <- err
			return
		}
		if err := os.WriteFile(continuePath, []byte("continue"), 0o600); err != nil {
			attackErr <- err
			return
		}
		if !waitForTestPath(captured) {
			attackErr <- fmt.Errorf("held signer never captured stdin")
			return
		}
		if err := os.Remove(binary); err != nil {
			attackErr <- err
			return
		}
		if err := os.Rename(binary+".valid", binary); err != nil {
			attackErr <- err
			return
		}
		if err := os.WriteFile(finish, []byte("finish"), 0o600); err != nil {
			attackErr <- err
			return
		}
		attackErr <- nil
	}()

	signed, signErr := signer.SignDocument(context.Background(), document, QualifiedSignOptions{
		Bundle: true, ExpectedIdentity: identity, ExpectedIssuer: issuer,
	})
	if attack := <-attackErr; attack != nil {
		t.Fatal(attack)
	}
	if signErr != nil {
		t.Fatal(signErr)
	}
	// The caller owns its input after SignDocument returns. Keep it stable
	// during the call, then prove the receipt retains the original bytes.
	for i := range document {
		document[i] = 'x'
	}
	if err := signed.RequireFor(context.Background(), wantDocument); err != nil {
		t.Fatal(err)
	}
	gotDocument, err := os.ReadFile(captured)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotDocument, wantDocument) {
		t.Fatalf("cosign consumed %q instead of original input bytes", gotDocument)
	}
	signature, err := signed.Signature()
	if err != nil || string(signature) != heldSignSignature {
		t.Fatalf("signature=(%q,%v)", signature, err)
	}
	gotCertificate, err := signed.Certificate()
	if err != nil || !bytes.Equal(gotCertificate, certificate) {
		t.Fatalf("certificate differs from exact descriptor output: %v", err)
	}
	bundle, err := signed.Bundle()
	if err != nil || string(bundle) != heldSignBundle {
		t.Fatalf("bundle=(%q,%v)", bundle, err)
	}
}

func TestProductionSignerCancellationFailsClosed(t *testing.T) {
	signer, _ := newPinnedHeldSignerForTest(t)
	coordination := t.TempDir()
	ready := filepath.Join(coordination, "ready")
	t.Setenv("NKD_COSIGN_SIGN_HELD_HELPER", "1")
	t.Setenv("NKD_COSIGN_SIGN_READY", ready)
	t.Setenv("NKD_COSIGN_SIGN_CONTINUE", filepath.Join(coordination, "never"))
	t.Setenv("NKD_COSIGN_SIGN_CAPTURED", filepath.Join(coordination, "captured"))
	t.Setenv("NKD_COSIGN_SIGN_FINISH", filepath.Join(coordination, "finish"))
	t.Setenv("NKD_COSIGN_SIGN_CERTIFICATE", base64.StdEncoding.EncodeToString(
		testSigningCertificate(t, "publisher@example.test", "https://issuer.example.test")))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	readyResult := make(chan error, 1)
	go func() {
		if !waitForTestPath(ready) {
			readyResult <- fmt.Errorf("held signer never became ready")
			return
		}
		cancel()
		readyResult <- nil
	}()
	_, err := signer.SignDocument(ctx, []byte(`{"cancel":true}`), QualifiedSignOptions{
		ExpectedIdentity: "publisher@example.test", ExpectedIssuer: "https://issuer.example.test",
	})
	if readyErr := <-readyResult; readyErr != nil {
		t.Fatal(readyErr)
	}
	if signCode(err) != "NKD-E-TIMEOUT" {
		t.Fatalf("production cancellation must retain timeout semantics: %v", err)
	}
}
