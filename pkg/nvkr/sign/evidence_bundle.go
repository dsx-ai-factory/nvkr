// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// CosignEvidenceVerifier authenticates upstream Sigstore provenance bundles with
// the exact pinned cosign capability. It trusts cosign's public-good TUF root;
// a pinned offline trusted root is not supported yet.
type CosignEvidenceVerifier struct {
	cosign *CosignOCISigner
}

var _ sbom.EvidenceBundleVerifier = (*CosignEvidenceVerifier)(nil)

// NewCosignEvidenceVerifier constructs the verifier. A nil runner selects the
// stock pinned, Linux-only cosign execution path, which requires binaryDigest.
func NewCosignEvidenceVerifier(binary, binaryDigest string, runner CommandRunner) (*CosignEvidenceVerifier, error) {
	if strings.TrimSpace(binary) == "" {
		binary = DefaultBinary
	}
	if runner == nil {
		runner = ExecRunner{}
	}
	if binaryDigest != "" && !canonical.IsDigest(binaryDigest) {
		return nil, e.New(e.CodeUnpinnedInput,
			"upstream evidence cosign binary digest must be an exact lowercase sha256 identity")
	}
	return &CosignEvidenceVerifier{cosign: &CosignOCISigner{binary: binary, binaryDigest: binaryDigest, runner: runner}}, nil
}

// VerifyEvidenceBundle checks the bundle's statement subject and certificate
// claims, then runs cosign verify-blob-attestation for the signature, exact
// identity, issuer and transparency evidence.
func (v *CosignEvidenceVerifier) VerifyEvidenceBundle(ctx context.Context, bundle []byte, policy sbom.EvidenceBundlePolicy) error {
	if v == nil || v.cosign == nil || v.cosign.runner == nil {
		return e.New(e.CodeInternal, "upstream evidence cosign verifier is not initialized")
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := v.cosign.admit(); err != nil {
		return err
	}
	if !canonical.IsDigest(policy.SubjectDigest) || policy.PredicateType == "" ||
		policy.Issuer == "" || policy.Identity == "" {
		return e.New(e.CodeTrustPolicyMissing,
			"upstream evidence verification requires an exact subject, predicate, issuer and identity")
	}
	if len(bundle) == 0 || len(bundle) > sbom.MaxBundleBytes {
		return e.New(e.CodeSignatureInvalid, "upstream evidence bundle is empty or exceeds its byte limit")
	}
	verifyPolicy := VerifyPolicy{IdentityRegexp: "^" + regexp.QuoteMeta(policy.Identity) + "$", Issuer: policy.Issuer}
	if _, _, err := ociBundleClaims(bundle, policy.SubjectDigest, policy.PredicateType, verifyPolicy); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	workspace, cleanup, err := newPrivateWorkspace("nvkr-cosign-evidence-verify-*")
	if err != nil {
		return err
	}
	defer cleanup()
	path := filepath.Join(workspace, "bundle.json")
	if err := writePrivateFile(ctx, path, bundle); err != nil {
		return err
	}
	args := ociVerifyBundleArgs(path, policy.SubjectDigest, policy.PredicateType, verifyPolicy)
	if _, err := v.cosign.capture(ctx, args, ociCosignEnvironment(os.Environ(), "")); err != nil {
		return err
	}
	if err := assertPrivateFileUnchanged(path, bundle); err != nil {
		return err
	}
	return contextError(ctx)
}
