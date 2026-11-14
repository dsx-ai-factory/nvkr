// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"io/fs"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// trustStoreFile is the operator's on-disk trust store: the identities the operator
// has explicitly promoted to trusted (ADR-04 §2). It is deliberately OUTSIDE any
// verified object — trust must originate from outside what it authenticates.
type trustStoreFile struct {
	Identities            []string `json:"identities"`
	ProfileIdentities     []string `json:"profileIdentities,omitempty"`
	ConformanceIdentities []string `json:"conformanceIdentities,omitempty"`
	StatusIdentities      []string `json:"statusIdentities,omitempty"`
	PromotionIdentities   []string `json:"promotionPolicyIdentities,omitempty"`
	ReleaseSetIdentities  []string `json:"releaseSetIdentities,omitempty"`
	// StatusSigner is the v1 single-identity spelling. It is accepted only as
	// local policy input and is folded into StatusIdentities; it is never treated
	// as evidence that a document was authenticated.
	StatusSigner    string `json:"statusSigner,omitempty"`
	PromoteAdvisory bool   `json:"promoteAdvisory,omitempty"`
}

func loadTrustStoreFile(ctx context.Context, path string) (trustStoreFile, error) {
	var s trustStoreFile
	if err := inputContextError(ctx, "reading trust store"); err != nil {
		return s, err
	}
	err := readCanonicalJSONContext(ctx, path, &s)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil // an absent store is empty, not an error
	}
	if err != nil {
		var coded *e.Error
		if errors.As(err, &coded) && (coded.Code == e.CodeTimeout || coded.Code == e.CodeUsage) {
			return s, err
		}
		return s, e.Wrap(e.CodeTrustPolicyMissing, err, "reading canonical trust store %s", path)
	}
	return s, nil
}
