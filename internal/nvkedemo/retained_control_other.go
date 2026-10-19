// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package nvkedemo

import (
	"net"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func retainedRequesterUID(*net.UnixConn) uint32 { return ^uint32(0) }

func detachRetainedProcess() error {
	return nvkrerr.New(nvkrerr.CodeEngineCapability, "retained service ownership requires native Linux")
}
