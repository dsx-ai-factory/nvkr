// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const localEvidenceMediaType = "application/vnd.nvkr.local-evidence-bundle.v1+json"

func (qualified *localVerifiedQualification) pushEvidence(ctx context.Context, repository string, options delivery.RegistryOptions) (string, error) {
	if qualified == nil {
		return "", e.New(e.CodeGateUnmet, "local evidence publication requires a verified gate")
	}
	operation, finish, err := qualified.build.beginOperation(ctx)
	if err != nil {
		return "", err
	}
	defer finish()
	raw, err := qualified.evidenceBundleBytes(operation)
	if err != nil {
		return "", err
	}
	ref, err := delivery.PushDocumentArtifact(operation, repository, localEvidenceMediaType, raw, options)
	if err != nil {
		return ref, err
	}
	if err := qualified.requireLive(operation); err != nil {
		return ref, err
	}
	return ref, nil
}

// Download supplies bounded descriptions only. The layout consumer must still
// authenticate all three roles against its independently selected public policy.
func pullLocalEvidence(ctx context.Context, reference string, options delivery.RegistryOptions) (map[string][]byte, error) {
	raw, err := delivery.PullDocumentArtifact(ctx, reference, localEvidenceMediaType, localEvidenceBundleLimit, options)
	if err != nil {
		return nil, err
	}
	return decodeLocalEvidenceBundle(raw)
}
