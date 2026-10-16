// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"os"
	"path/filepath"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// QualificationPredicateType cannot substitute for production SLSA evidence.
const QualificationPredicateType = "https://nvkr.dev/qualification-evidence/v2"

// DevelopmentQualificationPredicateType is never a production qualification claim.
const DevelopmentQualificationPredicateType = "https://nvkr.dev/development-qualification-evidence/v1"

// MaxQualificationPredicateBytes is the shared publication and verification bound.
// Callers admit the complete canonical predicate before constructing a signer.
const MaxQualificationPredicateBytes = maxOCIPredicateBytes

// AttestQualification attaches one exact qualification predicate to an existing
// OCI subject, then independently downloads and verifies the attachment under
// the configured qualification identity. It never rebuilds or signs build
// provenance. The owning qualification module admits all verdicts before calling
// this adapter. Failure may leave an attachment and never returns success.
func (s *CosignOCISigner) AttestQualification(ctx context.Context, subject Subject, predicate []byte) error {
	return s.attestQualification(ctx, subject, predicate, QualificationPredicateType)
}

// AttestDevelopmentQualification signs and independently reads back development-only evidence.
func (s *CosignOCISigner) AttestDevelopmentQualification(ctx context.Context, subject Subject, predicate []byte) error {
	return s.attestQualification(ctx, subject, predicate, DevelopmentQualificationPredicateType)
}

func (s *CosignOCISigner) attestQualification(ctx context.Context, subject Subject, predicate []byte, predicateType string) error {
	if s == nil || s.runner == nil {
		return e.New(e.CodeInternal, "qualification signer is unavailable")
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := s.admit(); err != nil {
		return err
	}
	if err := subject.Validate(); err != nil {
		return err
	}
	if err := requireOCIPredicateWithLimit(predicate, MaxQualificationPredicateBytes, e.CodeVerifyFailed, "qualification predicate"); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	exact := append([]byte(nil), predicate...)
	policy, err := resolvePolicy(VerifyPolicy{RequireAttestation: true, ExpectedPredicate: exact}, s.identity, s.issuer)
	if err != nil {
		return err
	}
	workspace, cleanup, err := newPrivateWorkspace("nvkr-qualification-attest-*")
	if err != nil {
		return err
	}
	defer cleanup()
	path := filepath.Join(workspace, "qualification.json")
	if err := writePrivateFile(ctx, path, exact); err != nil {
		return err
	}
	dockerConfig, err := s.writeDockerConfig(ctx, workspace)
	if err != nil {
		return err
	}
	environment := ociCosignEnvironment(os.Environ(), dockerConfig)
	args := qualificationAttestArgs(subject.Ref, path)
	args[4] = predicateType
	if _, err := s.capture(ctx, args, environment); err != nil {
		return err
	}
	if err := assertPrivateFileUnchanged(path, exact); err != nil {
		return err
	}
	_, _, _, err = s.verifyOCIBundles(ctx, subject, predicateType, policy, workspace, environment)
	return err
}

func qualificationAttestArgs(reference, predicatePath string) []string {
	return []string{"attest", "--yes", "--new-bundle-format", "--type", QualificationPredicateType, "--predicate", predicatePath, reference}
}

// VerifyQualification independently authenticates the exact expected predicate,
// subject, keyless certificate identity and issuer without signing permissions.
func (s *CosignOCISigner) VerifyQualification(ctx context.Context, subject Subject, predicate []byte) error {
	if s == nil || s.runner == nil {
		return e.New(e.CodeInternal, "qualification verifier is unavailable")
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := s.admit(); err != nil {
		return err
	}
	if err := subject.Validate(); err != nil {
		return err
	}
	policy, err := resolvePolicy(VerifyPolicy{RequireAttestation: true, ExpectedPredicate: predicate}, s.identity, s.issuer)
	if err != nil {
		return err
	}
	if len(predicate) == 0 {
		return e.New(e.CodeVerifyFailed, "qualification verification requires exact evidence bytes")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	workspace, cleanup, err := newPrivateWorkspace("nvkr-qualification-verify-*")
	if err != nil {
		return err
	}
	defer cleanup()
	dockerConfig, err := s.writeDockerConfig(ctx, workspace)
	if err != nil {
		return err
	}
	_, _, _, err = s.verifyOCIBundles(ctx, subject, QualificationPredicateType, policy, workspace, ociCosignEnvironment(os.Environ(), dockerConfig))
	return err
}
