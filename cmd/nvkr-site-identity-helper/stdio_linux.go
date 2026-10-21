// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package main

import (
	"context"
	"os"
	"runtime"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/siteidentity"
	"golang.org/x/sys/unix"
)

// This command exclusively transfers process stdio. It must not adapt borrowed
// RunHelper files: duplicates share O_NONBLOCK with the original open file
// description. RunHelper retains its existing pollable, borrowed-file contract.
func runHelperWithProcessStdio(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil || runtime.GOARCH != "arm64" {
		// The identity owner returns the exact runtime/context admission error
		// before this adapter performs descriptor work.
		_, err := siteidentity.RunHelper(ctx, os.Stdin, os.Stdout)
		return err
	}
	defer func() { _ = os.Stdin.Close(); _ = os.Stdout.Close() }()
	output, err := adoptTransferredPipe(ctx, os.Stdout, unix.O_WRONLY, "output")
	if err != nil {
		return err
	}
	defer output.Close()
	// Output ownership and deadline support precede input adoption or reading.
	input, err := adoptTransferredPipe(ctx, os.Stdin, unix.O_RDONLY, "input")
	if err != nil {
		return err
	}
	defer input.Close()
	_, err = siteidentity.RunHelper(ctx, input, output)
	return err
}

// adoptTransferredPipe consumes original on every return. No borrowed-file API
// uses it. Private >=3 descriptors also turn EPIPE into an ordinary error rather
// than Go's special SIGPIPE handling for stdout/stderr.
func adoptTransferredPipe(ctx context.Context, original *os.File, access int, label string) (*os.File, error) {
	if original != nil {
		defer original.Close()
	}
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "identity helper %s pipe admission requires a context", label)
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "identity helper %s pipe admission canceled", label)
	}
	if original == nil {
		return nil, e.New(e.CodeEngineCapability, "identity helper %s needs a transferred pipe", label)
	}
	raw, err := original.SyscallConn()
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "identity helper %s descriptor is unavailable", label)
	}
	duplicate := -1
	var descriptorErr error
	// File.Fd disables deadlines. Control inspects/duplicates under os.File's
	// descriptor lifetime lock without changing its polling configuration.
	err = raw.Control(func(fd uintptr) {
		var st unix.Stat_t
		if descriptorErr = unix.Fstat(int(fd), &st); descriptorErr != nil {
			return
		}
		var flags int
		flags, descriptorErr = unix.FcntlInt(fd, unix.F_GETFL, 0)
		if descriptorErr != nil {
			return
		}
		if st.Mode&unix.S_IFMT != unix.S_IFIFO || flags&unix.O_ACCMODE != access {
			descriptorErr = e.New(e.CodeEngineCapability, "identity helper %s requires its directional pipe", label)
			return
		}
		duplicate, descriptorErr = unix.FcntlInt(fd, unix.F_DUPFD_CLOEXEC, 3)
	})
	if err != nil || descriptorErr != nil {
		if duplicate >= 0 {
			_ = unix.Close(duplicate)
		}
		if err == nil {
			err = descriptorErr
		}
		return nil, e.Wrap(e.CodeEngineCapability, err, "identity helper %s pipe admission failed", label)
	}
	if err := unix.SetNonblock(duplicate, true); err != nil {
		_ = unix.Close(duplicate)
		return nil, e.Wrap(e.CodeEngineCapability, err, "identity helper %s cannot become nonblocking", label)
	}
	owned := os.NewFile(uintptr(duplicate), "identity-helper-"+label)
	if owned == nil {
		_ = unix.Close(duplicate)
		return nil, e.New(e.CodeEngineCapability, "identity helper %s pipe could not be adopted", label)
	}
	if access == unix.O_WRONLY {
		err = owned.SetWriteDeadline(time.Time{})
	} else {
		err = owned.SetReadDeadline(time.Time{})
	}
	if err != nil {
		_ = owned.Close()
		return nil, e.Wrap(e.CodeEngineCapability, err, "identity helper %s pipe is not pollable", label)
	}
	return owned, nil
}
