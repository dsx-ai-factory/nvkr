// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ExportForInvocation materializes held, verified publication bytes only when
// their authenticated aggregate invocation matches the expected producing Run
// and the exact-input receipt was issued by this same live verified release.
// The extracted layout is transport data; the capability's policy scope remains
// artifact-publication and does not authorize runtime qualification or release.
func (verified *VerifiedRelease) ExportForInvocation(ctx context.Context, receipt *ExactInputReceipt, invocation, out string) error {
	if ctx == nil || strings.TrimSpace(invocation) == "" || strings.TrimSpace(out) == "" {
		return e.New(e.CodeUsage, "publication export requires context, invocation and destination")
	}
	if verified == nil {
		return e.New(e.CodeGateUnmet, "release capability is not verified")
	}
	verified.mu.RLock()
	defer verified.mu.RUnlock()
	if !verified.valid || verified.publication == nil {
		return e.New(e.CodeGateUnmet, "release capability is not verified")
	}
	if receipt == nil || !receipt.issued || receipt.owner != verified {
		return e.New(e.CodeGateUnmet, "publication export requires this release's exact-input receipt")
	}
	metadata, err := verified.publication.Description(verified.release.Subject)
	if err != nil {
		return err
	}
	if metadata.Metadata.InvocationID != invocation {
		return e.New(e.CodeVerifyFailed, "verified publication belongs to a different originating invocation")
	}
	exact := receipt.description
	if exact.ReleaseSubject != verified.release.Subject || exact.PolicyDigest != verified.release.PolicyDigest ||
		exact.DefinitionDigest != metadata.Metadata.DefinitionDigest ||
		exact.EnvironmentDigest != metadata.Metadata.EnvironmentDigest ||
		exact.PlanDigest != metadata.Metadata.PlanDigest || exact.ContentSubject != metadata.Metadata.ContentSubject {
		return e.New(e.CodeVerifyFailed, "publication export exact-input receipt differs from the verified release")
	}
	return verified.publication.WriteLayout(ctx, out)
}
