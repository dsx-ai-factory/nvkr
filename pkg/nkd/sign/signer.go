// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

// DefaultBinary is the cosign executable name.
const DefaultBinary = "cosign"

const maxSignedDocumentBytes int64 = maxVerificationMaterialBytes

// QualifiedSignOptions describes the outputs cosign must produce for an
// exact-byte signing operation. A bundle is optional for online verification,
// but should be requested whenever the signature must be mirrorable or verified
// without discovering transparency-log material later.
type QualifiedSignOptions struct {
	Bundle           bool
	ExpectedIdentity string
	ExpectedIssuer   string
}

// SignedDocument is the result of one successful exact-byte cosign invocation.
// Its fields are intentionally private: callers can publish defensive copies of
// the captured output bytes, but cannot manufacture a successful signing result.
//
// SignedDocument proves which bytes the subprocess consumed; it is not a trust
// decision. A consumer must still authenticate the signature under the
// appropriate role policy before granting authority.
type SignedDocument struct {
	documentDigest string
	signature      []byte
	certificate    []byte
	bundle         []byte
	identities     []string
	issuer         string
	receipt        *signingReceipt
}

type signingReceipt struct{}

// DocumentDigest returns the SHA-256 identity of the exact held document bytes
// transmitted to cosign.
func (s SignedDocument) DocumentDigest() (string, error) {
	if err := s.requireReceipt(); err != nil {
		return "", err
	}
	return s.documentDigest, nil
}

// Signature returns a defensive copy of the detached signature bytes.
func (s SignedDocument) Signature() ([]byte, error) {
	if err := s.requireReceipt(); err != nil {
		return nil, err
	}
	return append([]byte(nil), s.signature...), nil
}

// Certificate returns a defensive copy of the exact Fulcio certificate bytes
// emitted by the successful cosign process.
func (s SignedDocument) Certificate() ([]byte, error) {
	if err := s.requireReceipt(); err != nil {
		return nil, err
	}
	return append([]byte(nil), s.certificate...), nil
}

// Bundle returns a defensive copy of the Sigstore bundle, or nil when the
// caller did not request one.
func (s SignedDocument) Bundle() ([]byte, error) {
	if err := s.requireReceipt(); err != nil {
		return nil, err
	}
	return append([]byte(nil), s.bundle...), nil
}

