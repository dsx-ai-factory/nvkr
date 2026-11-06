// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"sort"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// ToolchainAccess classifies native execution images independently of output
// publication. Roots include a registry and repository path, without a trailing
// slash. Each root covers that repository and its path-component descendants.
// PublicRoots explicitly select anonymous pulls, not observed registry visibility.
type ToolchainAccess struct {
	PrivateRoots []string         `json:"privateRoots,omitempty"`
	PublicRoots  []string         `json:"publicRoots,omitempty"`
	Reader       *ToolchainReader `json:"reader,omitempty"`
}

// ToolchainReader names one independent GitHub secret and account variable.
// These references describe transport configuration, never secret values or grants.
type ToolchainReader struct {
	CredentialRef string `json:"credentialRef"`
	UsernameRef   string `json:"usernameRef"`
}

func (access *ToolchainAccess) Validate() error {
	if access == nil {
		return nil
	}
	if len(access.PrivateRoots)+len(access.PublicRoots) > 128 {
		return e.New(e.CodeInvalidCatalog, "toolchainAccess permits at most 128 roots")
	}
	var roots []string
	for index, group := range [][]string{access.PrivateRoots, access.PublicRoots} {
		previous := ""
		for _, root := range group {
			repository, err := name.NewRepository(root)
			// Docker Hub's explicit docker.io alias is canonical authored input;
			// the name library normalizes it to index.docker.io for matching.
			canonicalRoot := root
			if strings.HasPrefix(root, "docker.io/") {
				canonicalRoot = "index.docker.io/" + strings.TrimPrefix(root, "docker.io/")
			}
			if err != nil || root <= previous || repository.Name() != canonicalRoot ||
				root != strings.ToLower(root) || strings.HasSuffix(root, "/") || strings.Contains(root, "//") ||
				strings.ContainsAny(root, "@?#\\\r\n\x00") || strings.Contains(repository.RepositoryStr(), ":") {
				return e.New(e.CodeInvalidCatalog, "toolchainAccess roots must be sorted unique canonical registry/repository paths: %q", root)
			}
			for _, part := range strings.Split(repository.RepositoryStr(), "/") {
				if part == "." || part == ".." {
					return e.New(e.CodeInvalidCatalog, "toolchainAccess root contains a traversal component: %q", root)
				}
			}
			if index == 0 && repository.RegistryStr() != "ghcr.io" {
				return e.New(e.CodeEngineCapability, "private toolchainAccess roots support only ghcr.io: %q", root)
			}
			roots = append(roots, repository.Name())
			previous = root
		}
	}
	sort.Strings(roots)
	for i, root := range roots {
		for _, previous := range roots[:i] {
			if repositoryWithin(root, previous) {
				return e.New(e.CodeInvalidCatalog, "toolchainAccess roots overlap: %q and %q", previous, root)
			}
		}
	}
	if reader := access.Reader; reader != nil {
		for _, ref := range []string{reader.CredentialRef, reader.UsernameRef} {
			if !githubCredentialRefPattern.MatchString(ref) || strings.HasPrefix(strings.ToUpper(strings.TrimPrefix(ref, "github:")), "GITHUB_") {
				return e.New(e.CodeInvalidCatalog, "toolchainAccess reader requires independent github:NAME credentialRef and usernameRef")
			}
		}
		if len(access.PrivateRoots) == 0 {
			return e.New(e.CodeInvalidCatalog, "toolchainAccess reader requires privateRoots")
		}
	}
	return nil
}

// ClassifyImage admits an exact image to precisely one declared access class.
// The boolean describes private transport; it is not credential or execution authority.
func (access *ToolchainAccess) ClassifyImage(image string) (bool, error) {
	if err := access.Validate(); err != nil {
		return false, err
	}
	pin, err := name.NewDigest(image)
	if err != nil || !canonical.IsDigest(pin.DigestStr()) {
		return false, e.New(e.CodeUnpinnedInput, "native toolchain image must be digest-pinned: %q", image)
	}
	for _, part := range strings.Split(pin.RepositoryStr(), "/") {
		if part == "" || part == "." || part == ".." {
			return false, e.New(e.CodeInvalidCatalog, "native toolchain image has a noncanonical repository path: %q", image)
		}
	}
	if access != nil {
		for index, group := range [][]string{access.PrivateRoots, access.PublicRoots} {
			for _, root := range group {
				repository, err := name.NewRepository(root)
				if err != nil {
					return false, e.Wrap(e.CodeInvalidCatalog, err, "toolchainAccess root")
				}
				if repositoryWithin(pin.Context().Name(), repository.Name()) {
					return index == 0, nil
				}
			}
		}
	}
	return false, e.New(e.CodeEngineCapability, "native toolchain image %q has no declared toolchainAccess root", image)
}

func repositoryWithin(repository, root string) bool {
	return repository == root || strings.HasPrefix(repository, root+"/")
}
