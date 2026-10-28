// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package cistore

import (
	"context"
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// OpenGitHubOutput is admitted only on the maintained native CI platforms.
func OpenGitHubOutput(context.Context, string) (*os.File, error) {
	return nil, e.New(e.CodeEngineCapability, "private CI provider output requires Linux or macOS")
}
