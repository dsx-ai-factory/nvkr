// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package conformance

import (
	"context"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func openPrivateKubeconfig(context.Context, Tool) (*heldfile.File, error) {
	return nil, e.New(e.CodeEngineCapability, "private conformance kubeconfig admission requires Linux or macOS")
}
