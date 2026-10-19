// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/cistore"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/google/go-containerregistry/pkg/name"
)

const githubRepositoryTokenEnv = "NVKR_CI_REPOSITORY_TOKEN"

// Hosted GHCR publication uses the same package-policy owner as private CI
// transport, with the publisher's distinct credential. Non-GitHub and generic
// registry execution retain their existing transport contract.
func admitGitHubPublication(ctx context.Context, target Target, source string,
	admit func(context.Context, string, []string, cistore.Options) error,
) (string, error) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		return "", nil
	}
	parse := []name.Option{name.StrictValidation}
	if target.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	repository, err := name.NewRepository(target.Ref, parse...)
	if err != nil {
		tag, tagErr := name.NewTag(target.Ref, parse...)
		if tagErr != nil {
			return "", e.Wrap(e.CodeUsage, err, "parse hosted publication target")
		}
		repository = tag.Context()
	}
	if repository.RegistryStr() != "ghcr.io" {
		return "", nil
	}
	if os.Getenv("GITHUB_SERVER_URL") != "https://github.com" {
		return "", e.New(e.CodeEngineCapability, "private GHCR publication requires the canonical GitHub runner context")
	}
	options := cistore.Options{
		Token:           os.Getenv("NVKR_CREDENTIAL"),
		Username:        os.Getenv("NVKR_REGISTRY_USER"),
		RepositoryToken: os.Getenv(githubRepositoryTokenEnv),
	}
	if options.RepositoryToken == "" {
		return "", e.New(e.CodeTrustPolicyMissing, "hosted GHCR publication requires its separate repository metadata credential")
	}
	if err := admit(ctx, os.Getenv("GITHUB_REPOSITORY"), []string{repository.Name()}, options); err != nil {
		return "", err
	}
	// Hash only the bounded index. The existing descriptive owner checks all
	// control documents and exact sibling references without opening payloads.
	index, err := heldfile.OpenContext(ctx, filepath.Join(source, "index.json"), heldfile.Options{Label: "private-publication-index", MaxBytes: 8 << 20})
	if err != nil {
		return "", classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "open private publication index"))
	}
	digest := index.Digest()
	if err := index.Close(); err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "close private publication index")
	}
	description, err := delivery.InspectMetadataDescription(ctx, source, repository.Digest(digest).Name())
	if err != nil {
		return "", classifyContext(ctx, e.Wrap(e.CodeEngineCapability, err, "private hosted GHCR publication requires a checked pipeline metadata layout"))
	}
	repositories := []string{repository.Name()}
	appendRepository := func(reference string) error {
		ref, err := name.NewDigest(reference, name.StrictValidation)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "parse metadata-bound publication repository")
		}
		repositories = append(repositories, ref.Context().Name())
		return nil
	}
	for _, output := range description.Outputs {
		if err := appendRepository(output.Reference); err != nil {
			return "", err
		}
	}
	for _, evidence := range description.Evidence {
		if err := appendRepository(evidence.Reference); err != nil {
			return "", err
		}
	}
	if err := admit(ctx, os.Getenv("GITHUB_REPOSITORY"), uniqueSorted(repositories), options); err != nil {
		return "", err
	}
	return digest, nil
}
