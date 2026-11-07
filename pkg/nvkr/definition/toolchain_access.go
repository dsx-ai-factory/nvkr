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

// ProductionRegistryWriteCredential identifies the fixed production publisher
// credential so access declarations cannot reuse it as a toolchain reader.
const ProductionRegistryWriteCredential = "github:NVKR_REGISTRY_WRITE"

// ToolchainAccess classifies execution images independently of output
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

// ToolchainImageClassifier is an immutable view of validated, parsed roots for
// one admission pass. It owns its root storage and grants no credential authority.
// Its zero value admits no images.
type ToolchainImageClassifier struct {
	roots []toolchainRoot
}

type toolchainRoot struct {
	repository string
	private    bool
}

// Validate checks root partitioning and reader reference syntax.
// Environment admission additionally excludes publication writer references.
func (access *ToolchainAccess) Validate() error {
	_, err := access.ImageClassifier()
	return err
}

// ImageClassifier validates the declaration and freezes its parsed roots for
// repeated classification within an owned admission pass, without a shared cache.
func (access *ToolchainAccess) ImageClassifier() (ToolchainImageClassifier, error) {
	if access == nil {
		return ToolchainImageClassifier{}, nil
	}
	if len(access.PrivateRoots)+len(access.PublicRoots) > 128 {
		return ToolchainImageClassifier{}, e.New(e.CodeInvalidCatalog, "toolchainAccess permits at most 128 roots")
	}
	var roots []toolchainRoot
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
				return ToolchainImageClassifier{}, e.New(e.CodeInvalidCatalog, "toolchainAccess roots must be sorted unique canonical registry/repository paths: %q", root)
			}
			for _, part := range strings.Split(repository.RepositoryStr(), "/") {
				if part == "." || part == ".." {
					return ToolchainImageClassifier{}, e.New(e.CodeInvalidCatalog, "toolchainAccess root contains a traversal component: %q", root)
				}
			}
			if index == 0 && repository.RegistryStr() != "ghcr.io" {
				return ToolchainImageClassifier{}, e.New(e.CodeEngineCapability, "private toolchainAccess roots support only ghcr.io: %q", root)
			}
			roots = append(roots, toolchainRoot{repository: repository.Name(), private: index == 0})
			previous = root
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].repository < roots[j].repository })
	for i, root := range roots {
		for _, previous := range roots[:i] {
			if repositoryWithin(root.repository, previous.repository) {
				return ToolchainImageClassifier{}, e.New(e.CodeInvalidCatalog, "toolchainAccess roots overlap: %q and %q", previous.repository, root.repository)
			}
		}
	}
	if reader := access.Reader; reader != nil {
		for _, ref := range []string{reader.CredentialRef, reader.UsernameRef} {
			if !githubCredentialRefPattern.MatchString(ref) || strings.HasPrefix(strings.ToUpper(strings.TrimPrefix(ref, "github:")), "GITHUB_") {
				return ToolchainImageClassifier{}, e.New(e.CodeInvalidCatalog, "toolchainAccess reader requires independent github:NAME credentialRef and usernameRef")
			}
		}
		if len(access.PrivateRoots) == 0 {
			return ToolchainImageClassifier{}, e.New(e.CodeInvalidCatalog, "toolchainAccess reader requires privateRoots")
		}
	}
	return ToolchainImageClassifier{roots: roots}, nil
}

// ClassifyImage admits an exact image to precisely one declared access class.
// The boolean describes private transport; it is not credential or execution authority.
func (access *ToolchainAccess) ClassifyImage(image string) (bool, error) {
	classifier, err := access.ImageClassifier()
	if err != nil {
		return false, err
	}
	return classifier.ClassifyImage(image)
}

// ClassifyImage checks an exact pin against the validated root snapshot. It
// returns a transport classification, never credential or execution authority.
func (classifier ToolchainImageClassifier) ClassifyImage(image string) (bool, error) {
	pin, err := name.NewDigest(image)
	if err != nil || !canonical.IsDigest(pin.DigestStr()) {
		return false, e.New(e.CodeUnpinnedInput, "execution image must be digest-pinned: %q", image)
	}
	for _, part := range strings.Split(pin.RepositoryStr(), "/") {
		if part == "" || part == "." || part == ".." {
			return false, e.New(e.CodeInvalidCatalog, "execution image has a noncanonical repository path: %q", image)
		}
	}
	for _, root := range classifier.roots {
		if repositoryWithin(pin.Context().Name(), root.repository) {
			return root.private, nil
		}
	}
	return false, e.New(e.CodeEngineCapability, "execution image %q has no declared toolchainAccess root", image)
}

func repositoryWithin(repository, root string) bool {
	return repository == root || strings.HasPrefix(repository, root+"/")
}
