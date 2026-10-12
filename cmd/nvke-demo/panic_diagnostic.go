// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Programming defects are not host-preflight failures. reflect, strconv and
// runtime/debug supply a bounded diagnostic representation; it is text, not
// a codec, authority record or public event. CreateTemp provides exclusive
// mode-0600 files; only a generic error and escaped locator reach the presenter.
// Cancellation must not erase this bounded local recovery diagnostic.
func recoveredPanic(_ context.Context, phase string, value any) error {
	log, err := os.CreateTemp("", "nvke-demo-panic-*.log")
	if err != nil {
		return nvkrerr.New(nvkrerr.CodeInternal, "%s panicked; could not create the recovery diagnostic", phase)
	}
	valueText := boundedPanicValue(value)
	stack := debug.Stack()
	if len(stack) > 32<<10 {
		stack = append(stack[:32<<10], []byte("\n[stack truncated]")...)
	}
	_, writeErr := fmt.Fprintf(log, "Phase: %s\nPanic: %s\n\n%s", phase, valueText, stack)
	closeErr := log.Close()
	if writeErr != nil || closeErr != nil {
		return nvkrerr.New(nvkrerr.CodeInternal, "%s panicked; recovery diagnostic is incomplete at %q", phase, log.Name())
	}
	return nvkrerr.New(nvkrerr.CodeInternal, "%s panicked; recovery diagnostics retained at %q", phase, log.Name())
}

func boundedPanicValue(value any) string {
	return nvkedemo.PrivatePanicValueDescription(value)
}

func runProgressWork(ctx context.Context, out io.Writer, work func(context.Context, io.Writer) error) (result error) {
	defer func() {
		if value := recover(); value != nil {
			result = recoveredPanic(ctx, "demo worker", value)
		}
	}()
	return work(ctx, out)
}
