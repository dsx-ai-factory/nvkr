// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"math"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// StoredBytes describes the held closure's bytes for scratch admission. It is
// neither a filesystem reservation nor evidence or publication authority.
func (publication *Publication) StoredBytes(ctx context.Context) (uint64, error) {
	if err := deliveryContext(ctx); err != nil {
		return 0, err
	}
	if publication == nil || publication.publicationState == nil {
		return 0, e.New(e.CodeGateUnmet, "publication is not open")
	}
	publication.mu.RLock()
	defer publication.mu.RUnlock()
	if publication.closed {
		return 0, e.New(e.CodeGateUnmet, "publication is closed")
	}
	var total uint64
	add := func(size int64) error {
		if size < 0 || uint64(size) > math.MaxUint64-total {
			return e.New(e.CodeEngineCapability, "publication storage size overflows")
		}
		total += uint64(size)
		return nil
	}
	for _, file := range publication.files {
		if err := add(file.Size()); err != nil {
			return 0, err
		}
	}
	for _, file := range publication.diskPayloads {
		info, err := file.Stat()
		if err != nil {
			return 0, e.Wrap(e.CodeVerifyFailed, err, "observing held disk storage")
		}
		if err := add(info.Size()); err != nil {
			return 0, err
		}
	}
	return total, nil
}
