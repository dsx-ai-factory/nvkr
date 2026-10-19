// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
)

// Validate checks the complete descriptive shape of existing exact-input
// identities. It performs no I/O or authentication and never reconstructs an
// ExactInputReceipt, verifies a publication or grants lifecycle authority.
func (description ExactInputDescription) Validate() error {
	for _, digest := range []string{description.PolicyDigest, description.DefinitionDigest, description.EnvironmentDigest, description.PlanDigest, description.ContentSubject} {
		if !canonical.IsDigest(digest) {
			return e.New(e.CodeVerifyFailed, "exact input description requires complete canonical input identities")
		}
	}
	// Keep the existing publication owner's parsing and coded reference errors.
	if err := delivery.ValidatePublicationReference(description.ReleaseSubject); err != nil {
		return err
	}
	if strings.Contains(description.ReleaseSubject, "://") || !safetext.Valid(description.ReleaseSubject, 4096, false) {
		return e.New(e.CodeVerifyFailed, "exact input description requires an exact safe OCI publication reference without a URL scheme")
	}
	return nil
}
