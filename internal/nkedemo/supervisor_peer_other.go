// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package nkedemo

import (
	"context"
	"net"
	"os"
	"time"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func observeSupervisorPeer(context.Context, *net.UnixConn, int) (*SupervisorPeer, error) {
	return nil, nkderr.New(nkderr.CodeEngineCapability, "retained demo peer ownership requires native Linux; no PID-only fallback")
}

func checkSupervisorPeerCapability(context.Context) error {
	return nkderr.New(nkderr.CodeEngineCapability, "retained demo peer ownership requires native Linux; no PID-only fallback")
}

func pollSupervisorPeer(*os.File, time.Duration) (bool, error) {
	return false, nkderr.New(nkderr.CodeEngineCapability, "retained demo peer ownership requires native Linux")
}
