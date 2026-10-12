// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package pipeline

import (
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func applyExecutionSandbox([]string) error {
	return e.New(e.CodeEngineCapability, "direct exec-node builds require Linux Landlock + seccomp isolation")
}

func replaceSandboxProcess(string, []string, []string) error {
	return e.New(e.CodeEngineCapability, "direct exec-node builds require Linux Landlock + seccomp isolation")
}
