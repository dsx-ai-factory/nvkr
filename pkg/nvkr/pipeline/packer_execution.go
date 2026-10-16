// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"io"
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// packerExecutable is a verified, owner-held native program. Linux retains a
// sealed anonymous file; Darwin retains a private executable snapshot. Neither
// representation grants publication, signing or qualified execution authority.
type packerExecutable interface {
	io.ReaderAt
	Size() int64
	OSFile() *os.File
	AssertUnchangedContext(context.Context) error
	Close() error
}

// packerExecutionClass contains only host mechanics. Source/material admission,
// Packer arguments, validation, output observation and lifetime remain shared.
type packerExecutionClass interface {
	open(context.Context, string, heldfile.Options) (packerExecutable, error)
	format(io.ReaderAt, string) error
	hostToolPath(int, packerExecutable) (string, error)
	projectDirectory(string, *os.File) (string, error)
	accelerator(string, string) error
	run(context.Context, packerExecutable, []packerHostTool, []string, heldfile.RunOptions) error
}

func closedPackerExecutable() error {
	return e.New(e.CodeEngineCapability, "Packer executable is closed or belongs to another execution class")
}
