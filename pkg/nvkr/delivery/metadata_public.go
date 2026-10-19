// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import "context"

// ValidatePublicMetadata checks a metadata-only layout, including each output's
// config profile, before CI exports its text controls. It rejects payload blobs
// and unreferenced blobs and validates evidence bindings. It does not authenticate
// signatures or issue a payload/publication capability.
func ValidatePublicMetadata(ctx context.Context, root string) error {
	inspection, err := inspectMetadata(ctx, root, false)
	if err != nil {
		return err
	}
	for _, object := range inspection.Root.Objects {
		if _, err := metadataObjectPlatform(ctx, root, object); err != nil {
			return err
		}
	}
	return deliveryContext(ctx)
}
