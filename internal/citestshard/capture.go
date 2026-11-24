// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
)

type captureWriter struct {
	output bytes.Buffer
	stdout io.Writer
	emit   bool
	limit  int
	cancel context.CancelFunc
	err    error
}

func (w *captureWriter) Write(p []byte) (int, error) {
	if w.emit {
		_, w.err = w.stdout.Write(p)
	}
	if w.err == nil && len(p) > w.limit-w.output.Len() {
		if !w.emit {
			_, _ = w.stdout.Write(w.output.Bytes())
			_, _ = w.stdout.Write(p)
		}
		w.err = fmt.Errorf("stdout exceeded %d-byte capture limit", w.limit)
	}
	if w.err != nil {
		w.cancel()
		return 0, w.err
	}
	return w.output.Write(p)
}

// captured owns a new process group through cancellation, kill and Wait. Go's
// copy goroutine is joined by Wait; WaitDelay also bounds inherited pipes after
// a parent exits while a descendant remains alive.
func captured(ctx context.Context, argv []string, dir string, env []string, timeout time.Duration, emit bool, maxBytes int, stdout, stderr io.Writer) (log string, resultErr error) {
	if maxBytes < 1 || len(argv) == 0 {
		return "", fmt.Errorf("invalid capture arguments")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	stdout, releaseOut, err := commandOutput(ctx, stdout)
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, releaseOut()) }()
	stderr, releaseErr, err := commandOutput(ctx, stderr)
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, releaseErr()) }()
	fmt.Fprintln(stderr, "+ "+strings.Join(argv, " "))
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env, cmd.Stderr = dir, env, stderr
	writer := &captureWriter{stdout: stdout, emit: emit, limit: maxBytes, cancel: cancel}
	cmd.Stdout = writer
	err = ownedcmd.Run(ctx, cmd)
	log = writer.output.String()
	if writer.err != nil {
		return log, writer.err
	}
	if !emit && err != nil {
		fmt.Fprint(stdout, log)
	}
	if ctx.Err() != nil {
		return log, ctx.Err()
	}
	if err != nil {
		return log, err
	}
	if !utf8.ValidString(log) {
		return "", fmt.Errorf("stdout is not UTF-8")
	}
	return log, nil
}
