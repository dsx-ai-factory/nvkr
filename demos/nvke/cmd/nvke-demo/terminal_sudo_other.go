// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package main

import (
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/term"
)

func restoreAuthenticationTerminal(_, _ int, _ *term.State) error {
	return nvkrerr.New(nvkrerr.CodeEngineCapability, "interactive deployment authentication requires Linux")
}