// RequireFor rebinds this receipt to exact live document bytes before a caller
// publishes the captured signature material.
func (s SignedDocument) RequireFor(ctx context.Context, document []byte) error {
	if err := s.requireReceipt(); err != nil {
		return err
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if digestBytes(document) != s.documentDigest {
		return e.New(e.CodeSignatureInvalid,
			"signed-document receipt does not match the bytes being published")
	}
	return nil
}

func (s SignedDocument) requireReceipt() error {
	if s.receipt == nil || s.documentDigest == "" || len(s.signature) == 0 || len(s.certificate) == 0 {
		return e.New(e.CodeSignatureInvalid, "signed document is not qualified")
	}
	return nil
}

// CosignSigner is the exact-byte signing adapter. The stock, pinned adapter uses
// only a held executable, stdin, and anonymous output descriptors. An explicitly
// injected runner is diagnostic infrastructure in the caller's TCB and retains a
// private-path protocol for tests that cannot execute Linux capabilities.
type CosignSigner struct {
	binary               string
	runner               CommandRunner
	expectedBinaryDigest string
	requiresBinaryPin    bool
}

func newCosignSigner(binary string, runner CommandRunner) *CosignSigner {
	if strings.TrimSpace(binary) == "" {
		binary = DefaultBinary
	}
	return &CosignSigner{binary: binary, runner: runner,
		requiresBinaryPin: isExecRunner(runner)}
}

// NewPinnedCosignSigner constructs the stock qualified signer. Unsupported
// platforms fail here, before document bytes or output destinations are touched.
// Linux resolves and captures the exact static executable immediately before use.
func NewPinnedCosignSigner(binary, expectedDigest string) (*CosignSigner, error) {
	return newPinnedCosignSigner(binary, expectedDigest, ExecRunner{})
}

func newPinnedCosignSigner(binary, expectedDigest string, runner CommandRunner) (*CosignSigner, error) {
	if strings.TrimSpace(binary) == "" {
		binary = DefaultBinary
	}
	if runner == nil {
		return nil, e.New(e.CodeInternal, "qualified cosign signer runner is nil")
	}
	if !canonical.IsDigest(expectedDigest) {
		return nil, e.New(e.CodeUnpinnedInput,
			"qualified cosign signer requires an exact lowercase sha256 binary digest")
	}
	if isExecRunner(runner) {
		if err := heldfile.RequireExecutableSupport(); err != nil {
			return nil, e.Wrap(e.CodeEngineCapability, err,
				"qualified cosign signer requires Linux anonymous descriptor execution")
		}
	}
	return &CosignSigner{binary: binary, runner: runner,
		expectedBinaryDigest: expectedDigest, requiresBinaryPin: true}, nil
}

// Preflight proves that the configured qualified signer executable is available
// at its exact pin before a caller begins consequential producer work. The actual
// signing operation captures and verifies a fresh held capability immediately
// before use, so a post-preflight replacement can only make signing fail closed.
func (s *CosignSigner) Preflight(ctx context.Context) error {
	if s == nil || s.runner == nil {
		return e.New(e.CodeInternal, "qualified cosign signer is not initialized")
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if !s.requiresBinaryPin || s.expectedBinaryDigest == "" || !isExecRunner(s.runner) {
		return e.New(e.CodeUnpinnedInput,
			"qualified signer preflight requires the stock exact-pinned executable adapter")
	}
	executable, cleanup, err := snapshotPinnedCosignExecutableHeldContext(ctx, s.binary, s.expectedBinaryDigest)
	if err != nil {
		return err
	}
	defer cleanup()
	if err := executable.AssertUnchangedContext(ctx); err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return ctxErr
		}
		return e.Wrap(e.CodeEngineCapability, err,
			"qualified cosign signer changed during preflight")
	}
	return nil
}

// SignDocument signs the exact bytes supplied by the caller. It never asks
// cosign to reopen a caller-controlled pathname and it never lets cosign write
// directly to a public destination.
func (s *CosignSigner) SignDocument(ctx context.Context, document []byte, opts QualifiedSignOptions) (SignedDocument, error) {
	if s == nil || s.runner == nil {
		return SignedDocument{}, e.New(e.CodeInternal, "qualified cosign signer is not initialized")
	}
	if err := contextError(ctx); err != nil {
		return SignedDocument{}, err
	}
	if len(document) == 0 {
		return SignedDocument{}, e.New(e.CodeSignatureInvalid, "refusing to sign an empty document")
	}
	if int64(len(document)) > maxSignedDocumentBytes {
		return SignedDocument{}, e.New(e.CodeSignatureInvalid,
			"document exceeds the qualified-signing size limit")
	}
	opts.ExpectedIdentity = strings.TrimSpace(opts.ExpectedIdentity)
	opts.ExpectedIssuer = strings.TrimSpace(opts.ExpectedIssuer)
	if s.requiresBinaryPin && s.expectedBinaryDigest == "" {
		return SignedDocument{}, e.New(e.CodeUnpinnedInput,
			"qualified cosign signing refuses an ambient unpinned executable; configure its exact sha256 digest")
	}
	if s.requiresBinaryPin && (opts.ExpectedIdentity == "" || opts.ExpectedIssuer == "") {
		return SignedDocument{}, e.New(e.CodeTrustPolicyMissing,
			"qualified keyless signing requires an expected certificate identity and OIDC issuer")
	}
	exactDocument := append([]byte(nil), document...)
	var signed SignedDocument
	var err error
	if isExecRunner(s.runner) {
		signed, err = s.signDocumentHeld(ctx, exactDocument, opts)
	} else {
		signed, err = s.signDocumentInjected(ctx, exactDocument, opts)
	}
	if err != nil {
		return SignedDocument{}, err
	}
	if err := signed.requireExpectedClaims(opts.ExpectedIdentity, opts.ExpectedIssuer); err != nil {
		return SignedDocument{}, err
	}
	return signed, nil
}

