// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package pipeline

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

func platformPackerExecutionClass() (packerExecutionClass, error) {
	return nil, e.New(e.CodeEngineCapability, "native Packer execution requires Linux or macOS")
}
