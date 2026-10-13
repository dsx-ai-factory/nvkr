// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package qualification

import (
	"context"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"os"
)

func openQualificationFile(_ *os.Root, _ string, _ int, _ os.FileMode) (*os.File, error) {
	return nil, e.New(e.CodeEngineCapability, "qualification generation requires Linux or Darwin")
}
func acquireQualificationLock(_ context.Context, _ *os.Root, _ string) (func(), error) {
	return nil, e.New(e.CodeEngineCapability, "qualification generation requires Linux or Darwin")
}

func qualificationFileSupport() error {
	return e.New(e.CodeEngineCapability, "qualification generation requires Linux or Darwin")
}
