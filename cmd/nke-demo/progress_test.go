// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
				out.(nkedemo.ProgressReporter).Report(nkedemo.ProgressEvent{Kind: nkedemo.LaneStarted, Name: "build"})
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

func TestProgressKeepsObservedBoundariesSeparateFromLogs(t *testing.T) {
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
	if out.String() != "[START 0s] \"os\"\n[FAIL 0s] \"boot\\x1b[2J\"\n" {
		t.Fatalf("unexpected progress: %q", out.String())
	}
}

func TestProgressConciseAndVerbosePublicOutput(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		t.Run(map[bool]string{false: "concise", true: "verbose"}[verbose], func(t *testing.T) {
			var out, raw bytes.Buffer
			input := "raw compiler noise\n[start] image (build)\nBuild graph: 29 outputs\n  output: image [oci]\n[start] escaped\x1b[2J\n==> qemu.node: Waiting for SSH to become available...\r\n[done] image\nPASS: actual owner result"
			err := progressOutput(t.Context(), &out, &raw, verbose, "", func(_ context.Context, writer io.Writer) error {
				for _, b := range []byte(input) {
					if _, err := writer.Write([]byte{b}); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil || raw.String() != input {
				t.Fatalf("error=%v raw=%q", err, &raw)
			}
			if verbose {
				if out.String() != input {
					t.Fatalf("verbose output changed: %q", &out)
				}
			} else {
				for _, marker := range []string{"[log +0s]", "[start] image", "Build graph:", "  output:", "[done] image", "Waiting for SSH", "escaped\\x1b[2J", "PASS:"} {
					if !strings.Contains(out.String(), marker) {
						t.Fatalf("missing %q: %q", marker, &out)
					}
				}
				if strings.Contains(out.String(), "compiler noise") {
					t.Fatal("concise mode streamed raw noise")
				}
				if strings.ContainsRune(out.String(), '\x1b') {
					t.Fatal("raw terminal control in concise output")
				}
			}
		})
	}
}

func TestProgressBoundsAndLogFailure(t *testing.T) {
	for _, kind := range []string{"huge-line", "log-error", "verbose-log-error", "log-bound"} {
		t.Run(kind, func(t *testing.T) {
			var out, raw bytes.Buffer
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			w := &checkedWriter{out: &out, raw: &raw, remaining: 8 << 20, cancel: cancel, started: time.Now()}
			if kind == "log-error" || kind == "verbose-log-error" {
				w.raw = failedProgressWriter{short: true}
				w.verbose = kind == "verbose-log-error"
			}
			if kind == "log-bound" {
				w.remaining = 2
			}
			data := "[start] " + strings.Repeat("x", 32<<10) + "\n[done] next\n"
			_, err := io.WriteString(w, data)
			if kind == "huge-line" {
				if err != nil || len(w.line) != 0 || strings.Contains(out.String(), "xxxx") || !strings.Contains(out.String(), "[done] next") {
					t.Fatalf("error=%v output=%q", err, &out)
				}
			} else if err == nil || ctx.Err() == nil {
				t.Fatal("log failure did not cancel work")
			}
		})
	}
}

func TestBuildLogRetainedPrivatelyOnFailure(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var out bytes.Buffer
	err := runBuildProgress(t.Context(), &out, false, func(_ context.Context, writer io.Writer) error {
		_, err := io.WriteString(writer, "complete public build bytes\n")
		return errors.Join(err, nkderr.New(nkderr.CodeBuildFailed, "failed producer"))
	})
	assertCode(t, err, nkderr.CodeBuildFailed)
	paths, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "nke-demo-build-*.log"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("logs=%v error=%v", paths, err)
	}
	info, err := os.Stat(paths[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private log: %v", err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil || string(data) != "complete public build bytes\n" || !strings.Contains(out.String(), paths[0]) {
		t.Fatalf("log=%q error=%v", data, err)
	}
}
