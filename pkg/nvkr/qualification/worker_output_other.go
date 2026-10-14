// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package qualification

import (
	"context"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func commitSmokeWorkerFiles(context.Context, string, smokeWorkerFiles, func() error) error {
	return e.New(e.CodeEngineCapability, "native smoke worker evidence requires Linux no-replace publication")
}
