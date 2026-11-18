// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package factory

import (
	"context"
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func newRuntimeScratch(context.Context, uint64) (*runtimeScratch, error) {
	return nil, e.New(e.CodeEngineCapability, "runtime scratch requires the privileged native Linux owner")
}

func removeRuntimeScratch(*os.File, string) error {
	return e.New(e.CodeEngineCapability, "runtime scratch cleanup requires its native Linux owner")
}
