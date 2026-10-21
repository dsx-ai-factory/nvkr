// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package validation

import (
	"context"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type catalogRoot struct{}

func openCatalogRoot(context.Context, string) (*catalogRoot, error) {
	return nil, e.New(e.CodeEngineCapability, "catalog safe file loading requires Linux or Darwin")
}
func (*catalogRoot) Close() error { return nil }
func (*catalogRoot) read(context.Context, string, int) ([]byte, error) {
	return nil, e.New(e.CodeEngineCapability, "catalog safe file loading requires Linux or Darwin")
}
