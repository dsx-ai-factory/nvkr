// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

const maxLocalKeyBytes = 64 << 10

// LocalCosign authenticates detached local-rehearsal documents with explicit
// public keys. It cannot produce a keyless SignedDocument or a hosted receipt.
// Passwordless demo keys are supplied explicitly, never through ambient config.
type LocalCosign struct {
	binary string
	digest string
}

// LocalSignedDocument proves only that the exact bytes verify under the selected
// local key. Artifact membership, provenance and signer-role checks remain the
// responsibility of the local qualification owner.
type LocalSignedDocument struct {
	document, key string
	signature     []byte
}

func NewLocalCosign(binary, digest string) (*LocalCosign, error) {
	if binary == "" || !canonical.IsDigest(digest) {
		return nil, e.New(e.CodeUnpinnedInput, "local signing requires an exact cosign executable pin")
	}
	if runtime.GOOS != "linux" {
		return nil, e.New(e.CodeEngineCapability, "local signing requires Linux held executable support")
	}
	return &LocalCosign{binary: binary, digest: digest}, nil
}

// Signature returns detached bytes; no serialization can restore this receipt.
func (s LocalSignedDocument) Signature() ([]byte, error) {
	if s.document == "" || s.key == "" || len(s.signature) == 0 {
		return nil, e.New(e.CodeSignatureInvalid, "local document has no verified signature")
	}
	return bytes.Clone(s.signature), nil
}

func (s LocalSignedDocument) RequireFor(ctx context.Context, document, publicKey []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if _, err := s.Signature(); err != nil {
		return err
	}
	if digestBytes(document) != s.document || digestBytes(publicKey) != s.key {
		return e.New(e.CodeSignatureInvalid, "local signature belongs to different document bytes or key")
	}
	return nil
}

func (s *LocalCosign) SignDocument(ctx context.Context, document, privateKey, publicKey []byte) (LocalSignedDocument, error) {
	if err := s.admit(ctx, document, publicKey); err != nil {
		return LocalSignedDocument{}, err
	}
	if len(privateKey) == 0 || len(privateKey) > maxLocalKeyBytes {
		return LocalSignedDocument{}, e.New(e.CodeTrustPolicyMissing, "local signing requires a bounded explicit private key")
	}
	// Freeze both public inputs before subprocess execution and verification.
	document, publicKey = bytes.Clone(document), bytes.Clone(publicKey)
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	executable, closeExecutable, err := snapshotPinnedCosignExecutableHeldContext(ctx, s.binary, s.digest)
	if err != nil {
		return LocalSignedDocument{}, err
	}
	defer closeExecutable()
	key, err := heldfile.FromBytesContext(ctx, privateKey, heldfile.Options{Label: "local-signing-key", MaxBytes: maxLocalKeyBytes})
	if err != nil {
		return LocalSignedDocument{}, e.Wrap(e.CodeSignatureInvalid, err, "holding local signing key")
	}
	defer func() { _ = key.Close() }()
	output, err := heldfile.NewOutput("local-signature", maxVerificationMaterialBytes)
	if err != nil {
		return LocalSignedDocument{}, e.Wrap(e.CodeSignatureInvalid, err, "creating local signature output")
	}
	defer func() { _ = output.Close() }()
	// RunContext reserves inherited descriptor zero for the executable.
	keyPath, err := heldfile.ChildPath(1)
	if err != nil {
		return LocalSignedDocument{}, e.Wrap(e.CodeEngineCapability, err, "mapping local signing key")
	}
	outputPath, err := heldfile.ChildPath(2)
	if err != nil {
		return LocalSignedDocument{}, e.Wrap(e.CodeEngineCapability, err, "mapping local signature output")
	}
	args := []string{"sign-blob", "--yes", "--key", keyPath, "--use-signing-config=false", "--new-bundle-format=false", "--tlog-upload=false", "--output-signature", outputPath, "-"}
	if err := runLocalCosign(ctx, executable, args, document, key, output); err != nil {
		return LocalSignedDocument{}, err
	}
	signature, err := captureHeldSigningOutput(ctx, output, "local signature")
	if err != nil {
		return LocalSignedDocument{}, err
	}
	if err := s.VerifyDocument(ctx, document, signature, publicKey); err != nil {
		return LocalSignedDocument{}, err
	}
	return LocalSignedDocument{document: digestBytes(document), key: digestBytes(publicKey), signature: bytes.Clone(signature)}, nil
}

