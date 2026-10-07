// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package factory

import (
	"context"
	"io"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
)

func admitGitHubRecoveryPlatform() error {
	return nil
}

// Statuses deliberately replace the previous diagnostic record on a retry.
// Atomic replacement beneath held, no-follow parents cannot truncate a symlink
// target or expose a partially written status to the existing finalizer.
func writeGitHubRestoreOutcome(ctx context.Context, artifactID, status string) error {
	if err := githubCarrierContext(ctx); err != nil {
		return err
	}
	parent, name, err := securepath.CreateParentBeneath(".", HostedRestoreOutcomePath(".nkd/factory", artifactID))
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "opening GitHub restore outcome directory")
	}
	defer parent.Close()
	file, temporary, err := securepath.CreateTemporarySibling(parent)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating GitHub restore outcome")
	}
	defer func() {
		_ = file.Close()
		_ = unix.Unlinkat(int(parent.Fd()), temporary, 0)
	}()
	if _, err := io.WriteString(file, status+"\n"); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing GitHub restore outcome")
	}
	if err := file.Sync(); err != nil {
		return e.Wrap(e.CodeInternal, err, "persisting GitHub restore outcome")
	}
	if err := file.Close(); err != nil {
		return e.Wrap(e.CodeInternal, err, "closing GitHub restore outcome")
	}
	if err := githubCarrierContext(ctx); err != nil {
		return err
	}
	if err := unix.Renameat(int(parent.Fd()), temporary, int(parent.Fd()), name); err != nil {
		return e.Wrap(e.CodeInternal, err, "replacing GitHub restore outcome")
	}
	if err := parent.Sync(); err != nil {
		return e.Wrap(e.CodeInternal, err, "persisting GitHub restore outcome directory")
	}
	return nil
}
