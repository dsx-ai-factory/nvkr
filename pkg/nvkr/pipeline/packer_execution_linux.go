// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

type sealedPackerExecution struct{}

func platformPackerExecutionClass() (packerExecutionClass, error) {
	return sealedPackerExecution{}, nil
}
func (sealedPackerExecution) open(ctx context.Context, path string, options heldfile.Options) (packerExecutable, error) {
	return heldfile.OpenContext(ctx, path, options)
}
func (sealedPackerExecution) format(reader io.ReaderAt, label string) error {
	return requireELFExecutable(reader, label)
}
func (sealedPackerExecution) hostToolPath(index int, executable packerExecutable) (string, error) {
	if held, ok := executable.(*heldfile.File); !ok || held == nil || held.OSFile() == nil {
		return "", closedPackerExecutable()
	}
	return heldfile.ChildPath(index + 1)
}
func (sealedPackerExecution) projectDirectory(_ string, directory *os.File) (string, error) {
	return fmt.Sprintf("/proc/%d/fd/%d", os.Getpid(), directory.Fd()), nil
}
func (sealedPackerExecution) accelerator(_ string, value string) error {
	if value != "kvm" && value != "tcg" {
		return e.New(e.CodeEngineCapability, "Linux Packer execution requires KVM or TCG")
	}
	return nil
}
func (sealedPackerExecution) run(ctx context.Context, executable packerExecutable, tools []packerHostTool, args []string, options heldfile.RunOptions) error {
	held, ok := executable.(*heldfile.File)
	if !ok || held == nil {
		return closedPackerExecutable()
	}
	descriptors := make([]heldfile.Descriptor, 0, len(tools))
	for _, tool := range tools {
		file, ok := tool.file.(*heldfile.File)
		if !ok || file == nil {
			return closedPackerExecutable()
		}
		descriptors = append(descriptors, file)
	}
	return heldfile.RunContext(ctx, held, args, options, descriptors...)
}

func requireELFExecutable(reader io.ReaderAt, label string) error {
	var magic [4]byte
	read, err := reader.ReadAt(magic[:], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return e.Wrap(e.CodeUnverifiedRef, err, "inspecting %s executable format", label)
	}
	if read != len(magic) || !bytes.Equal(magic[:], []byte("\x7fELF")) {
		return e.New(e.CodeEngineCapability, "%s is not an admitted Linux ELF executable", label)
	}
	return nil
}
