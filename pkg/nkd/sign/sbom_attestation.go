package sign

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
)

// SBOMAttester creates and authenticates standard CycloneDX attestation bundles.
// The digest is supplied only after the caller has inspected its payload closure.
type SBOMAttester interface {
	AttestSBOM(context.Context, string, []byte) ([]byte, error)
	VerifySBOM(context.Context, string, []byte) error
}

func (s *CosignOCISigner) AttestSBOM(ctx context.Context, digest string, predicate []byte) ([]byte, error) {
	if err := s.admitSBOM(ctx, digest); err != nil {
		return nil, err
	}
	if err := requireOCIPredicate(predicate); err != nil {
		return nil, err
	}
	workspace, cleanup, err := newPrivateWorkspace("nkd-sbom-attest-*")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	input, output := filepath.Join(workspace, "sbom.json"), filepath.Join(workspace, "bundle.json")
	if err := writePrivateFile(ctx, input, predicate); err != nil {
		return nil, err
	}
	args := []string{"attest-blob", "--yes", "--new-bundle-format", "--bundle", output, "--type", "cyclonedx", "--predicate", input, "--hash", strings.TrimPrefix(digest, "sha256:"), "artifact"}
	if _, err := s.capture(ctx, args, ociCosignEnvironment(os.Environ(), "")); err != nil {
		return nil, err
	}
	if err := assertPrivateFileUnchanged(input, predicate); err != nil {
		return nil, err
	}
	held, err := heldfile.OpenContext(ctx, output, heldfile.Options{Label: "SBOM attestation", MaxBytes: 8 << 20})
	if err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "reading SBOM attestation")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, 8<<20)
	if err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "reading SBOM attestation")
	}
	document, err := sbom.AttestedDocument(raw, digest)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(document, predicate) {
		return nil, e.New(e.CodeVerifyFailed, "attestation substituted SBOM predicate")
	}
	if err := s.VerifySBOM(ctx, digest, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *CosignOCISigner) VerifySBOM(ctx context.Context, digest string, bundle []byte) error {
	if err := s.admitSBOM(ctx, digest); err != nil {
		return err
	}
	if _, err := sbom.AttestedDocument(bundle, digest); err != nil {
		return err
	}
	workspace, cleanup, err := newPrivateWorkspace("nkd-sbom-verify-*")
	if err != nil {
		return err
	}
	defer cleanup()
	path := filepath.Join(workspace, "bundle.json")
	if err := writePrivateFile(ctx, path, bundle); err != nil {
		return err
	}
	args := []string{"verify-blob-attestation", "--bundle", path, "--type", "cyclonedx", "--digest", strings.TrimPrefix(digest, "sha256:"), "--digestAlg", "sha256", "--certificate-identity-regexp", s.identity, "--certificate-oidc-issuer", s.issuer}
	if _, err := s.capture(ctx, args, ociCosignEnvironment(os.Environ(), "")); err != nil {
		return err
	}
	return assertPrivateFileUnchanged(path, bundle)
}

func (s *CosignOCISigner) admitSBOM(ctx context.Context, digest string) error {
	if s == nil || s.runner == nil {
		return e.New(e.CodeInternal, "SBOM signer is not initialized")
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := s.admit(); err != nil {
		return err
	}
	if !canonical.IsDigest(digest) {
		return e.New(e.CodeUnpinnedSource, "SBOM attestation requires an exact subject digest")
	}
	return nil
}
