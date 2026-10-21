// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package main

import (
	"context"
	"os"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/siteidentity"
)

func runHelperWithProcessStdio(ctx context.Context) error {
	// The existing owner refuses unsupported runtimes before any descriptor or
	// identity work. This command does not introduce another supported platform.
	_, err := siteidentity.RunHelper(ctx, os.Stdin, os.Stdout)
	return err
}
