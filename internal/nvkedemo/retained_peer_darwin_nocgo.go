// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin && !cgo

package nvkedemo

import (
	"context"
	"net"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func retainedPeerGeneration(context.Context) (string, error) {
	return "", nvkrerr.New(nvkrerr.CodeEngineCapability, "Darwin retained ownership requires CGO and public audit-token APIs")
}

func checkRetainedPeerCapability(context.Context) error {
	return nvkrerr.New(nvkrerr.CodeEngineCapability, "Darwin retained ownership requires CGO and public audit-token APIs")
}

func observeRetainedPeer(context.Context, *net.UnixConn, int, string) (*SupervisorPeer, error) {
	return nil, nvkrerr.New(nvkrerr.CodeEngineCapability, "Darwin retained ownership requires CGO and public audit-token APIs")
}
