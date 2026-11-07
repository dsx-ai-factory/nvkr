// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// InspectionOptions chooses local or registry metadata inspection. Payloads,
// signatures and policy are not verified. Zero Timeout selects one hour.
type InspectionOptions struct {
	LayoutPath    string
	WorkspaceRoot string
	Registry      delivery.RegistryOptions
	Timeout       time.Duration
	// CacheRoot overrides the user cache directory. DisableCache forces a fresh pull.
	CacheRoot    string
	DisableCache bool
	Progress     func(delivery.MetadataProgress)
}

const defaultInspectionTimeout = time.Hour

// InspectRelease checks all bounded release metadata and evidence before returning
// a detached view, even when a caller later displays only one output. It never
// retrieves output layers or issues a verified publication capability.
func InspectRelease(ctx context.Context, reference string, options InspectionOptions) (delivery.PublicationDescription, error) {
	return inspectReleaseMetadata(ctx, reference, options, nil)
}

// The optional visitor authenticates the exact evidence bytes bound by the
// requested root before its private metadata workspace is removed.
func inspectReleaseMetadata(ctx context.Context, reference string, options InspectionOptions,
	verifyEvidence func(context.Context, string, []byte) error,
) (delivery.PublicationDescription, error) {
	if err := admitReleaseInspection(ctx, reference, options); err != nil {
		return delivery.PublicationDescription{}, err
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultInspectionTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	layout := options.LayoutPath
	if layout == "" {
		cacheRoot, err := metadataCacheRoot(options)
		if err != nil {
			return delivery.PublicationDescription{}, err
		}
		if cacheRoot != "" {
			return inspectCachedRelease(ctx, reference, cacheRoot, options, verifyEvidence)
		}
		if options.WorkspaceRoot != "" {
			if err := os.MkdirAll(options.WorkspaceRoot, 0o700); err != nil {
				return delivery.PublicationDescription{}, e.Wrap(e.CodeImmutableViolation, err, "creating release inspection workspace")
			}
		}
		workspace, err := os.MkdirTemp(options.WorkspaceRoot, "nvkr-release-inspection-*")
		if err != nil {
			return delivery.PublicationDescription{}, e.Wrap(e.CodeImmutableViolation, err, "creating release inspection workspace")
		}
		defer func() { _ = os.RemoveAll(workspace) }()
		layout = filepath.Join(workspace, "metadata")
		if _, err := delivery.PullMetadataWithOptions(ctx, reference, layout, delivery.MetadataPullOptions{Registry: options.Registry, Progress: options.Progress}); err != nil {
			if ctx.Err() != nil {
				return delivery.PublicationDescription{}, e.Wrap(e.CodeTimeout, ctx.Err(), "release inspection canceled")
			}
			return delivery.PublicationDescription{}, err
		}
	}
	return inspectReleaseLayout(ctx, reference, layout, verifyEvidence)
}

func inspectReleaseLayout(ctx context.Context, reference, layout string, verifyEvidence func(context.Context, string, []byte) error) (delivery.PublicationDescription, error) {
	description, err := delivery.InspectMetadataDescription(ctx, layout, reference)
	if err != nil {
		if ctx.Err() != nil {
			return delivery.PublicationDescription{}, e.Wrap(e.CodeTimeout, ctx.Err(), "release inspection canceled")
		}
		return delivery.PublicationDescription{}, err
	}
	if err := requireCompleteComposition(description); err != nil {
		return delivery.PublicationDescription{}, err
	}
	if verifyEvidence != nil {
		if err := delivery.VisitMetadataEvidence(ctx, layout, description.RootDigest, verifyEvidence); err != nil {
			return delivery.PublicationDescription{}, err
		}
	}
	return description, nil
}

func admitReleaseInspection(ctx context.Context, reference string, options InspectionOptions) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "release inspection requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "release inspection canceled")
	}
	if options.Timeout < 0 {
		return e.New(e.CodeUsage, "release inspection timeout must be positive")
	}
	if err := options.Registry.Validate(); err != nil {
		return err
	}
	if options.LayoutPath != "" && (options.WorkspaceRoot != "" || options.Registry != (delivery.RegistryOptions{})) {
		return e.New(e.CodeUsage, "local release inspection cannot use registry or workspace options")
	}
	return delivery.ValidatePublicationReference(reference)
}

// NVKR_METADATA_CACHE=off disables persistent reuse, including SDK consumers.
// An explicit path overrides the OS user-cache location without changing identity.
func metadataCacheRoot(options InspectionOptions) (string, error) {
	if options.DisableCache {
		return "", nil
	}
	root := options.CacheRoot
	if root == "" {
		root = os.Getenv("NVKR_METADATA_CACHE")
	}
	if root == "off" {
		return "", nil
	}
	if root != "" {
		return root, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", e.Wrap(e.CodeUsage, err, "locating release metadata cache; set NVKR_METADATA_CACHE or disable caching")
	}
	return filepath.Join(base, "nvkr", "release-metadata", "v1"), nil
}

// Capture owned, already bounded evidence bytes under the cache lock. External
// cryptographic work runs after the lock is released and never rereads the cache.
func inspectCachedRelease(ctx context.Context, reference, cacheRoot string, options InspectionOptions, verifyEvidence func(context.Context, string, []byte) error) (delivery.PublicationDescription, error) {
	type evidenceSnapshot struct {
		subject string
		raw     []byte
	}
	var evidence []evidenceSnapshot
	var capture func(context.Context, string, []byte) error
	if verifyEvidence != nil {
		capture = func(_ context.Context, subject string, raw []byte) error {
			evidence = append(evidence, evidenceSnapshot{subject: subject, raw: raw})
			return nil
		}
	}
	var description delivery.PublicationDescription
	err := delivery.VisitCachedMetadata(ctx, reference, cacheRoot, delivery.MetadataPullOptions{Registry: options.Registry, Progress: options.Progress}, func(ctx context.Context, layout string, _ delivery.Inspection) error {
		var err error
		description, err = inspectReleaseLayout(ctx, reference, layout, capture)
		return err
	})
	if err != nil {
		return delivery.PublicationDescription{}, err
	}
	for i, item := range evidence {
		if err := authenticityContext(ctx); err != nil {
			return delivery.PublicationDescription{}, err
		}
		if err := verifyEvidence(ctx, item.subject, item.raw); err != nil {
			return delivery.PublicationDescription{}, err
		}
		evidence[i].raw = nil
	}
	return description, nil
}
