// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package main

import (
	"context"
	"io"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
)

func commandOutput(context.Context, io.Writer) (io.Writer, func() error, error) {
	return nil, nil, ownedcmd.ErrUnsupported
}
