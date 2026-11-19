// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !darwin && !linux

package qemutpm

import (
	"context"
	"errors"
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

var ErrUnjoined = errors.New("native QEMU children could not be joined")

func Run(context.Context, []string) error {
	return e.New(e.CodeEngineCapability, "native QEMU TPM adapter requires Darwin ARM64")
}

func privateDirectory(*os.File) error                  { return ErrUnjoined }
func protectedOwner(*os.File) error                    { return ErrUnjoined }
func closeOwners(context.Context, configuration) error { return ErrUnjoined }
