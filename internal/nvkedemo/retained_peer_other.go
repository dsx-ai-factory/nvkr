// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package nvkedemo

import (
	"context"
	"net"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func retainedPeerGeneration(context.Context) (string, error) {
	return "", nvkrerr.New(nvkrerr.CodeEngineCapability, "native retained peer is unsupported")
}

func checkRetainedPeerCapability(context.Context) error {
	return nvkrerr.New(nvkrerr.CodeEngineCapability, "native retained peer is unsupported")
}

func observeRetainedPeer(context.Context, *net.UnixConn, int, string) (*SupervisorPeer, error) {
	return nil, nvkrerr.New(nvkrerr.CodeEngineCapability, "native retained peer is unsupported")
}
