// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !darwin

package conformance

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

type nativeExecution struct{}

func (*nativeExecution) close() error { return nil }

func (*admitted) runLocalNativeCommand(context.Context, *heldfile.File, []string, heldfile.RunOptions, *heldfile.File) error {
	return heldfile.ErrUnsupported
}
