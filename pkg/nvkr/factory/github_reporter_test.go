// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type githubBrokenLog struct{}

func (githubBrokenLog) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestGitHubReporterPreservesPrimaryResult(t *testing.T) {
	for _, brokenLogs := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			for _, sink := range []string{"file", "unset", "directory"} {
				t.Run(fmt.Sprintf("logs=%t/failed=%t/summary=%s", brokenLogs, failed, sink), func(t *testing.T) {
					root := t.TempDir()
					var stdout, stderr bytes.Buffer
					options := GitHubStepOptions{Stdout: &stdout, Stderr: &stderr}
					if brokenLogs {
						options.Stdout, options.Stderr = githubBrokenLog{}, githubBrokenLog{}
					}
					switch sink {
					case "file":
						options.SummaryPath = filepath.Join(root, "summary.md")
					case "directory":
						options.SummaryPath = root
					}
					var primary error
					if failed {
						primary = e.New(e.CodeVerifyFailed, "exact primary")
					}
					ran := false
					err := githubProgress(t.Context(), options, "Build controller", []string{"Node: controller/build"}, func(progress *githubStepProgress) error {
						ran = true
						progress.phase("execute controller/build")
						_, _ = fmt.Fprintln(progress.options.Stdout, "child output")
						if primary == nil {
							progress.detail("Produced: controller")
						}
						return primary
					})
					if !ran || err != primary {
						t.Fatalf("primary replaced or work skipped: %v", err)
					}
					if sink == "file" {
						raw, readErr := os.ReadFile(options.SummaryPath)
						if readErr != nil {
							t.Fatal(readErr)
						}
						want := "| completed | 0 |"
						if failed {
							want = "| failed | 22 |"
						}
						if !strings.Contains(string(raw), want) || !strings.Contains(string(raw), "execute controller/build") {
							t.Fatalf("wrong summary: %s", raw)
						}
						if failed && strings.Contains(string(raw), "Produced:") {
							t.Fatal("failed work reported successful output")
						}
					}
					if !brokenLogs && failed && (!strings.Contains(stderr.String(), "at execute controller/build (exit 22") || strings.Contains(stdout.String(), "Completed:")) {
						t.Fatalf("wrong failure logs: %s %s", &stdout, &stderr)
					}
					if !brokenLogs && !failed && !strings.Contains(stdout.String(), "last phase: execute controller/build") {
						t.Fatalf("successful operation lost its final phase in logs: %s", &stdout)
					}
				})
			}
		}
	}
}

func TestGitHubReporterEscapesDataAndDoesNotMutateDescriptions(t *testing.T) {
	text := "a|b <script> `touch marker` $(touch marker) ${{ secrets.EXAMPLE }}\n::error::fake"
	descriptions := make([]string, 1, 4)
	descriptions[0] = text
	var log bytes.Buffer
	summary := filepath.Join(t.TempDir(), "summary.md")
	err := githubProgress(t.Context(), GitHubStepOptions{Stdout: &log, Stderr: &log, SummaryPath: summary}, text, descriptions, func(progress *githubStepProgress) error {
		progress.phase(text)
		progress.detail("Recovery: artifact — restored:success")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if descriptions[:cap(descriptions)][1] != "" {
		t.Fatal("caller descriptions mutated")
	}
	if strings.Contains(log.String(), "\n::error::") || !strings.Contains(log.String(), "${{ secrets.EXAMPLE }}") {
		t.Fatalf("log data was not literal: %s", &log)
	}
	raw, err := os.ReadFile(summary)
	if err != nil {
		t.Fatal(err)
	}
	for _, unsafe := range []string{"<script>", "a|b", "`touch", "\n::error::"} {
		if strings.Contains(string(raw), unsafe) {
			t.Fatalf("unsafe summary %q", unsafe)
		}
	}
	if !strings.Contains(string(raw), "Recovery: artifact") {
		t.Fatal("recovery outcome missing")
	}
}

func TestGitHubReporterCancellationDoesNotClaimSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	var log bytes.Buffer
	err := githubProgress(ctx, GitHubStepOptions{Stdout: &log, Stderr: &log}, "Operation", nil, func(progress *githubStepProgress) error {
		progress.phase("execute node")
		cancel()
		return nil
	})
	assertFactoryErrorCode(t, err, e.CodeTimeout)
	if strings.Contains(log.String(), "Completed:") || !strings.Contains(log.String(), "exit 31") {
		t.Fatalf("cancellation concealed: %s", &log)
	}
}

func TestGitHubReporterTimeoutOverridesRetainedProcessStatus(t *testing.T) {
	for _, mode := range []string{"process failure", "caller cancellation", "operation deadline"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			cause := errors.New("child was killed")
			process := &githubStepExitError{status: 137, cause: e.Wrap(e.CodeEngineCapability, cause, "runtime failed")}
			var cancellation error
			wantStatus := 137
			var stdout, stderr bytes.Buffer
			options := GitHubStepOptions{Stdout: &stdout, Stderr: &stderr, SummaryPath: filepath.Join(t.TempDir(), "summary.md")}
			err := githubProgress(ctx, options, "Inspect disk", nil, func(progress *githubStepProgress) error {
				progress.phase("root scan")
				switch mode {
				case "caller cancellation":
					cancel()
					cancellation, wantStatus = context.Canceled, 31
				case "operation deadline":
					cancellation, wantStatus = context.DeadlineExceeded, 31
					return e.Wrap(e.CodeTimeout, errors.Join(cancellation, process), "inspection deadline elapsed")
				}
				return process
			})
			if got := GitHubStepExitCode(err); got != wantStatus {
				t.Fatalf("reported exit %d, want %d: %v", got, wantStatus, err)
			}
			if !errors.Is(err, process) || !errors.Is(err, cause) {
				t.Fatalf("retained process or cause was lost: %v", err)
			}
			if cancellation != nil {
				assertFactoryErrorCode(t, err, e.CodeTimeout)
				if !errors.Is(err, cancellation) {
					t.Fatalf("cancellation cause was lost: %v", err)
				}
			}
			summary, readErr := os.ReadFile(options.SummaryPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if !strings.Contains(string(summary), fmt.Sprintf("| failed | %d |", wantStatus)) || !strings.Contains(stderr.String(), fmt.Sprintf("at root scan (exit %d,", wantStatus)) || strings.Contains(stdout.String(), "Completed:") {
				t.Fatalf("exit or outcome differs between result, logs and summary: stdout=%s stderr=%s summary=%s", &stdout, &stderr, summary)
			}
		})
	}
}
