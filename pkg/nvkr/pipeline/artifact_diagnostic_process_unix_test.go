// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package pipeline

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestArtifactDiagnosticProcessCleanup(t *testing.T) {
	for _, test := range []struct {
		name, mode   string
		cancelParent bool
		code         e.Code
	}{
		{"failed-acquisition", "acquire", false, e.CodeBuildFailed},
		{"failed-suite", "suite", false, e.CodeBuildFailed},
		{"execution-timeout", "timeout", false, e.CodeTimeout},
		{"parent-canceled-during-cleanup", "acquire", true, e.CodeBuildFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			pidPath := filepath.Join(root, "cleanup.pid")
			// The real runtime boundary launches this client. Cleanup ignores TERM
			// until the reaper's reserved grace escalates to KILL. No daemon runs.
			script := "#!/bin/sh\ncase \"$1\" in\n" +
				"rm) trap '' TERM; printf '%s\\n' \"$$\" > '" + strings.ReplaceAll(pidPath, "'", "'\"'\"'") + "'; exec sleep 30;;\n" +
				"run) case '" + test.mode + "' in\n" +
				"  timeout) exec sleep 30;;\n" +
				"  suite) for argument do [ \"$argument\" != make ] || exit 7; done; exit 0;;\n" +
				"  *) exit 7;;\nesac;;\n*) exit 99;;\nesac\n"
			if err := os.WriteFile(filepath.Join(root, "docker"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", root+string(os.PathListSeparator)+os.Getenv("PATH"))
			workspace := t.TempDir()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			watchCtx, stopWatch := context.WithCancel(t.Context())
			defer stopWatch()
			var entered time.Time
			watchDone := make(chan struct{})
			go func() {
				defer close(watchDone)
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					if _, err := os.Stat(pidPath); err == nil {
						entered = time.Now()
						if test.cancelParent {
							cancel()
						}
						return
					}
					select {
					case <-watchCtx.Done():
						return
					case <-ticker.C:
					}
				}
			}()
			// Startup and scheduling must not race the completed failures. Only
			// the deliberate timeout case gets a short execution deadline.
			timeout := 30 * time.Second
			if test.code == e.CodeTimeout {
				timeout = 500 * time.Millisecond
			}
			err := RunArtifactDiagnostic(ctx, ArtifactDiagnostic{Workspace: workspace, Image: "fixture", Version: "v1", Platform: "linux/amd64", Suite: "contract", Timeout: timeout}, io.Discard, io.Discard)
			finished := time.Now()
			stopWatch()
			<-watchDone // Join the observer before reading its timestamp.
			if entered.IsZero() {
				t.Fatal("cleanup client entry was not observed")
			}
			if test.cancelParent && !errors.Is(ctx.Err(), context.Canceled) {
				t.Error("parent was not canceled during cleanup")
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Errorf("execution result changed during cleanup: %v", err)
			}
			if test.code == e.CodeBuildFailed {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 7 {
					t.Errorf("completed process exit lost: %v", err)
				}
			}
			// Measure cleanup independently of execution startup, using the
			// same observed entry that triggers the late parent cancellation.
			if elapsed := finished.Sub(entered); elapsed > 4*time.Second {
				t.Errorf("cleanup exceeded its two-second budget and scheduling margin: %s", elapsed)
			}
			rawPID, readErr := os.ReadFile(pidPath)
			if readErr != nil {
				t.Fatal("cleanup client was not exercised:", readErr)
			}
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(rawPID)))
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			if alive := syscall.Kill(pid, 0); !errors.Is(alive, syscall.ESRCH) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Errorf("cleanup client was not joined: %v", alive)
			}
		})
	}
}
