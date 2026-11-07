// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"maps"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

func validateImageLocations(locations map[string]string) error {
	if len(locations) > 256 {
		return e.New(e.CodeInvalidCatalog, "imageLocations exceeds 256 source bindings")
	}
	seen := map[string]bool{}
	for _, source := range slices.Sorted(maps.Keys(locations)) {
		for i, value := range []string{source, locations[source]} {
			repository, err := name.NewRepository(value)
			if err != nil || value == "" || strings.ContainsAny(value, "@?#\\\r\n\x00 ") || strings.Contains(repository.RepositoryStr(), ":") || strings.Contains(value, "//") || strings.HasSuffix(value, "/") {
				return e.New(e.CodeInvalidCatalog, "imageLocations requires repository names without tags or digests: %q", value)
			}
			for _, part := range strings.Split(repository.RepositoryStr(), "/") {
				if part == "." || part == ".." {
					return e.New(e.CodeInvalidCatalog, "imageLocations rejects repository traversal")
				}
			}
			if i == 0 {
				if seen[repository.Name()] {
					return e.New(e.CodeInvalidCatalog, "imageLocations contains ambiguous source aliases")
				}
				seen[repository.Name()] = true
			} else if repository.RegistryStr() == "mirror.invalid" || !strings.Contains(value, "/") || (!strings.Contains(strings.SplitN(value, "/", 2)[0], ".") && !strings.Contains(strings.SplitN(value, "/", 2)[0], ":")) {
				return e.New(e.CodeInvalidCatalog, "imageLocations destination must be an explicit registry repository: %q", value)
			}
		}
	}
	return nil
}

// ResolveImageLocation applies only an exact authored source binding. The
// reserved mirror.invalid registry names unresolved keys, never a pull target.
// Bare legacy toolchain names require an explicit binding during migration.
// Native digests and measured local IDs pass through without registry parsing.
func (profile Runtime) ResolveImageLocation(reference string) (string, error) {
	if profile.ToolchainRegistry != "" {
		return "", e.New(e.CodeEngineCapability, "toolchainRegistry requires migration to imageLocations")
	}
	if err := validateImageLocations(profile.ImageLocations); err != nil {
		return "", err
	}
	if reference == "" || canonical.IsDigest(reference) {
		return reference, nil
	}
	source, digest, pinned := strings.Cut(reference, "@")
	if !pinned || !canonical.IsDigest(digest) {
		return "", e.New(e.CodeUnpinnedInput, "image location requires an exact image digest")
	}
	// A digest-pinned reference may retain a tag; bindings name repositories.
	// Strip only the tag, preserving registry ports and authored spelling.
	if colon := strings.LastIndex(source, ":"); colon > strings.LastIndex(source, "/") {
		source = source[:colon]
	}
	if location, ok := profile.ImageLocations[source]; ok {
		return location + "@" + digest, nil
	}
	if strings.HasPrefix(source, "mirror.invalid/") || (!strings.Contains(source, "/") && strings.HasPrefix(source, "nvkr-toolchain-")) {
		return "", e.New(e.CodeEngineCapability, "image source %q has no explicit runtime.imageLocations binding", source)
	}
	return reference, nil
}