// VerifyDocument needs only public key bytes and never accepts certificate or
// issuer claims. Callers select this key independently from the signed document.
func (s *LocalCosign) VerifyDocument(ctx context.Context, document, signature, publicKey []byte) error {
	if err := s.admit(ctx, document, publicKey); err != nil {
		return err
	}
	if len(signature) == 0 || int64(len(signature)) > maxVerificationMaterialBytes {
		return e.New(e.CodeSignatureInvalid, "local signature is missing or oversized")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	executable, closeExecutable, err := snapshotPinnedCosignExecutableHeldContext(ctx, s.binary, s.digest)
	if err != nil {
		return err
	}
	defer closeExecutable()
	var descriptors []heldfile.Descriptor
	paths := make([]string, 0, 2)
	for index, raw := range [][]byte{publicKey, signature} {
		held, err := heldfile.FromBytesContext(ctx, raw, heldfile.Options{Label: "local-verification-material", MaxBytes: maxVerificationMaterialBytes})
		if err != nil {
			return e.Wrap(e.CodeSignatureInvalid, err, "holding local verification material")
		}
		defer func() { _ = held.Close() }()
		descriptors = append(descriptors, held)
		path, err := heldfile.ChildPath(1 + index)
		if err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "mapping local verification material")
		}
		paths = append(paths, path)
	}
	args := []string{"verify-blob", "--key", paths[0], "--signature", paths[1], "--insecure-ignore-tlog", "--offline", "-"}
	return runLocalCosign(ctx, executable, args, bytes.Clone(document), descriptors...)
}

func (s *LocalCosign) admit(ctx context.Context, document, publicKey []byte) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if s == nil || s.binary == "" || !canonical.IsDigest(s.digest) || runtime.GOOS != "linux" {
		return e.New(e.CodeEngineCapability, "local cosign adapter is unavailable")
	}
	if len(document) == 0 || int64(len(document)) > maxSignedDocumentBytes {
		return e.New(e.CodeSignatureInvalid, "local document is empty or oversized")
	}
	if len(publicKey) == 0 || len(publicKey) > maxLocalKeyBytes {
		return e.New(e.CodeTrustPolicyMissing, "local verification requires a bounded explicit public key")
	}
	if _, err := NormalizeLocalPublicKey(publicKey); err != nil {
		return err
	}
	return nil
}

func runLocalCosign(ctx context.Context, executable *heldfile.File, args []string, document []byte, descriptors ...heldfile.Descriptor) error {
	input := &countingReader{reader: bytes.NewReader(document)}
	stdout, stderr := &cosignOutputBuffer{}, &cosignOutputBuffer{}
	environment := append(qualifiedCosignEnvironment(os.Environ()), "COSIGN_PASSWORD=")
	err := heldfile.RunContext(ctx, executable, args, heldfile.RunOptions{Stdin: input, Stdout: stdout, Stderr: stderr, Env: environment}, descriptors...)
	if canceled := contextError(ctx); canceled != nil {
		return canceled
	}
	if err != nil {
		// Do not include subprocess diagnostics: malformed private keys may be
		// echoed by an external parser. The stable code is sufficient here.
		return e.Wrap(e.CodeSignatureInvalid, err, "local cosign operation failed")
	}
	if stdout.exceeded || stderr.exceeded {
		return e.New(e.CodeSignatureInvalid, "local cosign output exceeded its bound")
	}
	if input.count != int64(len(document)) {
		return e.Wrap(e.CodeSignatureInvalid, io.ErrUnexpectedEOF, "local cosign did not consume the complete document")
	}
	return nil
}
