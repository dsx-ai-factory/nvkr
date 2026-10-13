// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// QualificationPredicateTypeV2 cannot substitute for production SLSA evidence.
const QualificationPredicateTypeV2 = "https://nvkr.dev/qualification-evidence/v2"

// AttestQualificationV2 attaches one exact qualification predicate to an existing
// OCI subject, then independently downloads and verifies the attachment under
// the configured qualification identity. It never rebuilds or signs build
// provenance. The owning qualification module admits all verdicts before calling
// this adapter. Failure may leave an attachment and never returns success.
func (s *CosignOCISigner) AttestQualificationV2(ctx context.Context, subject Subject, predicate []byte) error {
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
	if err := requireQualificationPredicateV2(predicate); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	exact := append([]byte(nil), predicate...)
	policy, err := resolvePolicy(VerifyPolicy{RequireAttestation: true, ExpectedPredicate: exact, exactPredicateBytes: true}, s.identity, s.issuer)
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
	args := qualificationAttestArgsV2(subject.Ref, path)
	if _, err := s.capture(ctx, args, environment); err != nil {
		return err
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := assertPrivateFileUnchanged(path, exact); err != nil {
		return err
	}
	_, _, _, err = s.verifyOCIBundles(ctx, subject, QualificationPredicateTypeV2, policy, workspace, observationVerificationEnvironment(environment))
	if err != nil {
		return err
	}
	return contextError(ctx)
}

func qualificationAttestArgsV2(reference, predicatePath string) []string {
	return []string{"attest", "--yes", "--new-bundle-format", "--type", QualificationPredicateTypeV2, "--predicate", predicatePath, reference}
}

// VerifyQualificationV2 independently authenticates the exact expected predicate,
// subject, keyless certificate identity and issuer without signing permissions.
func (s *CosignOCISigner) VerifyQualificationV2(ctx context.Context, subject Subject, predicate []byte) error {
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
	if err := requireQualificationPredicateV2(predicate); err != nil {
		return err
	}
	policy, err := resolvePolicy(VerifyPolicy{RequireAttestation: true, ExpectedPredicate: predicate, exactPredicateBytes: true}, s.identity, s.issuer)
	if err != nil {
		return err
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
	_, _, _, err = s.verifyOCIBundles(ctx, subject, QualificationPredicateTypeV2, policy, workspace, observationVerificationEnvironment(ociCosignEnvironment(os.Environ(), dockerConfig)))
	if err != nil {
		return err
	}
	return contextError(ctx)
}

func requireQualificationPredicateV2(predicate []byte) error {
	if err := requireOCIPredicateWithLimit(predicate, maxOCIPredicateBytes, e.CodeVerifyFailed, "qualification v2 predicate"); err != nil {
		return err
	}
	raw, err := canonical.Marshal(json.RawMessage(predicate))
	if err != nil || !bytes.Equal(raw, predicate) {
		return e.New(e.CodeVerifyFailed, "qualification v2 requires exact canonical predicate bytes")
	}
	return nil
}
