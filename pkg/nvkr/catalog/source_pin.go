// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

// SourcePin returns the validated pin from the same registry-and-lock snapshot
// that produced the Catalog. It never reloads the lock or accepts an unvalidated
// caller-provided PinLock.
func (c *Catalog) SourcePin(name string) (Pin, error) {
	if c == nil {
		return Pin{}, e.New(e.CodeInvalidCatalog, "catalog is nil")
	}
	if _, ok := c.Sources[name]; !ok {
		return Pin{}, e.New(e.CodeInvalidCatalog, "catalog source %q does not exist", name)
	}
	pin, ok := c.sourcePins[name]
	if !ok {
		return Pin{}, e.New(e.CodeUnpinnedSource, "catalog source %q has no validated lock pin", name)
	}
	return clonePin(pin), nil
}
