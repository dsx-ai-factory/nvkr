// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package sign

import (
	"context"
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func acquirePublicationLock(ctx context.Context, _ string) (*os.File, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	return nil, e.New(e.CodeEngineCapability,
		"crash-recoverable signed-document publication requires a kernel advisory lock")
}
