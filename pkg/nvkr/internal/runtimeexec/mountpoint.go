// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runtimeexec

import (
	"errors"
	"os"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func prepareMountPoint(root *os.File, name string) (returnErr error) {
	// Walk each component through a held directory. Nested projections must not
	// let a packaged symlink redirect a privileged mkdir or mount outside root.
	if root == nil || !absolutePath("/"+name) || name == "" || strings.HasPrefix(name, "/") {
		return e.New(e.CodeUsage, "runtime mount point is not canonical")
	}
	parent := root
	for _, component := range strings.Split(name, "/") {
		if err := unix.Mkdirat(int(parent.Fd()), component, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
			return e.Wrap(e.CodeEngineCapability, err, "preparing runtime mount point")
		}
		directory, _, err := securepath.OpenChild(parent, component, true)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "runtime mount point must be a real directory")
		}
		defer func() {
			if err := directory.Close(); err != nil {
				returnErr = e.Wrap(e.CodeEngineCapability, errors.Join(returnErr, err), "closing runtime mount point")
			}
		}()
		parent = directory
	}
	return nil
}
