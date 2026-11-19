// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux && !darwin

package nvkedemo

import (
	"context"
	"net"
	"os"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func observeSupervisorPeer(context.Context, *net.UnixConn, int) (*SupervisorPeer, error) {
	return nil, nvkrerr.New(nvkrerr.CodeEngineCapability, "retained demo peer ownership requires native Linux; no PID-only fallback")
}

func checkSupervisorPeerCapability(context.Context) error {
	return nvkrerr.New(nvkrerr.CodeEngineCapability, "retained demo peer ownership requires native Linux; no PID-only fallback")
}

func pollSupervisorPeer(*os.File, time.Duration) (bool, error) {
	return false, nvkrerr.New(nvkrerr.CodeEngineCapability, "retained demo peer ownership requires native Linux")
}
