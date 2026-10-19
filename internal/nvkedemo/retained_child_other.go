// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package nvkedemo

import (
	"context"
	"os/exec"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func startRetainedChild(context.Context, *exec.Cmd) (*retainedChild, error) {
	return nil, nvkrerr.New(nvkrerr.CodeEngineCapability, "retained service ownership requires native Linux")
}

func (*retainedChild) release() error {
	return nvkrerr.New(nvkrerr.CodeEngineCapability, "retained service ownership requires native Linux")
}

func joinRetainedPrestart(context.Context, *exec.Cmd) error {
	return nvkrerr.New(nvkrerr.CodeEngineCapability, "pre-start child joining requires native Linux")
}
