// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// PublicationOptions describes the selected credential and visibility policy.
// It is not proof of package grants: GitHub authenticates package identity,
// association and visibility; Actions roles and inheritance are operator-owned.
type PublicationOptions struct {
	Options
	PlatformToken       bool
	AllowPublicPackages bool
}

// AdmitPublication checks every destination before payload writes. The platform
// token profile requires existing packages linked to the publishing repository;
// it never creates a package or probes deletion/restoration permissions.
func AdmitPublication(ctx context.Context, sourceRepository string, repositories []string, opts PublicationOptions) error {
	return newBackend().admitPublication(ctx, sourceRepository, repositories, opts)
}

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
	return b.admitPublication(ctx, sourceRepository, repositories, PublicationOptions{Options: opts})
}

func (b *backend) admitPublication(ctx context.Context, sourceRepository string, repositories []string, opts PublicationOptions) error {
	ctx, cancel, credentials, err := admitSource(ctx, sourceRepository, opts.Options)
	if err != nil {
		return err
	}
	defer cancel()
	opts.Options = credentials
	if len(repositories) == 0 || len(repositories) > 512 {
		return e.New(e.CodeUsage, "publication requires a bounded nonempty exact package set")
	}
	ordered := slices.Clone(repositories)
	slices.Sort(ordered)
	parsed := make([]name.Repository, 0, len(ordered))
	owner := strings.ToLower(strings.Split(sourceRepository, "/")[0])
	for i, repository := range ordered {
		ref, err := name.NewRepository(repository, name.StrictValidation)
		if err != nil || ref.RegistryStr() != "ghcr.io" || ref.Name() != repository || !safePath(ref.RepositoryStr()) || !strings.HasPrefix(ref.RepositoryStr(), owner+"/") || (i > 0 && ordered[i-1] == repository) {
			return e.New(e.CodeUsage, "publication requires unique canonical GHCR packages in the authenticated source owner namespace")
		}
		parsed = append(parsed, ref)
		if opts.PlatformToken && !strings.HasPrefix(ref.RepositoryStr(), strings.ToLower(sourceRepository)+"/") {
			return e.New(e.CodeUsage, "platform-token publication requires the source repository's GHCR namespace")
		}
	}
	source, err := b.source(ctx, sourceRepository, opts.Options)
	if err != nil {
		return err
	}
	missing := []name.Repository{}
	// Inspect the whole set before any metadata seed. One inadmissible
	// package prevents all writes even when an earlier package is missing.
	for _, ref := range parsed {
		packageName := strings.TrimPrefix(ref.RepositoryStr(), owner+"/")
		exists, err := b.checkPublicationPackage(ctx, packageName, source, opts)
		if err != nil {
			return err
		}
		if !exists {
			missing = append(missing, ref)
		}
	}
	for _, ref := range missing {
		if err := b.ensurePrivatePackage(ctx, ref, source, opts.Options, true); err != nil {
			return err
		}
	}
	// Seeding may take time; every exact package must still pass before return.
	for _, ref := range parsed {
		packageName := strings.TrimPrefix(ref.RepositoryStr(), owner+"/")
		if exists, err := b.checkPublicationPackage(ctx, packageName, source, opts); err != nil {
			return err
		} else if !exists {
			return e.New(e.CodeTrustPolicyMissing, "publication package disappeared before payload admission")
		}
	}
	return nil
}

func (b *backend) checkPublicationPackage(ctx context.Context, packageName string, source repositoryRecord, opts PublicationOptions) (bool, error) {
	if !opts.PlatformToken {
		// The public opt-in never relaxes an independent credential's policy.
		return b.checkPackagePrivacy(ctx, packageName, source, opts.Options, true)
	}
	kind := "users"
	if source.Owner.Type == "Organization" {
		kind = "orgs"
	}
	var record packageRecord
	status, err := b.get(ctx, "/"+kind+"/"+url.PathEscape(source.Owner.Login)+"/packages/container/"+url.PathEscape(packageName), opts.Token, &record)
	if err != nil {
		return false, err
	}
	if status == http.StatusNotFound {
		return false, e.New(e.CodeTrustPolicyMissing, "platform-token destination package %q must be initialized and linked by its operator before publication", packageName)
	}
	if status != http.StatusOK {
		return false, e.New(e.CodeFetchFailed, "publication package request returned HTTP %d", status)
	}
	if record.Name != packageName || record.PackageType != "container" || !strings.EqualFold(record.Owner.Login, source.Owner.Login) {
		return false, e.New(e.CodeTrustPolicyMissing, "platform-token destination package identity was not authenticated by GitHub")
	}
	if !privateVisibility(record.Visibility) && !(opts.AllowPublicPackages && record.Visibility == "public") {
		return false, e.New(e.CodeTrustPolicyMissing, "destination package %q requires private/internal visibility or publication.allowPublicPackages for public visibility", packageName)
	}
	var linked repositoryRecord
	if err := json.Unmarshal(record.Repository, &linked); err != nil || linked.ID != source.ID || linked.FullName != source.FullName {
		return false, e.New(e.CodeTrustPolicyMissing, "platform-token destination package %q must be linked to the authenticated publishing repository", packageName)
	}
	return true, nil
}
