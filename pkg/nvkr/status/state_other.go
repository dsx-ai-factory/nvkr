// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package status

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func admitStatusState() error {
	return e.New(e.CodeEngineCapability, "protected status state requires Linux or macOS file and lock semantics")
}

func readProtectedHighWaterContext(context.Context, string, *highWaterFile) ([]byte, error) {
	return nil, admitStatusState()
}

func persistStatusHighWaterContext(context.Context, string, []byte, []byte) error {
	return admitStatusState()
}
