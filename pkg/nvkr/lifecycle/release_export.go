// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
)

// ExportRelease returns the complete versioned JSON description of one exact
// published release. It preserves every output and its evidence without choosing
// consumer roles, a release-wide source revision, or a deployment platform.
// Metadata inspection does not verify payloads, signatures or consumer policy.
func ExportRelease(ctx context.Context, reference string, options InspectionOptions) ([]byte, error) {
	if err := admitReleaseInspection(ctx, reference, options); err != nil {
		return nil, err
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultInspectionTimeout
	}
	// Keep the deadline alive through rendering; InspectRelease owns a child
	// context whose lifetime ends when its metadata description is returned.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	description, err := InspectRelease(ctx, reference, options)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "release export canceled")
	}
	raw, err := releaseview.Render(description, releaseview.Options{Format: "json"})
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "release export canceled")
	}
	return raw, nil
}
