// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"golang.org/x/sys/unix"
)

// prepareNativeSimulatorUI exposes only the already admitted installer assets.
// NICoSim serves ui/ relative to its runtime cwd, independently of its database
// and VM directories. This view neither fetches nor modifies source files.
func prepareNativeSimulatorUI(ctx context.Context, installer, work string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	index, info, err := openRunFile(installer, filepath.FromSlash("controller/nico-sim/ui/index.html"))
	if err != nil {
		return fmt.Errorf("require selected simulator UI assets: %w", err)
	}
	defer index.Close()
	if info.Size() == 0 {
		return errors.New("selected simulator UI index is empty")
	}
	root, rootInfo, err := securepath.OpenDirectory(work)
	if err != nil {
		return err
	}
	defer root.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &stat); err != nil {
		return err
	}
	if rootInfo.Mode().Perm() != 0o700 || stat.Uid != uint32(os.Getuid()) {
		return errors.New("simulator UI view requires a private owned runtime directory")
	}
	target, err := filepath.Rel(work, filepath.Join(installer, "controller", "nico-sim", "ui"))
	if err != nil {
		return err
	}
	if err := requireContext(ctx); err != nil {
		return err
	}
	// Symlinkat is no-replace, including an existing dangling link. Holding the
	// freshly created runtime directory keeps the write at its owning boundary.
	if err := unix.Symlinkat(target, int(root.Fd()), "ui"); err != nil {
		return fmt.Errorf("prepare selected simulator UI view: %w", err)
	}
	return nil
}
