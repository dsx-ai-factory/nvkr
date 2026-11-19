// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package factory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGitHubBootstrapDeadlineStopsDescendants(t *testing.T) {
	requireGitHubBootstrapTimeout(t)
	for _, phase := range []string{"download", "build"} {
		t.Run(phase, func(t *testing.T) {
			root, cache := t.TempDir(), t.TempDir()
			for _, name := range []string{"go.mod", "go.sum"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			script := `#!/bin/bash
set -eu
phase=build
[ "$1" != mod ] || phase=download
[ "$phase" = ` + shellQuote(phase) + ` ] || exit 0
# Both leader and descendant resist TERM, exercising timeout's KILL escalation.
trap '' TERM
group=$(ps -o pgid= -p "$$" | tr -d ' ')
[ -n "$group" ]
bash -c '
  printf "%s" "$$" > descendant.pid
  # Readiness triggers TERM/KILL escalation in the real timeout.
  kill -ALRM "$1"
  while :; do sleep 1; done
' descendant "$group" &
wait
`

			if err := os.WriteFile(filepath.Join(root, "go"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			bootstrap := githubCIBootstrapBuildCommand("-X main.version=fixture", githubRunnerRelease{})
			// The generous fallback bounds a broken fixture; readiness above triggers
			// the deadline under test, without relying on startup elapsed time.
			bootstrap = strings.ReplaceAll(strings.ReplaceAll(bootstrap, "900s", "60s"), "--kill-after=5s", "--kill-after=0.2s")
			command := exec.CommandContext(ctx, "bash", "-c", bootstrap)
			command.Dir = root
			command.WaitDelay = time.Second
			command.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "RUNNER_TEMP="+cache)
			output, runErr := command.CombinedOutput()
			pidBytes, err := os.ReadFile(filepath.Join(root, "descendant.pid"))
			if err != nil {
				t.Fatalf("missing descendant: %v %s", err, output)
			}
			pid, err := strconv.Atoi(string(pidBytes))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if p, err := os.FindProcess(pid); err == nil {
					_ = p.Kill()
				}
			})
			var exit *exec.ExitError
			if !errors.As(runErr, &exit) || exit.ExitCode() != e.ExitCode(e.New(e.CodeTimeout, "")) || !bytes.Contains(output, []byte(e.CodeTimeout)) {
				t.Fatalf("lost timeout: %v %s", runErr, output)
			}
			assertGitHubBootstrapProcessStopped(ctx, t, pid)
			if entries, err := os.ReadDir(cache); err != nil || len(entries) != 0 {
				t.Fatalf("cache leaked: %v %v", entries, err)
			}
		})
	}
}

func TestGitHubKVMAccessFailureBranches(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash required")
	}
	for _, mode := range []string{"missing", "symlink", "regular file", "already open", "repair succeeds", "sudo fails", "still unopenable"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			device := filepath.Join(root, "device")
			switch mode {
			case "missing":
			case "symlink":
				if err := os.Symlink("/dev/null", device); err != nil {
					t.Fatal(err)
				}
			case "regular file":
				if err := os.WriteFile(device, []byte("unchanged"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				device = "/dev/null" // Read/write only; no real device permissions are changed.
			}
			// Execute the emitted branch structure with a safe character-device fixture.
			// Override exec's return value to model denied opens; sudo/timeout are
			// fixtures that record exact arguments and never invoke privileged commands.
			script := strings.ReplaceAll(githubKVMAccessCheck().Run, "/dev/kvm", device)
			script = strings.ReplaceAll(script, "/usr/bin/timeout", "fixture_timeout")
			script = strings.ReplaceAll(script, "/usr/bin/sudo", "fixture_sudo")
			prefix := `fixture_timeout() {
 [ "$1" = '--kill-after=5s' ] && [ "$2" = '15s' ] || exit 99
 shift 2
 "$@"
}
fixture_sudo() {
 [ "$1" = '-n' ] && [ "$2" = '--' ] && [ "$3" = '/usr/bin/chmod' ] && [ "$4" = '0666' ] && [ "$5" = '/dev/null' ] || exit 98
 printf called > repair
 [ "$MODE" != 'sudo fails' ]
}
exec() {
 if [ "$MODE" = 'already open' ]; then return 0; fi
 if [ "$MODE" = 'repair succeeds' ] && [ -f repair ]; then return 0; fi
 return 1
}
`
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, bash, "-c", prefix+script)
			command.Dir = root
			command.Env = append(os.Environ(), "MODE="+mode)
			output, runErr := command.CombinedOutput()
			success := mode == "already open" || mode == "repair succeeds"
			if success {
				if runErr != nil {
					t.Fatalf("admitted device failed: %v %s", runErr, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(runErr, &exit) || exit.ExitCode() != e.ExitCode(e.New(e.CodeEngineCapability, "")) || !bytes.Contains(output, []byte(e.CodeEngineCapability)) {
					t.Fatalf("device failure lost code: %v %s", runErr, output)
				}
			}
			_, err = os.Stat(filepath.Join(root, "repair"))
			called := err == nil
			if called != (mode == "repair succeeds" || mode == "sudo fails" || mode == "still unopenable") {
				t.Fatal("repair reached wrong device branch")
			}
			if mode == "regular file" {
				raw, err := os.ReadFile(device)
				if err != nil || string(raw) != "unchanged" {
					t.Fatal("non-device fixture was modified")
				}
			}
		})
	}
}
