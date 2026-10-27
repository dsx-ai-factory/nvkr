// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package nvkedemo

import (
	"context"
	"encoding/hex"
	"net"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func validRetainedEndpoint(socket, token string) bool {
	_, err := hex.DecodeString(token)
	return len(token) == 64 && err == nil && socket == "@nvke-retained-"+token[:32]
}

func listenRetainedEndpoint(ctx context.Context, token string) (*net.UnixListener, func() error, error) {
	if ctx == nil || len(token) != 64 {
		return nil, nil, nvkrerr.New(nvkrerr.CodeUsage, "retained endpoint requires a context and exact token")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "retained endpoint canceled")
	}
	address := "@nvke-retained-" + token[:32]
	if !validRetainedEndpoint(address, token) {
		return nil, nil, nvkrerr.New(nvkrerr.CodeUsage, "invalid retained endpoint token")
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: address})
	if err != nil {
		return nil, nil, nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "open retained endpoint")
	}
	return listener, func() error { return nil }, nil
}

func retainedRequesterUID(connection *net.UnixConn) uint32 {
	uid := ^uint32(0)
	raw, err := connection.SyscallConn()
	if err != nil {
		return uid
	}
	_ = raw.Control(func(socket uintptr) {
		credentials, err := unix.GetsockoptUcred(int(socket), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err == nil && credentials.Pid > 0 {
			uid = credentials.Uid
		}
	})
	return uid
}
