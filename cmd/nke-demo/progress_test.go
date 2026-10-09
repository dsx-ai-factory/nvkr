// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

type failedProgressWriter struct{ short bool }

func (w failedProgressWriter) Write(data []byte) (int, error) {
	if w.short {
		return len(data) / 2, nil
	}
	return 0, errors.New("private writer details")
}

func TestProgressFailureCancelsAndWaitsForWork(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "short"}[short], func(t *testing.T) {
			joined := false
			err := runProgress(t.Context(), failedProgressWriter{short: short}, func(ctx context.Context, out io.Writer) error {
				_, _ = io.WriteString(out, "public output")
				if ctx.Err() == nil {
					t.Fatal("output failure did not cancel work")
				}
				joined = true
				return nil
			})
			assertCode(t, err, nkderr.CodePreflightFailed)
			if !joined || strings.Contains(err.Error(), "private writer") {
				t.Fatal("work unjoined or private error exposed")
			}
		})
	}
}

func TestProgressStreamsOutputAndObservedBoundaries(t *testing.T) {
	var out bytes.Buffer
	err := runProgress(t.Context(), &out, func(_ context.Context, writer io.Writer) error {
		reporter := writer.(nkedemo.ProgressReporter)
		reporter.Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "os"})
		_, _ = io.WriteString(writer, "packer output\n")
		reporter.Report(nkedemo.ProgressEvent{Kind: nkedemo.PhaseFailed, Name: "boot\x1b[2J", Status: "secret"})
		reporter.Report(nkedemo.ProgressEvent{Kind: "unknown", Name: "not observed"})
		return nkderr.New(nkderr.CodeBuildFailed, "build failed")
	})
	assertCode(t, err, nkderr.CodeBuildFailed)
	if out.String() != "[START] \"os\"\npacker output\n[FAIL] \"boot\\x1b[2J\"\n" {
		t.Fatalf("unexpected progress: %q", out.String())
	}
}
