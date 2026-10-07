// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalogmaintenance

import (
	"context"
	"slices"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Git insteadOf matches prefixes, not repository identities. Admit every source
// the pin transaction or catalog validation can resolve before compiling (which
// revalidates pin sets); prepare also admits the compiled execution fetches.
func admitSourceCheckoutCatalogs(ctx context.Context, config Config, sources map[string]catalog.Source, catalogs map[string]bool) error {
	if config.SourceCheckout == nil {
		return nil
	}
	if err := admitSourceCheckoutSources(config.SourceCheckout, sources, config.Sources); err != nil {
		return err
	}
	paths := make([]string, 0, len(catalogs))
	for path := range catalogs {
		if path != config.Catalog {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	for _, path := range paths {
		sources, err := catalog.ReadSourcesContext(ctx, catalog.Dir(path))
		if err != nil {
			return err
		}
		if err := admitSourceCheckoutSources(config.SourceCheckout, sources, nil); err != nil {
			return err
		}
	}
	return nil
}

func admitSourceCheckoutSources(checkout *SourceCheckout, sources map[string]catalog.Source, selected []string) error {
	pending := append([]string(nil), selected...)
	// Catalog loading validates every declared pin set, including those that do
	// not supply a selected output. Nested member sources are resolved as well.
	for name, source := range sources {
		if source.PinSet != nil {
			pending = append(pending, name)
		}
	}
	seen := map[string]bool{}
	for len(pending) != 0 {
		slices.Sort(pending)
		name := pending[0]
		pending = pending[1:]
		if seen[name] {
			continue
		}
		seen[name] = true
		source := sources[name]
		if err := admitSourceCheckoutRepo(checkout, source.Repo); err != nil {
			return err
		}
		if source.PinSet != nil {
			for _, member := range source.PinSet.Members {
				if member.Source != "" {
					pending = append(pending, member.Source)
				}
			}
		}
	}
	return nil
}

func admitSourceCheckoutRepo(checkout *SourceCheckout, repo string) error {
	if checkout == nil {
		return nil
	}
	prefix := "https://github.com/" + checkout.Repository
	if strings.HasPrefix(repo, prefix) && !matchesSourceCheckout(checkout, repo) {
		return e.New(e.CodeEngineCapability, "source checkout URL prefix %q would redirect distinct repository %q", prefix, repo)
	}
	return nil
}

// Use the same repository spelling rules for root selection and prefix admission.
func matchesSourceCheckout(checkout *SourceCheckout, repo string) bool {
	if checkout == nil {
		return false
	}
	// Git accepts the same repository with an optional .git and trailing slash.
	identity := strings.TrimSuffix(strings.TrimSuffix(repo, "/"), ".git")
	return identity == "https://github.com/"+checkout.Repository
}
