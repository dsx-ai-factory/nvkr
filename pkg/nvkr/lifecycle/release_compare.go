// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releaseview"
)

// ReleaseComparisonRequest names two exact publication roots. Each side may use
// its own local layout. Registry and workspace settings apply to registry sides;
// with two local layouts they are rejected like any local inspection.
type ReleaseComparisonRequest struct {
	Before, After             string
	BeforeLayout, AfterLayout string
	Inspection                InspectionOptions
}

// CompareReleases admits both references before any I/O, then checks each
// complete metadata closure with the existing bounded inspector under one shared
// deadline. It never retrieves output layers, signatures or policy, and it
// returns no comparison unless both inspections succeed.
func CompareReleases(ctx context.Context, request ReleaseComparisonRequest) (releaseview.Comparison, error) {
	if request.Inspection.LayoutPath != "" {
		return releaseview.Comparison{}, e.New(e.CodeUsage, "release comparison takes a layout for each side, not one shared layout")
	}
	before, after := request.Inspection, request.Inspection
	before.LayoutPath, after.LayoutPath = request.BeforeLayout, request.AfterLayout
	if request.BeforeLayout == "" || request.AfterLayout == "" {
		for _, side := range []*InspectionOptions{&before, &after} {
			if side.LayoutPath != "" {
				side.Registry, side.WorkspaceRoot = delivery.RegistryOptions{}, ""
			}
		}
	}
	for _, side := range []struct {
		reference string
		options   InspectionOptions
	}{{request.Before, before}, {request.After, after}} {
		if err := admitReleaseInspection(ctx, side.reference, side.options); err != nil {
			return releaseview.Comparison{}, err
		}
	}
	timeout := request.Inspection.Timeout
	if timeout == 0 {
		timeout = defaultInspectionTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	previous, err := InspectRelease(ctx, request.Before, before)
	if err != nil {
		return releaseview.Comparison{}, err
	}
	next, err := InspectRelease(ctx, request.After, after)
	if err != nil {
		return releaseview.Comparison{}, err
	}
	comparison, err := releaseview.Compare(previous, next)
	if err != nil {
		return releaseview.Comparison{}, err
	}
	if err := ctx.Err(); err != nil {
		return releaseview.Comparison{}, e.Wrap(e.CodeTimeout, err, "release comparison canceled")
	}
	return comparison, nil
}
