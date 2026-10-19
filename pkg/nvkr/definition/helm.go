// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"maps"
	"slices"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ValidateBindings checks named relationships; chart mappings belong to the producer.
func (h HelmOutput) ValidateBindings() error {
	if len(h.Images) == 0 || len(h.Images) > 64 {
		return e.New(e.CodeInvalidCatalog, "Helm binding requires 1..64 image slots")
	}
	for _, slot := range sortedImageSlots(h.Images) {
		if !namePattern.MatchString(slot) || !namePattern.MatchString(h.Images[slot]) {
			return e.New(e.CodeInvalidCatalog, "invalid Helm image relationship %q", slot)
		}
	}
	return nil
}
func sortedImageSlots(images map[string]string) []string {
	return slices.Sorted(maps.Keys(images))
}
