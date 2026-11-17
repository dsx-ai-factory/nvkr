// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build !linux

package runtimeexec

import (
	"context"
	"io"
	"os"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func platformAdmission() error {
	return e.New(e.CodeEngineCapability, "produced runtime execution requires native Linux namespaces")
}
func executeChild(context.Context, *heldfile.File, requestRecord, []*os.File, io.Writer, io.Writer) error {
	return platformAdmission()
}

// Entry is called only by the dedicated bootstrap dispatch in the NVKR command.
func Entry() error { return platformAdmission() }
