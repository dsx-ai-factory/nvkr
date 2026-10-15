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
		name, mode     string
		parentDeadline bool
		code           e.Code
	}{
		{"failed-acquisition", "acquire", false, e.CodeBuildFailed},
		{"failed-suite", "suite", false, e.CodeBuildFailed},
		{"execution-timeout", "timeout", false, e.CodeTimeout},
		{"parent-expires-during-cleanup", "acquire", true, e.CodeBuildFailed},
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
			ctx := t.Context()
			if test.parentDeadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 500*time.Millisecond)
				defer cancel()
			}
			started := time.Now()
			err := RunArtifactDiagnostic(ctx, ArtifactDiagnostic{Workspace: t.TempDir(), Image: "fixture", Version: "v1", Platform: "linux/amd64", Suite: "contract", Timeout: 500 * time.Millisecond}, io.Discard, io.Discard)
			elapsed := time.Since(started)
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
			if elapsed > 4*time.Second {
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
