// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// TestObservationPredicateType authenticates observations without granting
// production, passing qualification or release authority.
const TestObservationPredicateType = "https://nvkr.dev/test-observation/v1"

// AttestTestObservation attaches one exact test observation predicate to an existing
// OCI subject, then independently downloads and verifies the attachment under
// the configured test observation identity. It never rebuilds or signs build
// provenance. Failed observations are publishable: the owning module validates
// their schema and evaluates outcome policy. Failure may leave an attachment
// and never returns success.
func (s *CosignOCISigner) AttestTestObservation(ctx context.Context, subject Subject, predicate []byte) error {
	if s == nil || s.runner == nil {
		return e.New(e.CodeInternal, "test observation signer is unavailable")
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
	if err := requireObservationPredicate(predicate); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	exact := append([]byte(nil), predicate...)
	policy, err := resolvePolicy(VerifyPolicy{RequireAttestation: true, ExpectedPredicate: exact, exactPredicateBytes: true}, s.identity, s.issuer)
	if err != nil {
		return err
	}
	workspace, cleanup, err := newPrivateWorkspace("nvkr-test-observation-attest-*")
	if err != nil {
		return err
	}
	defer cleanup()
	path := filepath.Join(workspace, "test-observation.json")
	if err := writePrivateFile(ctx, path, exact); err != nil {
		return err
	}
	dockerConfig, err := s.writeObservationDockerConfig(ctx, workspace)
	if err != nil {
		return err
	}
	environment := ociCosignEnvironment(os.Environ(), dockerConfig)
	args := testObservationAttestArgs(subject.Ref, path)
	if _, err := s.capture(ctx, args, environment); err != nil {
		return err
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := assertPrivateFileUnchanged(path, exact); err != nil {
		return err
	}
	_, _, _, err = s.verifyOCIBundles(ctx, subject, TestObservationPredicateType, policy, workspace, observationVerificationEnvironment(environment))
	if err != nil {
		return err
	}
	return contextError(ctx)
}

func testObservationAttestArgs(reference, predicatePath string) []string {
	return []string{"attest", "--yes", "--new-bundle-format", "--type", TestObservationPredicateType, "--predicate", predicatePath, reference}
}

// VerifyTestObservation independently authenticates the exact expected predicate,
// subject, keyless certificate identity and issuer without signing permissions.
func (s *CosignOCISigner) VerifyTestObservation(ctx context.Context, subject Subject, predicate []byte) error {
	if s == nil || s.runner == nil {
		return e.New(e.CodeInternal, "test observation verifier is unavailable")
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
	if err := requireObservationPredicate(predicate); err != nil {
		return err
	}
	policy, err := resolvePolicy(VerifyPolicy{RequireAttestation: true, ExpectedPredicate: predicate, exactPredicateBytes: true}, s.identity, s.issuer)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	workspace, cleanup, err := newPrivateWorkspace("nvkr-test-observation-verify-*")
	if err != nil {
		return err
	}
	defer cleanup()
	dockerConfig, err := s.writeObservationDockerConfig(ctx, workspace)
	if err != nil {
		return err
	}
	_, _, _, err = s.verifyOCIBundles(ctx, subject, TestObservationPredicateType, policy, workspace, observationVerificationEnvironment(ociCosignEnvironment(os.Environ(), dockerConfig)))
	if err != nil {
		return err
	}
	return contextError(ctx)
}

// Verification requires registry read access but never OIDC minting authority.
func observationVerificationEnvironment(environment []string) []string {
	out := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_URL":
			continue
		}
		out = append(out, entry)
	}
	return out
}

// Cosign may reformat JSON, so the portable document has exactly one admitted
// canonical byte representation. Original result bytes are separately pinned.
func requireObservationPredicate(predicate []byte) error {
	if err := requireOCIPredicateWithLimit(predicate, maxOCIPredicateBytes, e.CodeVerifyFailed, "test observation predicate"); err != nil {
		return err
	}
	document, err := canonical.Marshal(json.RawMessage(predicate))
	if err != nil || !bytes.Equal(document, predicate) {
		return e.New(e.CodeVerifyFailed, "test observation requires exact canonical predicate bytes")
	}
	return nil
}

// Even credential-free observation operations select an explicit empty private
// Docker config so cosign cannot consult an ambient HOME keychain.
func (s *CosignOCISigner) writeObservationDockerConfig(ctx context.Context, workspace string) (string, error) {
	if !s.credential.empty() {
		return s.writeDockerConfig(ctx, workspace)
	}
	dir := filepath.Join(workspace, dockerConfigDirName)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", e.Wrap(e.CodeInternal, err, "creating empty private registry config")
	}
	if err := writePrivateFile(ctx, filepath.Join(dir, dockerConfigFileName), []byte(`{"auths":{}}`)); err != nil {
		return "", err
	}
	return dir, nil
}
