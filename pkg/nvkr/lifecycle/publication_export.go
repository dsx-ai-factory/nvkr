// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ExportForInvocation materializes held, verified publication bytes only when
// their authenticated aggregate invocation matches the expected producing Run.
// The extracted layout is transport data; the capability's policy scope remains
// artifact-publication and does not authorize runtime qualification or release.
func (verified *VerifiedRelease) ExportForInvocation(ctx context.Context, invocation, out string) error {
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
	metadata, err := verified.publication.Description(verified.release.Subject)
	if err != nil {
		return err
	}
	if metadata.Metadata.InvocationID != invocation {
		return e.New(e.CodeVerifyFailed, "verified publication belongs to a different originating invocation")
	}
	return verified.publication.WriteLayout(ctx, out)
}
