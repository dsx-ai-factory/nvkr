// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// InspectionOptions chooses local or registry metadata inspection. Payloads,
// signatures and policy are not verified. Zero Timeout selects ten minutes.
type InspectionOptions struct {
	LayoutPath    string
	WorkspaceRoot string
	Registry      delivery.RegistryOptions
	Timeout       time.Duration
}

// InspectRelease checks all bounded release metadata and evidence before returning
// a detached view, even when a caller later displays only one output. It never
// retrieves output layers or issues a verified publication capability.
func InspectRelease(ctx context.Context, reference string, options InspectionOptions) (delivery.PublicationDescription, error) {
	if ctx == nil {
		return delivery.PublicationDescription{}, e.New(e.CodeUsage, "release inspection requires a context")
	}
	if err := ctx.Err(); err != nil {
		return delivery.PublicationDescription{}, e.Wrap(e.CodeTimeout, err, "release inspection canceled")
	}
	if options.Timeout < 0 {
		return delivery.PublicationDescription{}, e.New(e.CodeUsage, "release inspection timeout must be positive")
	}
	if err := options.Registry.Validate(); err != nil {
		return delivery.PublicationDescription{}, err
	}
	if options.LayoutPath != "" && (options.WorkspaceRoot != "" || options.Registry != (delivery.RegistryOptions{})) {
		return delivery.PublicationDescription{}, e.New(e.CodeUsage, "local release inspection cannot use registry or workspace options")
	}
	if err := delivery.ValidatePublicationReference(reference); err != nil {
		return delivery.PublicationDescription{}, err
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	layout := options.LayoutPath
	if layout == "" {
		if options.WorkspaceRoot != "" {
			if err := os.MkdirAll(options.WorkspaceRoot, 0o700); err != nil {
				return delivery.PublicationDescription{}, e.Wrap(e.CodeImmutableViolation, err, "creating release inspection workspace")
			}
		}
		workspace, err := os.MkdirTemp(options.WorkspaceRoot, "nkd-release-inspection-*")
		if err != nil {
			return delivery.PublicationDescription{}, e.Wrap(e.CodeImmutableViolation, err, "creating release inspection workspace")
		}
		defer func() { _ = os.RemoveAll(workspace) }()
		layout = filepath.Join(workspace, "metadata")
		if _, err := delivery.PullMetadata(ctx, reference, layout, options.Registry); err != nil {
			if ctx.Err() != nil {
				return delivery.PublicationDescription{}, e.Wrap(e.CodeTimeout, ctx.Err(), "release inspection canceled")
			}
			return delivery.PublicationDescription{}, err
		}
	}
	description, err := delivery.InspectMetadataDescription(ctx, layout, reference)
	if err != nil {
		if ctx.Err() != nil {
			return delivery.PublicationDescription{}, e.Wrap(e.CodeTimeout, ctx.Err(), "release inspection canceled")
		}
		return delivery.PublicationDescription{}, err
	}
	return description, nil
}
