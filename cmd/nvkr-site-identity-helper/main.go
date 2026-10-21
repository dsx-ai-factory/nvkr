// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// nvkr-site-identity-helper is a private fixed-operation build target. It exposes
// no public customer command, arbitrary script, credential flag or download path.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:])
	stop()
	if err != nil {
		// Input/RPC diagnostics may be private. The descriptive result carries its
		// completion classification; stderr exposes only the stable error mapping.
		fmt.Fprintln(os.Stderr, "site identity helper failed; exit", e.ExitCode(err))
		os.Exit(e.ExitCode(err))
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return e.New(e.CodeUsage, "identity helper accepts no arguments")
	}
	return runHelperWithProcessStdio(ctx)
}
