// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"crypto/sha256"
	"slices"
)

// PreparedSourcePins retains a validated candidate and its exact original CAS.
// CandidateBytes is a staging description bound to its exact original input bytes.
// This receipt coordinates preparation, not a transaction across catalog files.
type PreparedSourcePins struct {
	lock *PinLock
	raw  []byte
}

// PrepareSourcePins resolves and validates without opening a writer or modifying
// a file. Check is immaterial: preparation always withholds the commit.
func PrepareSourcePins(ctx context.Context, request SourcePinRequest) (*PreparedSourcePins, SourcePinUpdate, error) {
	return prepareSourcePins(ctx, request, ListTags, ResolvePin, resolvePrebuiltTarget)
}

func prepareSourcePins(ctx context.Context, request SourcePinRequest,
	listTags func(context.Context, string) ([]string, error),
	resolvePin func(context.Context, Source, string) (Pin, error),
	resolveArtifact func(context.Context, PrebuiltTarget, string) (ArtifactPin, error),
) (*PreparedSourcePins, SourcePinUpdate, error) {
	var prepared *PreparedSourcePins
	update, err := resolveSourcePinsWithArtifacts(ctx, request, listTags, resolvePin, resolveArtifact, func(lock *PinLock) error {
		raw, err := serializePinLock(lock)
		if err != nil {
			return err
		}
		held := clonePinLock(lock)
		held.revision = lock.revision
		prepared = &PreparedSourcePins{lock: held, raw: raw}
		return nil
	})
	if err != nil {
		return nil, update, err
	}
	return prepared, update, nil
}

// MatchesInputs checks that a staged registry/lock pair is the exact input read
// during resolution. It does not authenticate that staging directory's other files.
func (prepared *PreparedSourcePins) MatchesInputs(registry, lock []byte) bool {
	return prepared != nil && prepared.lock != nil && prepared.lock.revision.known &&
		prepared.lock.revision.registryKnown && prepared.lock.revision.exists &&
		sha256.Sum256(registry) == prepared.lock.revision.registryDigest &&
		sha256.Sum256(lock) == prepared.lock.revision.digest
}

func (prepared *PreparedSourcePins) CandidateBytes() []byte {
	if prepared == nil {
		return nil
	}
	return slices.Clone(prepared.raw)
}
