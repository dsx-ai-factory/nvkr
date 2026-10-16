// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func validateLocalCompileInputs(ctx context.Context, plan *Plan, raw string) error {
	if raw == "" {
		return nil
	}
	if len(raw) > 64<<20 {
		return e.New(e.CodeInvalidCatalog, "local compiler input snapshot exceeds byte bound")
	}
	var snapshot InputSnapshot
	if err := canonical.UnmarshalExact([]byte(raw), &snapshot); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "decoding local compiler inputs")
	}
	replayed, err := CompileSnapshot(ctx, snapshot, plan.GeneratorVersion)
	if err != nil {
		return err
	}
	want, err := plan.Digest()
	if err != nil {
		return err
	}
	got, err := replayed.Digest()
	if err != nil {
		return err
	}
	if got != want {
		return e.New(e.CodePipelineDrift, "local compiler inputs do not reproduce the executing Plan")
	}
	return nil
}

func retainLocalCompileInputs(ctx context.Context, workspace, raw string) error {
	if raw == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "local input retention canceled")
	}
	root, _, err := securepath.OpenDirectory(workspace)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "opening local input destination")
	}
	defer root.Close()
	file, name, err := securepath.CreateTemporarySibling(root)
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "reserving local compiler input snapshot")
	}
	defer func() { _ = file.Close(); _ = unix.Unlinkat(int(root.Fd()), name, 0) }()
	_, writeErr := file.WriteString(raw)
	err = errors.Join(writeErr, file.Chmod(0o444), file.Sync(), file.Close())
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "retaining local compiler input snapshot")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "local input retention canceled")
	}
	if err := unix.Linkat(int(root.Fd()), name, int(root.Fd()), "compile-inputs.json", 0); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "committing immutable local compiler inputs")
	}
	if err := root.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "syncing local compiler inputs")
	}
	return nil
}
