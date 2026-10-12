// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package nkedemo

import (
	"context"
	"os/exec"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func startRetainedChild(context.Context, *exec.Cmd) (*retainedChild, error) {
	return nil, nkderr.New(nkderr.CodeEngineCapability, "retained service ownership requires native Linux")
}

func (*retainedChild) release() error {
	return nkderr.New(nkderr.CodeEngineCapability, "retained service ownership requires native Linux")
}

func joinRetainedPrestart(context.Context, *exec.Cmd) error {
	return nkderr.New(nkderr.CodeEngineCapability, "pre-start child joining requires native Linux")
}
