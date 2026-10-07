// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"path/filepath"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"
)

func TestProfileCLIThreadsCancellationIntoAuthoredProfileLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.pipe")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app := &cli.App{Commands: []*cli.Command{cmdProfile()}}
	err := app.RunContext(ctx, []string{"nkdctl", "profile", "show", "--profile", path})
	if cliErrorCode(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("error = %v, want retryable %s before profile path inspection", err, nkderr.CodeTimeout)
	}
}