// signDocumentInjected is a diagnostic compatibility protocol. The injected
// runner is trusted to honor its private path arguments; stock commands never
// select this branch.
func (s *CosignSigner) signDocumentInjected(ctx context.Context, document []byte, opts QualifiedSignOptions) (SignedDocument, error) {
	binary := s.binary
	if s.requiresBinaryPin {
		pinnedBinary, binaryCleanup, err := snapshotPinnedCosignExecutableNamedDiagnostic(ctx, s.binary, s.expectedBinaryDigest)
		if err != nil {
			return SignedDocument{}, err
		}
		defer binaryCleanup()
		binary = pinnedBinary
	}
	dir, err := os.MkdirTemp("", "nkd-cosign-sign-*")
	if err != nil {
		return SignedDocument{}, e.Wrap(e.CodeInternal, err, "creating private signing directory")
	}
	defer func() { _ = os.RemoveAll(dir) }()
	if err := os.Chmod(dir, 0o700); err != nil {
		return SignedDocument{}, e.Wrap(e.CodeInternal, err, "sealing private signing directory")
	}

	documentPath := filepath.Join(dir, "document")
	if err := writeSealedContext(ctx, documentPath, document); err != nil {
		return SignedDocument{}, err
	}
	signaturePath := filepath.Join(dir, "signature")
	certificatePath := filepath.Join(dir, "certificate.pem")
	bundlePath := ""
	if opts.Bundle {
		bundlePath = filepath.Join(dir, "bundle.sigstore")
	}
	args := qualifiedSignBlobArgs(documentPath, signaturePath, certificatePath, bundlePath, opts.ExpectedIssuer)
	var stdout, stderr bytes.Buffer
	if err := s.runner.Run(ctx, binary, args, &stdout, &stderr); err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return SignedDocument{}, ctxErr
		}
		return SignedDocument{}, e.Wrap(e.CodeSignatureInvalid, err,
			"qualified cosign sign-blob failed: %s", strings.TrimSpace(stderr.String()))
	}
	capturedDocument, err := capturePrivateOutputContext(ctx, documentPath, "signing document snapshot")
	if err != nil {
		return SignedDocument{}, err
	}
	if !bytes.Equal(capturedDocument, document) {
		return SignedDocument{}, e.New(e.CodeSignatureInvalid,
			"private signing document changed while cosign was running")
	}

	signature, err := capturePrivateOutputContext(ctx, signaturePath, "signature")
	if err != nil {
		return SignedDocument{}, err
	}
	certificate, err := capturePrivateOutputContext(ctx, certificatePath, "certificate")
	if err != nil {
		return SignedDocument{}, err
	}
	var bundle []byte
	if opts.Bundle {
		bundle, err = capturePrivateOutputContext(ctx, bundlePath, "bundle")
		if err != nil {
			return SignedDocument{}, err
		}
	}
	return signedDocumentFromExactOutputs(document, signature, certificate, bundle)
}

