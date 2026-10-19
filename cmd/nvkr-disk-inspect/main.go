// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// nvkr-disk-inspect is the fixed entrypoint inside the inspection toolchain. All
// subject authentication, inspection and result binding lives in pkg/nvkr/sbom;
// the execution owner authenticates kernel resource limits before subject reads.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/dsx-ai-factory/nvkr/internal/diskdiagnostic"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	ctx = diskdiagnostic.WithWriter(ctx, os.Stderr)
	status := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	if status != 0 {
		os.Exit(status)
	}
}

// run maps flags and the owning SBOM operation to the frozen CLI exit table.
func run(ctx context.Context, args []string, input io.Reader, output, diagnostic io.Writer) int {
	return runWithLimitCheck(ctx, args, input, output, diagnostic, execution.VerifyCurrentCgroupLimits)
}

// The private seam tests that admission precedes subject reads. Production always
// invokes the kernel owner above; no caller can replace its enforcement check.
func runWithLimitCheck(ctx context.Context, args []string, input io.Reader, output, diagnostic io.Writer, verify func(context.Context, uint64, uint64, uint64) error) int {
	return runWithContentHelper(ctx, args, input, output, diagnostic, verify, sbom.RunDiskContentHelper)
}

// Only the command's positional mapping is replaceable in tests. Production
// always delegates to the concrete SBOM owner after kernel-limit admission.
func runWithContentHelper(ctx context.Context, args []string, input io.Reader, output, diagnostic io.Writer,
	verify func(context.Context, uint64, uint64, uint64) error,
	content func(context.Context, string, string, string, int64, string, string, string, string, uint64, string, io.Reader, io.Writer) error,
) int {
	if len(args) != 7 && len(args) != 14 {
		err := e.New(e.CodeUsage, "inspection helper requires exact tool, subject and resource pins")
		fmt.Fprintln(diagnostic, err)
		return e.ExitCode(err)
	}
	var limits [3]uint64
	for i := range limits {
		value, err := strconv.ParseUint(args[len(args)-3+i], 10, 64)
		if err != nil || value == 0 {
			err := e.New(e.CodeUsage, "inspection helper resource ceilings must be positive integers")
			fmt.Fprintln(diagnostic, err)
			return e.ExitCode(err)
		}
		limits[i] = value
	}
	if err := verify(ctx, limits[0], limits[1], limits[2]); err != nil {
		fmt.Fprintln(diagnostic, err)
		return e.ExitCode(err)
	}
	args = args[:len(args)-3]
	size, err := strconv.ParseInt(args[3], 10, 64)
	if err != nil {
		err = e.Wrap(e.CodeUsage, err, "inspection helper subject size must be an exact integer")
	} else if len(args) == 11 {
		budget, parseErr := strconv.ParseUint(args[9], 10, 64)
		if args[4] != "composition" || parseErr != nil {
			err = e.New(e.CodeUsage, "invalid disk content helper operation or budget")
		} else {
			err = content(ctx, args[0], args[1], args[2], size, args[5], args[6], args[7], args[8], budget, args[10], input, output)
		}
	} else {
		err = sbom.RunDiskInspectionHelper(ctx, args[0], args[1], args[2], size, input, output)
	}
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return e.ExitCode(err)
	}
	return 0
}
