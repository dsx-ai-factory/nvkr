// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package delivery

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func lockMetadataCache(context.Context, string) (func(), error) {
	return nil, metadataCacheSupport()
}

func metadataCacheSupport() error {
	return e.New(e.CodeEngineCapability, "metadata caching requires Linux or macOS")
}
