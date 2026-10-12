// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
)

func LoadDefinition(ctx context.Context, path string) (Definition, error) {
	return definition.LoadDefinition(ctx, path)
}
func LoadEnvironment(ctx context.Context, path string) (Environment, error) {
	return definition.LoadEnvironment(ctx, path)
}