// signDocumentHeld is the only stock signing subprocess protocol. The document
// is an immutable held capability streamed on stdin; cosign and every writable
// output are inherited capabilities with no publishable pathname.
func (s *CosignSigner) signDocumentHeld(ctx context.Context, document []byte, opts QualifiedSignOptions) (SignedDocument, error) {
	if !s.requiresBinaryPin || s.expectedBinaryDigest == "" {
		return SignedDocument{}, e.New(e.CodeUnpinnedInput,
			"qualified cosign signing refuses an ambient unpinned executable; configure its exact sha256 digest")
	}
	executable, executableCleanup, err := snapshotPinnedCosignExecutableHeldContext(ctx, s.binary, s.expectedBinaryDigest)
	if err != nil {
		return SignedDocument{}, err
	}
	defer executableCleanup()

	documentDigest := digestBytes(document)
	heldDocument, err := heldfile.FromBytesContext(ctx, document, heldfile.Options{
		Label: "signing-document", MaxBytes: maxSignedDocumentBytes,
		ExpectedDigest: documentDigest,
	})
	if err != nil {
		return SignedDocument{}, e.Wrap(e.CodeEngineCapability, err,
			"holding exact signing document")
	}
	defer func() { _ = heldDocument.Close() }()

	var outputs []*heldfile.Output
	cleanupOutputs := func() {
		for i := len(outputs) - 1; i >= 0; i-- {
			_ = outputs[i].Close()
		}
	}
	defer cleanupOutputs()
	addOutput := func(label string) (*heldfile.Output, string, error) {
		output, outputErr := heldfile.NewOutput(label, maxVerificationMaterialBytes)
		if outputErr != nil {
			return nil, "", e.Wrap(e.CodeEngineCapability, outputErr,
				"creating anonymous cosign %s output", label)
		}
		path, pathErr := heldfile.ChildPath(1 + len(outputs))
		if pathErr != nil {
			_ = output.Close()
			return nil, "", e.Wrap(e.CodeEngineCapability, pathErr,
				"mapping anonymous cosign %s output", label)
		}
		outputs = append(outputs, output)
		return output, path, nil
	}
	signatureOutput, signaturePath, err := addOutput("signature")
	if err != nil {
		return SignedDocument{}, err
	}
	certificateOutput, certificatePath, err := addOutput("certificate.pem")
	if err != nil {
		return SignedDocument{}, err
	}
	var bundleOutput *heldfile.Output
	bundlePath := ""
	if opts.Bundle {
		bundleOutput, bundlePath, err = addOutput("bundle.sigstore")
		if err != nil {
			return SignedDocument{}, err
		}
	}

	descriptors := make([]heldfile.Descriptor, len(outputs))
	for i := range outputs {
		descriptors[i] = outputs[i]
	}
	args := qualifiedSignBlobArgs("-", signaturePath, certificatePath, bundlePath, opts.ExpectedIssuer)
	stdin := &countingReader{reader: io.NewSectionReader(heldDocument, 0, heldDocument.Size())}
	var stdout, stderr bytes.Buffer
	if err := heldfile.RunContext(ctx, executable, args, heldfile.RunOptions{
		Stdin: stdin, Stdout: &stdout, Stderr: &stderr,
		Env: qualifiedCosignEnvironment(os.Environ()),
	}, descriptors...); err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return SignedDocument{}, ctxErr
		}
		return SignedDocument{}, e.Wrap(e.CodeSignatureInvalid, err,
			"qualified cosign sign-blob failed: %s", strings.TrimSpace(stderr.String()))
	}
	if stdin.count != heldDocument.Size() {
		return SignedDocument{}, e.New(e.CodeSignatureInvalid,
			"qualified cosign signer did not receive the complete exact document")
	}
	if err := heldDocument.AssertUnchangedContext(ctx); err != nil || heldDocument.Digest() != documentDigest {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return SignedDocument{}, ctxErr
		}
		return SignedDocument{}, e.New(e.CodeSignatureInvalid,
			"held signing document changed while cosign was running")
	}

	signature, err := captureHeldSigningOutput(ctx, signatureOutput, "signature")
	if err != nil {
		return SignedDocument{}, err
	}
	certificate, err := captureHeldSigningOutput(ctx, certificateOutput, "certificate")
	if err != nil {
		return SignedDocument{}, err
	}
	var bundle []byte
	if bundleOutput != nil {
		bundle, err = captureHeldSigningOutput(ctx, bundleOutput, "bundle")
		if err != nil {
			return SignedDocument{}, err
		}
	}
	return signedDocumentFromExactOutputs(document, signature, certificate, bundle)
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.count += int64(n)
	return n, err
}

