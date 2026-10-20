// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSigningOutputs(t *testing.T, args []string, certificate []byte) {
	t.Helper()
	// Cosign 3 defaults to signing-config service selection and bundle-only
	// output. The detached protocol must explicitly select compatible options.
	for _, flag := range []string{"--use-signing-config=false", "--new-bundle-format=false", "--b64=false", "--tlog-upload=true"} {
		if !containsArg(args, flag) {
			t.Fatalf("detached signing protocol missing %s: %v", flag, args)
		}
	}
	if err := os.WriteFile(argAfter(t, args, "--output-signature"), []byte("detached-signature"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(argAfter(t, args, "--output-certificate"), certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	if containsArg(args, "--bundle") {
		if err := os.WriteFile(argAfter(t, args, "--bundle"), []byte(`{"bundle":"fixture"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestQualifiedSignerConsumesExactPrivateBytesAndCapturesOutputs(t *testing.T) {
	const identity = "publisher@example.test"
	const issuer = "https://issuer.example.test"
	document := []byte(`{"kind":"nvkr.validation-vector","schemaVersion":"v1"}`)
	certificate := testSigningCertificate(t, identity, issuer)
	var snapshotPath string
	runner := RunnerFunc(func(_ context.Context, binary string, args []string, _, _ io.Writer) error {
		if binary != "cosign-under-test" {
			t.Fatalf("binary=%q", binary)
		}
		snapshotPath = args[len(args)-1]
		got, err := os.ReadFile(snapshotPath)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, document) {
			t.Fatalf("cosign did not receive exact bytes: %q", got)
		}
		info, err := os.Stat(snapshotPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o400 {
			t.Fatalf("snapshot mode=%o, want 0400", info.Mode().Perm())
		}
		for _, flag := range []string{"--output-signature", "--output-certificate", "--bundle"} {
			if !strings.HasPrefix(argAfter(t, args, flag), filepath.Dir(snapshotPath)+string(os.PathSeparator)) {
				t.Fatalf("%s output escaped private directory", flag)
			}
		}
		writeSigningOutputs(t, args, certificate)
		return nil
	})
	signed, err := newCosignSigner("cosign-under-test", runner).SignDocument(
		context.Background(), document, QualifiedSignOptions{Bundle: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := signed.RequireFor(context.Background(), document); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(snapshotPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private signing directory was not cleaned up: %v", err)
	}
	if len(signed.identities) != 1 || signed.identities[0] != identity || signed.issuer != issuer {
		t.Fatalf("claims=(%v,%q)", signed.identities, signed.issuer)
	}
	bundle, err := signed.Bundle()
	if err != nil || !bytes.Contains(bundle, []byte("fixture")) {
		t.Fatalf("bundle=(%q,%v)", bundle, err)
	}
	if err := signed.RequireFor(context.Background(), append(append([]byte(nil), document...), ' ')); signCode(err) != "NVKR-E-SIGNATURE-INVALID" {
		t.Fatalf("receipt accepted different publication bytes: %v", err)
	}

	sig, err := signed.Signature()
	if err != nil {
		t.Fatal(err)
	}
	sig[0] ^= 0xff
	again, err := signed.Signature()
	if err != nil || bytes.Equal(sig, again) {
		t.Fatalf("signature accessor did not return a defensive copy")
	}
}

func TestQualifiedSignerRejectsPrivateDocumentMutation(t *testing.T) {
	document := []byte(`{"signed":true}`)
	certificate := testSigningCertificate(t, "publisher@example.test", "https://issuer.example.test")
	runner := RunnerFunc(func(_ context.Context, _ string, args []string, _, _ io.Writer) error {
		writeSigningOutputs(t, args, certificate)
		path := args[len(args)-1]
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		return os.WriteFile(path, []byte(`{"signed":false}`), 0o600)
	})
	_, err := newCosignSigner("cosign-under-test", runner).SignDocument(
		context.Background(), document, QualifiedSignOptions{})
	if signCode(err) != "NVKR-E-SIGNATURE-INVALID" {
		t.Fatalf("mutated signing input must fail closed: %v", err)
	}
}

func TestQualifiedSignerAllowsAnExplicitlyAbsentBundle(t *testing.T) {
	document := []byte(`{"signed":true}`)
	certificate := testSigningCertificate(t, "publisher@example.test", "https://issuer.example.test")
	runner := RunnerFunc(func(_ context.Context, _ string, args []string, _, _ io.Writer) error {
		if containsArg(args, "--bundle") {
			t.Fatal("bundle output was requested when QualifiedSignOptions.Bundle was false")
		}
		writeSigningOutputs(t, args, certificate)
		return nil
	})
	signed, err := newCosignSigner("cosign-under-test", runner).SignDocument(
		context.Background(), document, QualifiedSignOptions{})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := signed.Bundle()
	if err != nil || bundle != nil {
		t.Fatalf("optional bundle=(%q,%v), want nil", bundle, err)
	}
}

func TestQualifiedSignerBindsExpectedIdentityAndIssuer(t *testing.T) {
	const issuer = "https://issuer.example.test"
	document := []byte(`{"signed":true}`)
	certificate := testSigningCertificate(t, "other@example.test", issuer)
	runner := RunnerFunc(func(_ context.Context, _ string, args []string, _, _ io.Writer) error {
		if got := argAfter(t, args, "--oidc-issuer"); got != issuer {
			t.Fatalf("signing issuer argument = %q, want %q", got, issuer)
		}
		writeSigningOutputs(t, args, certificate)
		return nil
	})
	_, err := newCosignSigner("cosign-under-test", runner).SignDocument(
		context.Background(), document, QualifiedSignOptions{
			ExpectedIdentity: "publisher@example.test", ExpectedIssuer: issuer,
		})
	if signCode(err) != "NVKR-E-UNTRUSTED-SIGNER" {
		t.Fatalf("unexpected certificate identity was accepted: %v", err)
	}
}

func TestPinnedQualifiedSignerRequiresExpectedClaimsBeforeRunner(t *testing.T) {
	called := false
	runner := RunnerFunc(func(context.Context, string, []string, io.Writer, io.Writer) error {
		called = true
		return nil
	})
	signer, err := newPinnedCosignSigner("cosign-under-test",
		"sha256:"+strings.Repeat("a", 64), runner)
	if err != nil {
		t.Fatal(err)
	}
	_, err = signer.SignDocument(context.Background(), []byte(`{"signed":true}`), QualifiedSignOptions{})
	if signCode(err) != "NVKR-E-TRUST-POLICY-MISSING" {
		t.Fatalf("missing expected signing claims code = %q: %v", signCode(err), err)
	}
	if called {
		t.Fatal("missing expected signing claims reached the runner")
	}
}

func TestQualifiedSignerRejectsSymlinkOutput(t *testing.T) {
	certificate := testSigningCertificate(t, "publisher@example.test", "https://issuer.example.test")
	runner := RunnerFunc(func(_ context.Context, _ string, args []string, _, _ io.Writer) error {
		target := filepath.Join(t.TempDir(), "target")
		if err := os.WriteFile(target, []byte("detached-signature"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, argAfter(t, args, "--output-signature")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(argAfter(t, args, "--output-certificate"), certificate, 0o600); err != nil {
			t.Fatal(err)
		}
		return nil
	})
	_, err := newCosignSigner("cosign-under-test", runner).SignDocument(
		context.Background(), []byte(`{"signed":true}`), QualifiedSignOptions{})
	if signCode(err) != "NVKR-E-SIGNATURE-INVALID" {
		t.Fatalf("symlinked signing output must fail closed: %v", err)
	}
}

func TestQualifiedSignerPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := RunnerFunc(func(ctx context.Context, _ string, _ []string, _, _ io.Writer) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	})
	_, err := newCosignSigner("cosign-under-test", runner).SignDocument(
		ctx, []byte(`{"signed":true}`), QualifiedSignOptions{})
	if signCode(err) != "NVKR-E-TIMEOUT" {
		t.Fatalf("cancellation must retain timeout semantics: %v", err)
	}
}

func TestPinnedQualifiedSignerRejectsMalformedExecutableDigest(t *testing.T) {
	for _, digest := range []string{"", "sha256:ABC", "sha256:" + strings.Repeat("A", 64)} {
		if _, err := NewPinnedCosignSigner("/does/not/exist/cosign", digest); signCode(err) != "NVKR-E-UNPINNED-INPUT" {
			t.Fatalf("digest %q did not fail closed as unpinned input: %v", digest, err)
		}
	}
}

func TestZeroSignedDocumentFailsClosed(t *testing.T) {
	var signed SignedDocument
	if err := signed.RequireFor(context.Background(), []byte("anything")); signCode(err) != "NVKR-E-SIGNATURE-INVALID" {
		t.Fatalf("zero signed document must fail closed: %v", err)
	}
}

func TestQualifiedSigningArgumentContract(t *testing.T) {
	for _, issuer := range []string{"", "https://issuer.example.test"} {
		for _, bundle := range []string{"", "/private/bundle.json"} {
			t.Run(issuer+bundle, func(t *testing.T) {
				args := qualifiedSignBlobArgs("/private/document", "/private/signature", "/private/certificate", bundle, issuer)
				if args[0] != "sign-blob" || args[len(args)-1] != "/private/document" {
					t.Fatal("wrong command or subject")
				}
				expected := map[string]string{"--yes": "", "--use-signing-config=false": "", "--new-bundle-format=false": "", "--b64=false": "", "--tlog-upload=true": "", "--output-signature": "/private/signature", "--output-certificate": "/private/certificate"}
				if issuer != "" {
					expected["--oidc-issuer"] = issuer
				}
				if bundle != "" {
					expected["--bundle"] = bundle
				}
				for i := 1; i < len(args)-1; i++ {
					value, ok := expected[args[i]]
					if !ok {
						t.Fatalf("unexpected or duplicate argument %s", args[i])
					}
					delete(expected, args[i])
					if value != "" {
						i++
						if i >= len(args)-1 || args[i] != value {
							t.Fatal("wrong option value")
						}
					}
				}
				if len(expected) != 0 {
					t.Fatalf("missing options: %v", expected)
				}
			})
		}
	}
}
