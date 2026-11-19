// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/derivedoutput"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const maxDerivedInventoryArchiveBytes = 9 << 20

// CompareDerivedInventory checks the exact named, explicitly derived inventory
// against a caller's re-projection. It grants no signature or release authority.
// The held bytes and membership belong to this owner; no path is reopened.
func (publication *Publication) CompareDerivedInventory(ctx context.Context, output string, inventory []byte) error {
	return publication.retainedState().compareDerivedInventory(ctx, output, inventory)
}

// CompareDerivedInventory compares only an explicitly marked retained inventory.
// Other non-executable payloads remain unavailable after checked acquisition.
func (publication *CheckedPublication) CompareDerivedInventory(ctx context.Context, output string, inventory []byte) error {
	return publication.retainedState().compareDerivedInventory(ctx, output, inventory)
}

func (publication *publicationState) compareDerivedInventory(ctx context.Context, output string, inventory []byte) error {
	if err := deliveryContext(ctx); err != nil {
		return err
	}
	if publication == nil {
		return e.New(e.CodeGateUnmet, "inventory comparison requires an open publication")
	}
	// Close waits for the complete bounded read and comparison. Shallow handles
	// share this lock and revocation, exactly as retained executable access does.
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed {
		return e.New(e.CodeGateUnmet, "publication is closed")
	}
	for _, object := range publication.inspection.Root.Objects {
		if object.Name != output || object.Output == nil || object.Output.Derived == nil {
			continue
		}
		for _, file := range publication.files {
			if file.Digest() != object.BlobDigest {
				continue
			}
			raw, err := file.ReadAllContext(ctx, maxDerivedInventoryArchiveBytes)
			if err != nil {
				return classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "reading held derived inventory"))
			}
			return derivedoutput.ValidateInventoryArchive(ctx, raw, inventory)
		}
		return e.New(e.CodeReleaseIncomplete, "derived inventory has no retained layer")
	}
	return e.New(e.CodeVerifyFailed, "publication has no derived inventory named %q", output)
}
