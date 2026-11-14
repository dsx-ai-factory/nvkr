// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"io"
	"path/filepath"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ctx returns the run's admitted context. Public execution entry points reject nil
// before calling helpers, so helpers never substitute a context of their own.
func (o RunOptions) ctx() context.Context { return o.Ctx }

func mapCancellation(ctx context.Context, err error, operation string) error {
	if err == nil {
		return nil
	}
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return e.Wrap(e.CodeTimeout, ctxErr, "%s canceled", operation)
		}
	}
	return err
}

// Local executes individual producer instructions for the factory executor.
type Local struct{}

func withDefaults(o RunOptions) RunOptions {
	if o.SourceRoot == "" {
		o.SourceRoot = filepath.Join(".nvkr", "work", "src")
	}
	if o.Runtime == "" {
		o.Runtime = "docker"
	}
	if o.OCI == nil {
		o.OCI = registryOCITransport{}
	}
	if o.PackageDir == "" {
		o.PackageDir = filepath.Join(filepath.Dir(o.SourceRoot), "packages")
	}
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	return o
}
