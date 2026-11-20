// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package nvkedemo

import (
	"context"
	"net"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func validRetainedEndpoint(string, string) bool { return false }

func listenRetainedEndpoint(context.Context, string) (*net.UnixListener, func() error, error) {
	return nil, nil, nvkrerr.New(nvkrerr.CodeEngineCapability, "native retained endpoint is unsupported")
}

func retainedRequesterUID(*net.UnixConn) uint32 { return ^uint32(0) }

func detachRetainedProcess() error {
	return nvkrerr.New(nvkrerr.CodeEngineCapability, "retained service ownership requires native Linux")
}
