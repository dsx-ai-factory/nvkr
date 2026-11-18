// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package pipeline

import (
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func buildMkosi(ir.Node, RunOptions, string) (*ArtifactResult, error) {
	return nil, e.New(e.CodeEngineCapability, "mkosi assembly requires its admitted native Linux namespace executor")
}
