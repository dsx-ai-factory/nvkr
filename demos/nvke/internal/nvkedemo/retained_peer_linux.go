// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package nvkedemo

import (
	"context"
	"net"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func retainedPeerGeneration(ctx context.Context) (string, error) {
	return "", CheckSupervisorPeerCapability(ctx)
}

func checkRetainedPeerCapability(ctx context.Context) error {
	return CheckSupervisorPeerCapability(ctx)
}

func observeRetainedPeer(ctx context.Context, connection *net.UnixConn, pid int, generation string) (*SupervisorPeer, error) {
	if generation != "" {
		return nil, nvkrerr.New(nvkrerr.CodePreflightFailed, "Linux retained peer has an unexpected generation selector")
	}
	return ObserveSupervisorPeer(ctx, connection, pid)
}
