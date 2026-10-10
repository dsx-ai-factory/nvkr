// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package main

import nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"

func restoreTerminalForeground(_, _ int) error {
	return nkderr.New(nkderr.CodeEngineCapability, "interactive deployment authentication requires Linux")
}