func captureHeldSigningOutput(ctx context.Context, output *heldfile.Output, label string) ([]byte, error) {
	sealed, err := output.SealContext(ctx)
	if err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, e.Wrap(e.CodeSignatureInvalid, err,
			"sealing anonymous cosign %s output", label)
	}
	defer func() { _ = sealed.Close() }()
	raw, err := sealed.ReadAllContext(ctx, maxVerificationMaterialBytes)
	if err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err,
			"capturing anonymous cosign %s output", label)
	}
	if len(raw) == 0 {
		return nil, e.New(e.CodeSignatureInvalid,
			"cosign %s output is empty", label)
	}
	if err := sealed.AssertUnchangedContext(ctx); err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, e.Wrap(e.CodeSignatureInvalid, err,
			"checking anonymous cosign %s output", label)
	}
	return raw, nil
}

func signedDocumentFromExactOutputs(document, signature, certificate, bundle []byte) (SignedDocument, error) {
	selectedCertificate := certificate
	if len(bundle) != 0 {
		bundleCertificate, newBundle, err := bindBundleCertificate(bundle, certificate)
		if err != nil {
			return SignedDocument{}, err
		}
		if newBundle {
			selectedCertificate = bundleCertificate
		}
	}
	cert, err := parseCertificate(selectedCertificate)
	if err != nil {
		return SignedDocument{}, err
	}
	issuer, err := certificateOIDCIssuer(cert)
	if err != nil {
		return SignedDocument{}, err
	}
	return SignedDocument{
		documentDigest: digestBytes(document),
		signature:      append([]byte(nil), signature...),
		certificate:    append([]byte(nil), certificate...),
		bundle:         append([]byte(nil), bundle...),
		identities:     certificateIdentities(cert),
		issuer:         issuer,
		receipt:        &signingReceipt{},
	}, nil
}

func (s SignedDocument) requireExpectedClaims(identity, issuer string) error {
	if identity == "" && issuer == "" {
		return nil
	}
	if identity == "" || issuer == "" {
		return e.New(e.CodeTrustPolicyMissing,
			"qualified keyless signing requires both expected identity and issuer")
	}
	if s.issuer != issuer {
		return e.New(e.CodeUntrustedSigner,
			"signing certificate issuer %q does not match expected issuer %q", s.issuer, issuer)
	}
	for _, candidate := range s.identities {
		if candidate == identity {
			return nil
		}
	}
	return e.New(e.CodeUntrustedSigner,
		"signing certificate identities %v do not contain expected identity %q", s.identities, identity)
}

func qualifiedSignBlobArgs(documentPath, signaturePath, certificatePath, bundlePath, oidcIssuer string) []string {
	args := []string{
		"sign-blob", "--yes",
		"--output-signature", signaturePath,
		"--output-certificate", certificatePath,
	}
	if oidcIssuer != "" {
		args = append(args, "--oidc-issuer", oidcIssuer)
	}
	if bundlePath != "" {
		args = append(args, "--bundle", bundlePath)
	}
	return append(args, documentPath)
}

func capturePrivateOutputContext(ctx context.Context, path, label string) ([]byte, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{
		Label: "cosign-" + label + "-output", MaxBytes: maxVerificationMaterialBytes,
	})
	if err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "holding cosign %s output", label)
	}
	defer func() { _ = held.Close() }()
	data, err := held.ReadAllContext(ctx, maxVerificationMaterialBytes)
	if err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "capturing cosign %s output", label)
	}
	if len(data) == 0 {
		return nil, e.New(e.CodeSignatureInvalid, "cosign %s output is empty", label)
	}
	if err := held.AssertUnchangedContext(ctx); err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "rechecking cosign %s output", label)
	}
	return data, nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "qualified signing requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "qualified signing canceled")
	}
	return nil
}
