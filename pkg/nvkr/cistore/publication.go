// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// AdmitPrivatePublication checks the current access policy of every exact GHCR
// package before the caller writes payloads. Credentials are explicit publisher
// credentials, independent of CI transport credentials. Missing packages may be
// seeded with metadata only and must then pass the same API checks. The operation
// issues no build/release authority and cannot lock future external visibility or
// prove independent package grants. Call it at the owning publication boundary.
func AdmitPrivatePublication(ctx context.Context, sourceRepository string, repositories []string, opts Options) error {
	return newBackend().admitPrivatePublication(ctx, sourceRepository, repositories, opts)
}

func (b *backend) admitPrivatePublication(ctx context.Context, sourceRepository string, repositories []string, opts Options) error {
	ctx, cancel, opts, err := admitSource(ctx, sourceRepository, opts)
	if err != nil {
		return err
	}
	defer cancel()
	if len(repositories) == 0 || len(repositories) > 512 {
		return e.New(e.CodeUsage, "private publication requires a bounded nonempty exact package set")
	}
	ordered := slices.Clone(repositories)
	slices.Sort(ordered)
	parsed := make([]name.Repository, 0, len(ordered))
	owner := strings.ToLower(strings.Split(sourceRepository, "/")[0])
	for i, repository := range ordered {
		ref, err := name.NewRepository(repository, name.StrictValidation)
		if err != nil || ref.RegistryStr() != "ghcr.io" || ref.Name() != repository || !safePath(ref.RepositoryStr()) || !strings.HasPrefix(ref.RepositoryStr(), owner+"/") || (i > 0 && ordered[i-1] == repository) {
			return e.New(e.CodeUsage, "private publication requires unique canonical GHCR packages in the authenticated source owner namespace")
		}
		parsed = append(parsed, ref)
	}
	source, err := b.source(ctx, sourceRepository, opts)
	if err != nil {
		return err
	}
	missing := []name.Repository{}
	// Inspect the whole set before any metadata seed. One public or unknown
	// package prevents all writes even when an earlier package is missing.
	for _, ref := range parsed {
		packageName := strings.TrimPrefix(ref.RepositoryStr(), owner+"/")
		exists, err := b.checkPackagePrivacy(ctx, packageName, source, opts, false)
		if err != nil {
			return err
		}
		if !exists {
			missing = append(missing, ref)
		}
	}
	for _, ref := range missing {
		if err := b.ensurePrivatePackage(ctx, ref, source, opts, false); err != nil {
			return err
		}
	}
	// Seeding may take time; every exact package must still pass before return.
	for _, ref := range parsed {
		packageName := strings.TrimPrefix(ref.RepositoryStr(), owner+"/")
		if exists, err := b.checkPackagePrivacy(ctx, packageName, source, opts, false); err != nil {
			return err
		} else if !exists {
			return e.New(e.CodeTrustPolicyMissing, "private publication package disappeared before payload admission")
		}
	}
	return nil
}
