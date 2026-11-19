// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package nvkedemo

import (
	"context"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

const darwinRetainedParent = "/private/tmp"

func validRetainedEndpoint(socket, token string) bool {
	_, err := hex.DecodeString(token)
	return len(token) == 64 && err == nil && socket == filepath.Join(darwinRetainedParent, "nvke-retained-"+token[:32], "control.sock")
}

func listenRetainedEndpoint(ctx context.Context, token string) (*net.UnixListener, func() error, error) {
	if ctx == nil || len(token) != 64 {
		return nil, nil, nvkrerr.New(nvkrerr.CodeUsage, "retained endpoint requires a context and exact token")
	}
	address := filepath.Join(darwinRetainedParent, "nvke-retained-"+token[:32], "control.sock")
	if !validRetainedEndpoint(address, token) {
		return nil, nil, nvkrerr.New(nvkrerr.CodeUsage, "invalid retained endpoint token")
	}
	if err := checkRetainedPeerCapability(ctx); err != nil {
		return nil, nil, err
	}
	parent, parentInfo, err := securepath.OpenDirectory(darwinRetainedParent)
	if err != nil {
		return nil, nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "hold native retained endpoint parent")
	}
	dirname := filepath.Base(filepath.Dir(address))
	if err := unix.Mkdirat(int(parent.Fd()), dirname, 0o700); err != nil {
		_ = parent.Close()
		return nil, nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "allocate a fresh native retained endpoint")
	}
	directory, _, err := privateNetworkDirectory(filepath.Dir(address))
	if err != nil {
		// A substituted directory is not ours to unlink.
		_ = parent.Close()
		return nil, nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "hold private native retained endpoint")
	}
	var dirID unix.Stat_t
	if err := unix.Fstat(int(directory.Fd()), &dirID); err != nil {
		_ = directory.Close()
		_ = parent.Close()
		return nil, nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "identify native retained endpoint")
	}
	var socketID *unix.Stat_t
	var once sync.Once
	var closeErr error
	cleanup := func() error {
		once.Do(func() {
			defer func() { closeErr = errors.Join(closeErr, directory.Close(), parent.Close()) }()
			current, err := os.Lstat(darwinRetainedParent)
			var currentDir unix.Stat_t
			if err != nil || !os.SameFile(parentInfo, current) ||
				unix.Fstatat(int(parent.Fd()), dirname, &currentDir, unix.AT_SYMLINK_NOFOLLOW) != nil ||
				networkStatIdentity(currentDir) != networkStatIdentity(dirID) {
				closeErr = nvkrerr.New(nvkrerr.CodePreflightFailed, "retained endpoint directory changed; preserving it")
				return
			}
			if socketID != nil {
				var currentSocket unix.Stat_t
				if unix.Fstatat(int(directory.Fd()), "control.sock", &currentSocket, unix.AT_SYMLINK_NOFOLLOW) != nil ||
					networkStatIdentity(currentSocket) != networkStatIdentity(*socketID) {
					closeErr = nvkrerr.New(nvkrerr.CodePreflightFailed, "retained endpoint socket changed; preserving it")
					return
				}
				if err := unix.Unlinkat(int(directory.Fd()), "control.sock", 0); err != nil {
					closeErr = nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "remove owned retained endpoint socket")
					return
				}
			}
			if err := unix.Unlinkat(int(parent.Fd()), dirname, unix.AT_REMOVEDIR); err != nil {
				closeErr = nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "remove owned empty retained endpoint directory")
			}
		})
		return closeErr
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, errors.Join(nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "retained endpoint canceled"), cleanup())
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Net: "unix", Name: address})
	if err != nil {
		return nil, nil, errors.Join(nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "open retained endpoint"), cleanup())
	}
	// The held cleanup checks exact directory/socket identity after the server
	// and handlers join; net must never unlink a possibly replaced pathname.
	listener.SetUnlinkOnClose(false)
	var observed unix.Stat_t
	if err = unix.Fstatat(int(directory.Fd()), "control.sock", &observed, unix.AT_SYMLINK_NOFOLLOW); err != nil ||
		observed.Mode&unix.S_IFMT != unix.S_IFSOCK || observed.Uid != uint32(os.Getuid()) {
		_ = listener.Close()
		_ = directory.Close()
		_ = parent.Close()
		return nil, nil, nvkrerr.New(nvkrerr.CodePreflightFailed, "retained endpoint socket identity unavailable; preserving path")
	}
	socketID = &observed
	return listener, cleanup, nil
}

func retainedRequesterUID(connection *net.UnixConn) uint32 {
	uid := ^uint32(0)
	raw, err := connection.SyscallConn()
	if err != nil {
		return uid
	}
	_ = raw.Control(func(socket uintptr) {
		credentials, err := unix.GetsockoptXucred(int(socket), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
		if err == nil && credentials.Version == 0 {
			uid = credentials.Uid
		}
	})
	return uid
}
