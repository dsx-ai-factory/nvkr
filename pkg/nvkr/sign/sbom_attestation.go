// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// SBOMAttester creates and authenticates standard CycloneDX attestation bundles.
// The caller binds the digest and exact document to its admitted preparation.
// Per-output GitHub attestation checks exported controls; preparation and
// publication own complete payload inspection. This interface issues no payload
// verification capability.
type SBOMAttester interface {
	AttestSBOM(context.Context, string, []byte) ([]byte, error)
	VerifySBOM(context.Context, string, []byte) error
}

func (s *CosignOCISigner) AttestSBOM(ctx context.Context, digest string, predicate []byte) ([]byte, error) {
	if err := s.admitSBOM(ctx, digest); err != nil {
		return nil, err
	}
	// This inventory policy applies to every SBOM subject, not only disks.
	// Provenance retains its separate 4 MiB / CodeProvenanceMissing admission.
	if err := requireOCIPredicateWithLimit(predicate, sbom.MaxDocumentBytes, e.CodeVerifyFailed, "SBOM inventory"); err != nil {
		return nil, err
	}
	exactPredicate := append([]byte(nil), predicate...)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	workspace, cleanup, err := newPrivateWorkspace("nvkr-sbom-attest-*")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	input := filepath.Join(workspace, "sbom.json")
	if err := writePrivateFile(ctx, input, exactPredicate); err != nil {
		return nil, err
	}
	output, err := s.createSBOMBundle(ctx, digest, exactPredicate, input, workspace)
	if err != nil {
		return nil, err
	}
	held, err := heldfile.OpenContext(ctx, output, heldfile.Options{Label: "SBOM attestation", MaxBytes: sbom.MaxBundleBytes})
	if err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "reading SBOM attestation")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, sbom.MaxBundleBytes)
	if err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "reading SBOM attestation")
	}
	document, err := sbom.AttestedDocument(raw, digest)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(document, exactPredicate) {
		return nil, e.New(e.CodeVerifyFailed, "attestation substituted SBOM predicate")
	}
	if err := s.VerifySBOM(ctx, digest, raw); err != nil {
		return nil, err
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return raw, nil
}

// cosign 3.1.3 cannot recover an accepted Rekor v1 entry after a conflicting
// response (sigstore-go#647). Retry only that creation failure with a fresh
// keyless invocation. A conflict never authenticates a bundle; the caller still
// verifies the successful attempt's exact subject, predicate and trust policy.
func (s *CosignOCISigner) createSBOMBundle(ctx context.Context, digest string, predicate []byte, input, workspace string) (string, error) {
	environment := ociCosignEnvironment(os.Environ(), "")
	for attempt := 1; attempt <= 3; attempt++ {
		if err := contextError(ctx); err != nil {
			return "", err
		}
		output := filepath.Join(workspace, fmt.Sprintf("bundle-%d.json", attempt))
		args := []string{"attest-blob", "--yes", "--new-bundle-format", "--bundle", output, "--type", "cyclonedx", "--predicate", input, "--hash", strings.TrimPrefix(digest, "sha256:"), "artifact"}
		var stdout, stderr cosignOutputBuffer
		runErr := s.run(ctx, args, environment, &stdout, &stderr)
		predicateErr := assertPrivateFileUnchanged(input, predicate)
		if err := contextError(ctx); err != nil {
			return "", err
		}
		if predicateErr != nil {
			return "", predicateErr
		}
		if stdout.exceeded || stderr.exceeded {
			return "", e.New(e.CodeSignatureInvalid, "cosign attest-blob produced an oversized report")
		}
		if runErr == nil {
			return output, nil
		}
		if attempt == 3 || !sbomRekorEntryConflict(stderr.String()) {
			return "", e.Wrap(e.CodeSignatureInvalid, runErr,
				"cosign attest-blob failed after %d attempt(s): %s", attempt, strings.TrimSpace(stderr.String()))
		}
		timer := time.NewTimer(time.Duration(attempt) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", contextError(ctx)
		case <-timer.C:
		}
	}
	return "", e.New(e.CodeInternal, "SBOM attestation exhausted its attempt bound")
}

func sbomRekorEntryConflict(stderr string) bool {
	const prefix = "creating bundle: signing bundle: error signing bundle: [POST /api/v1/log/entries][409] createLogEntryConflict "
	for _, line := range strings.Split(stderr, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "Error: ")
		line = strings.TrimPrefix(line, "error during command execution: ")
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func (s *CosignOCISigner) VerifySBOM(ctx context.Context, digest string, bundle []byte) error {
	if err := s.admitSBOM(ctx, digest); err != nil {
		return err
	}
	if _, err := sbom.AttestedDocument(bundle, digest); err != nil {
		return err
	}
	workspace, cleanup, err := newPrivateWorkspace("nvkr-sbom-verify-*")
	if err != nil {
		return err
	}
	defer cleanup()
	path := filepath.Join(workspace, "bundle.json")
	if err := writePrivateFile(ctx, path, bundle); err != nil {
		return err
	}
	args := ociVerifyBundleArgs(path, digest, sbom.CycloneDXPredicateType, VerifyPolicy{IdentityRegexp: s.identity, Issuer: s.issuer})
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
