// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sourcegit

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// ImportLocal imports an exact commit into a fresh private directory using the
// ordinary developer source policy. It issues no authenticated source receipt.
// Only fetch receives the existing LocalCommand credential projection.
func ImportLocal(ctx context.Context, environment []string, directory, repository, commit, tree string, stderr io.Writer) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "source import requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "source import canceled")
	}
	// LocalCommand deliberately rejects Git search-path separators in its
	// explicit directory. Import in a fresh system scratch directory, then move
	// only the verified tree into the diagnostic workspace (which may contain
	// colons). A cross-filesystem move fails closed without a partial checkout.
	scratch, err := os.MkdirTemp("", "nvkr-test-source-")
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "creating diagnostic source scratch")
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	checkout := filepath.Join(scratch, "checkout")
	if err := os.Mkdir(checkout, 0o700); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "creating diagnostic source checkout")
	}
	query := func(args ...string) (string, error) {
		bounded, cancel := context.WithTimeout(ctx, 30*time.Minute)
		defer cancel()
		cmd, err := LocalCommand(bounded, environment, checkout, args...)
		if err != nil {
			return "", e.Wrap(e.CodeFetchFailed, err, "preparing diagnostic source %s", args[0])
		}
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, stderr
		if err := ownedcmd.Run(bounded, cmd); err != nil {
			return "", e.Wrap(e.CodeFetchFailed, err, "importing diagnostic source")
		}
		return strings.TrimSpace(output.String()), nil
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"remote", "add", "origin", repository},
		{"fetch", "--depth=1", "--no-tags", "origin", commit}, {"checkout", "--detach", commit}} {
		if _, err := query(args...); err != nil {
			return err
		}
	}
	if err := VerifyRevision(query, checkout, commit, tree); err != nil {
		return err
	}
	status, err := query("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return err
	}
	if status != "" {
		return e.New(e.CodeFetchFailed, "imported diagnostic source is modified")
	}
	if err := os.Rename(checkout, directory); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "moving verified diagnostic source")
	}
	return nil
}

// VerifyRevision shares exact commit/tree checks between ordinary imports and
// the pipeline's existing source-fetch owner. The query retains its caller's
// admitted transport, bounded I/O and context.
func VerifyRevision(query func(...string) (string, error), directory, commit, tree string) error {
	for _, check := range []struct{ revision, expected, description string }{
		{"HEAD", commit, "commit"}, {"HEAD^{tree}", tree, "tree-hash"},
	} {
		actual, err := query("rev-parse", check.revision)
		if err != nil {
			return err
		}
		if actual != check.expected {
			return e.New(e.CodeFetchFailed, "%s mismatch in %s: got %s want %s", check.description, directory, actual, check.expected)
		}
	}
	return nil
}
