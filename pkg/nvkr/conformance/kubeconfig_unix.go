// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package conformance

import (
	"context"
	"os"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"golang.org/x/sys/unix"
)

func openPrivateKubeconfig(ctx context.Context, tool Tool) (*heldfile.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Open(tool.Path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "private-kubeconfig")
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, failure("kubeconfig must be an owner-only regular file")
	}
	if info.Size() < 0 || info.Size() > 1<<20 {
		return nil, failure("kubeconfig byte budget")
	}
	return heldfile.FromReaderContext(ctx, file, heldfile.Options{Label: "private kubeconfig", MaxBytes: 1 << 20, ExpectedDigest: tool.Digest})
}
