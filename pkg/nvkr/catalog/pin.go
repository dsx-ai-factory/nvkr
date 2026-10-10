// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import "context"

const (
	lockAPIVersion = "nvkr.dev/lock/v1"
	lockKind       = "PinLock"
)

// readSources returns the declared sources without requiring the lock.
func readSources(p DataProvider) (map[string]Source, error) {
	reg, err := readRegistry(p)
	if err != nil {
		return nil, err
	}
	return reg.Sources, nil
}

// ReadSourcesContext is ReadSources through the bounded context-aware provider
// contract.
func ReadSourcesContext(ctx context.Context, p DataProvider) (map[string]Source, error) {
	bound, err := BindContext(ctx, p)
	if err != nil {
		return nil, err
	}
	sources, err := readSources(bound)
	return sources, normalizeCatalogContextError(ctx, err, "catalog source read")
}

// ReadPrebuiltTargets returns the prebuilt components of a registry with their
// acquisition refs version-rendered from the lock's source pins — WITHOUT requiring
// artifact digests to exist yet, so `catalog pin-artifacts` can resolve them on a first
// run (full Load would fail closed on the not-yet-pinned digests). Targets are returned
// in stable (sorted-by-name) order.
func readPrebuiltTargets(ctx context.Context, p DataProvider) ([]PrebuiltTarget, error) {
	reg, err := readRegistry(p)
	if err != nil {
		return nil, err
	}
	lock, err := readLock(p)
	if err != nil {
		return nil, err
	}
	if err := validateLockAgainstRegistry(reg, lock, false); err != nil {
		return nil, err
	}
	if err := validateDerivedPins(ctx, reg, lock); err != nil {
		return nil, err
	}
	return prebuiltTargetsFrom(reg, lock)
}

// ReadPrebuiltTargetsContext is ReadPrebuiltTargets through the bounded
// context-aware provider contract.
func ReadPrebuiltTargetsContext(ctx context.Context, p DataProvider) ([]PrebuiltTarget, error) {
	bound, err := BindContext(ctx, p)
	if err != nil {
		return nil, err
	}
	targets, err := readPrebuiltTargets(ctx, bound)
	return targets, normalizeCatalogContextError(ctx, err, "catalog prebuilt-target read")
}
