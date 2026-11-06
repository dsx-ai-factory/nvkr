// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// PullMetadataCached materializes a private metadata layout using an optional
// cache. The lifecycle reader can use VisitCachedMetadata to inspect under the
// cache lock directly, avoiding a redundant copy and inspection of the closure.
func PullMetadataCached(ctx context.Context, reference, out, cacheRoot string, request MetadataPullOptions) (Inspection, error) {
	if cacheRoot == "" {
		return PullMetadataWithOptions(ctx, reference, out, request)
	}
	var result Inspection
	err := VisitCachedMetadata(ctx, reference, cacheRoot, request, func(ctx context.Context, path string, inspected Inspection) error {
		if err := copyMetadataCache(ctx, path, out, inspected); err != nil {
			return err
		}
		result = inspected
		return nil
	})
	return result, err
}

// VisitCachedMetadata rechecks a content-addressed cache and holds its per-root
// lock until visit returns. The path is valid only during the callback. Every hit
// verifies all blob digests, sizes and the complete closure; neither cached bytes
// nor the supplied inspection carry signature or policy authority. A miss or
// corrupt entry is fetched into a checked stage before atomic publication.
func VisitCachedMetadata(ctx context.Context, reference, cacheRoot string, request MetadataPullOptions, visit func(context.Context, string, Inspection) error) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if err := ValidatePublicationReference(reference); err != nil {
		return err
	}
	if err := request.Registry.Validate(); err != nil {
		return err
	}
	if cacheRoot == "" || visit == nil {
		return e.New(e.CodeUsage, "metadata cache requires a directory and visitor")
	}
	if err := metadataCacheSupport(); err != nil {
		return err
	}
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Hour)
		defer cancel()
	}
	_, digest, _ := strings.Cut(reference, "@")
	entry := filepath.Join(cacheRoot, strings.TrimPrefix(digest, "sha256:"))
	if err := os.MkdirAll(cacheRoot, 0o700); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating metadata cache")
	}
	unlock, err := lockMetadataCache(ctx, entry+".lock")
	if err != nil {
		return err
	}
	defer unlock()
	ctx = freshMetadataReads(ctx)
	if cached, err := inspectMetadataProgress(ctx, entry, false, digest, request.Progress); err == nil && cached.RootDigest == digest {
		return visit(ctx, entry, cached)
	}
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	// Discard any bytes retained while checking a rejected entry.
	ctx = freshMetadataReads(ctx)
	stage, err := os.MkdirTemp(cacheRoot, ".metadata-cache-*")
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "staging metadata cache")
	}
	defer func() { _ = os.RemoveAll(stage) }()
	replacement := filepath.Join(stage, "layout")
	pulled, err := PullMetadataWithOptions(ctx, reference, replacement, request)
	if err != nil {
		return err
	}
	// Cache data is expendable, not durable authority. A crash can lose an entry;
	// it cannot make a partial stage acceptable. Readers hold the same kernel lock.
	if _, err := os.Lstat(entry); err == nil {
		if err := os.Rename(entry, filepath.Join(stage, "rejected")); err != nil {
			return e.Wrap(e.CodeImmutableViolation, err, "retiring invalid metadata cache")
		}
	} else if !os.IsNotExist(err) {
		return e.Wrap(e.CodeImmutableViolation, err, "checking metadata cache destination")
	}
	if err := os.Rename(replacement, entry); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "publishing metadata cache")
	}
	return visit(ctx, entry, pulled)
}

func copyMetadataCache(ctx context.Context, source, out string, inspected Inspection) error {
	sources := make(map[string]string, len(inspected.Root.Objects))
	for _, object := range inspected.Root.Objects {
		sources[object.Name] = source
	}
	_, err := buildMetadataRoot(ctx, out, inspected.Root, sources, inspected.RootDigest)
	return err
}
