// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import "context"

// ReleaseManifestPredicateType is distinct from build provenance and
// qualification evidence; neither can substitute for a release manifest.
const ReleaseManifestPredicateType = "https://nvkr.dev/release-manifest/v1"

// AttestReleaseManifest attaches one exact release manifest to the publication
// root it describes, then independently downloads and verifies the attachment
// under the configured qualification identity. The owning release-manifest
// module authenticates complete qualification before calling this adapter.
// Failure may leave an attachment and never returns success.
func (s *CosignOCISigner) AttestReleaseManifest(ctx context.Context, subject Subject, predicate []byte) error {
	return s.attestExact(ctx, subject, ReleaseManifestPredicateType, predicate, "release manifest")
}

// VerifyReleaseManifest authenticates the exact expected manifest bytes on the
// exact publication root under the configured identity and issuer.
func (s *CosignOCISigner) VerifyReleaseManifest(ctx context.Context, subject Subject, predicate []byte) error {
	return s.verifyExact(ctx, subject, ReleaseManifestPredicateType, predicate, "release manifest")
}
