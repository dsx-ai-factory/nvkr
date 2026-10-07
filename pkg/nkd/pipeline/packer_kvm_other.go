// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package pipeline

import (
	"context"
	"os"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func observePackerKVMProjection(_ context.Context, _ *os.File, enabled bool) (*packerKVMProjection, error) {
	if !enabled {
		return nil, nil
	}
	return nil, e.New(e.CodeEngineCapability, "Docker KVM projection requires Linux")
}

func validateEnabledPackerContainerKVM(_ packerContainerRuntime, _ *os.File) error {
	return e.New(e.CodeEngineCapability, "Packer KVM execution requires Linux")
}
