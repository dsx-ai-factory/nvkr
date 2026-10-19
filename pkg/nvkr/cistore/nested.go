// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// NestedArchive describes an exact archive member and permitted inner roots.
// The caller derives this selection from its admitted execution plan. It is not
// an authenticity receipt: the store still authenticates the complete carrier.
type NestedArchive struct {
	Member  string
	Allowed []string
}

// DownloadNested restores selected inner roots without materializing their TAR
// carrier. Request, privacy, manifest/config and complete byte checks are the
// same as Download. Existing destination roots are never replaced.
func DownloadNested(ctx context.Context, request Request, locator Locator, destination string, selection NestedArchive, options Options) error {
	return newBackend().downloadNested(ctx, request, locator, destination, selection, options)
}

func (b *backend) downloadNested(ctx context.Context, request Request, locator Locator, destination string, selection NestedArchive, options Options) error {
	clean, err := admitNestedArchive(selection)
	if err != nil {
		return err
	}
	if err := matchNestedLocator(locator, clean); err != nil {
		return err
	}
	if locator.Metadata != nil {
		return restoreMetadata(ctx, request, locator, destination, &clean)
	}
	return b.downloadRestored(ctx, request, locator, destination, options,
		func(ctx context.Context, layers []v1.Layer, manifest *v1.Manifest, locator Locator, destination string) error {
			return restoreNestedLayers(ctx, layers, manifest, locator, destination, clean)
		})
}

func admitNestedArchive(selection NestedArchive) (NestedArchive, error) {
	if !safePath(selection.Member) || len(selection.Allowed) == 0 || len(selection.Allowed) > 4096 {
		return NestedArchive{}, e.New(e.CodeUsage, "nested CI transport requires an exact member and bounded inner roots")
	}
	clean := NestedArchive{Member: selection.Member, Allowed: slices.Clone(selection.Allowed)}
	slices.Sort(clean.Allowed)
	seen := make(map[string]bool, len(clean.Allowed))
	for _, root := range clean.Allowed {
		if !safePath(root) || seen[root] {
			return NestedArchive{}, e.New(e.CodeUsage, "nested CI transport requires unique canonical inner roots")
		}
		for ancestor := path.Dir(root); ancestor != "."; ancestor = path.Dir(ancestor) {
			if seen[ancestor] {
				return NestedArchive{}, e.New(e.CodeUsage, "nested CI transport inner roots overlap")
			}
		}
		seen[root] = true
	}
	return clean, nil
}

func matchNestedLocator(locator Locator, selection NestedArchive) error {
	if locator.Metadata != nil && !slices.Equal(selection.Allowed, []string{locator.Metadata.Root}) {
		return e.New(e.CodeVerifyFailed, "public metadata differs from the complete Plan selection")
	}
	prefix := path.Dir(selection.Member)
	if prefix == "." {
		prefix = ""
	}
	if !slices.Equal(locator.Paths, []string{selection.Member}) || locator.StripPrefix != prefix {
		return e.New(e.CodeVerifyFailed, "CI locator does not bind the selected nested archive member")
	}
	return nil
}

func restoreNestedLayers(ctx context.Context, layers []v1.Layer, manifest *v1.Manifest, locator Locator, destination string, selection NestedArchive) error {
	if err := validateChunkManifest(manifest, locator.ArchiveSize); err != nil {
		return err
	}
	if len(layers) != len(manifest.Layers) {
		return e.New(e.CodeVerifyFailed, "CI carrier chunk count mismatch")
	}
	if err := checkNestedDestinations(ctx, destination, selection.Allowed); err != nil {
		return err
	}
	temporary, err := os.MkdirTemp(destination, ".nvkr-ci-nested-*")
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating private nested CI staging")
	}
	defer os.RemoveAll(temporary)
	restored := filepath.Join(temporary, "restored")
	if err := unpackChunkAttempts(ctx, layers, manifest, restored, func(source *chunkReader) error {
		return transportarchive.UnpackNestedStream(ctx, source, locator.ArchiveSize, locator.ArchiveDigest,
			selection.Member, restored, selection.Allowed)
	}, waitChunkRetry, os.RemoveAll); err != nil {
		return err
	}
	if err := transportarchive.PublishRestoredRoots(ctx, restored, destination, selection.Allowed); err != nil {
		return err
	}
	if err := os.RemoveAll(temporary); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "removing completed nested CI staging")
	}
	return nil
}

// Check every destination before payload transfer, without creating parents.
// OpenChild rejects symlink/non-directory ancestors, and publication repeats the
// checks with no-replace renames to close the race after this early admission.
func checkNestedDestinations(ctx context.Context, destination string, roots []string) error {
	root, _, err := securepath.OpenDirectory(destination)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "holding nested CI destination")
	}
	defer root.Close()
	for _, relative := range roots {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "checking nested CI destination canceled")
		}
		if err := checkNestedDestination(root, strings.Split(relative, "/")); err != nil {
			return err
		}
	}
	return nil
}

func checkNestedDestination(parent *os.File, parts []string) error {
	child, _, err := securepath.OpenChild(parent, parts[0], len(parts) > 1)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "nested CI destination is unavailable or unsafe")
	}
	defer child.Close()
	if len(parts) == 1 {
		return e.New(e.CodeImmutableViolation, "nested CI destination already exists")
	}
	return checkNestedDestination(child, parts[1:])
}
