// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Verification concurrency is an execution budget, never a Plan or evidence input.
// Keep the default small: each worker may start a cosign process and registry I/O.
func verificationWorkers() (int, error) {
	raw, set := os.LookupEnv("NVKR_VERIFY_WORKERS")
	if !set {
		return 4, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 16 || strconv.Itoa(n) != raw {
		return 0, e.New(e.CodeUsage, "NVKR_VERIFY_WORKERS must be an integer from 1 through 16")
	}
	return n, nil
}

// verifyBatch owns and joins every worker. Ordinary failures do not cancel sibling
// checks: report every failure in input order. Cancellation stops new work and
// joins in-flight checks before returning. Each callback owns only its result slot;
// callers emit logs, evidence or files after the join, in canonical input order.
func verifyBatch(ctx context.Context, names []string, workers int, verify func(context.Context, int) error) error {
	if workers < 1 || workers > 16 {
		return e.New(e.CodeUsage, "verification worker count must be from 1 through 16")
	}
	errs := make([]error, len(names))
	jobs := make(chan int)
	var group sync.WaitGroup
	for range min(workers, len(names)) {
		group.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					errs[i] = classifyContext(ctx, ctx.Err())
				} else {
					errs[i] = verify(ctx, i)
				}
				if errs[i] != nil {
					errs[i] = fmt.Errorf("verify %s: %w", names[i], errs[i])
				}
			}
		})
	}
	for i := range names {
		jobs <- i
	}
	close(jobs)
	group.Wait()
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, errors.Join(append(errs, ctx.Err())...), "factory verification canceled")
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return nil
}
