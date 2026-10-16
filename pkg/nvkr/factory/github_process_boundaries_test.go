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

func TestGitHubBootstrapStopsDescendantHoldingCapturedPipe(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python 3 required")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("Bash required")
	}
	for _, outcome := range []string{"valid module result", "invalid module result"} {
		t.Run(outcome, func(t *testing.T) {
			root, cache := t.TempDir(), t.TempDir()
			for _, name := range []string{"go.mod", "go.sum"} {
				if err := os.WriteFile(filepath.Join(root, name), []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			script := "#!/bin/sh\nexec " + shellQuote(python) + " -I - \"$@\" <<'PY'\n" + `import json, os, pathlib, subprocess, sys, time
if sys.argv[1:4] == ["mod", "download", "-json"]:
    pid = os.fork()
    if pid == 0:
        # Keep both inherited capture pipes open after the leader exits.
        pathlib.Path("descendant.pid").write_text(str(os.getpid()))
        while True:
            if not pathlib.Path(os.environ["GOMODCACHE"]).parent.exists():
                pathlib.Path("cleanup-before-stop").write_text("violation")
            time.sleep(0.01)
    deadline = time.monotonic() + 5
    while not pathlib.Path("descendant.pid").exists():
        if time.monotonic() > deadline: sys.exit(9)
        time.sleep(0.01)
    print("not-json" if ` + strconv.Quote(outcome) + ` == "invalid module result" else json.dumps({"Path":"example.com/module","Version":"v1.0.0"}), flush=True)
    sys.exit(0)
pid = pathlib.Path("descendant.pid").read_text()
state = subprocess.run(["ps", "-o", "stat=", "-p", pid], capture_output=True, text=True).stdout.strip()
assert not state or state.startswith("Z"), "descendant still executing when compilation began"
assert pathlib.Path(os.environ["GOMODCACHE"]).parent.exists(), "cache removed before compiler"
pathlib.Path("joined-before-build").write_text("yes")
pathlib.Path(".nvkr/ci-bootstrap/nvkr-exec").write_text("compiled")
PY
`
			if err := os.WriteFile(filepath.Join(root, "go"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, bash, "-c", githubCIBootstrapBuildCommand("-X main.version=fixture"))
			command.Dir = root
			command.WaitDelay = time.Second
			command.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "RUNNER_TEMP="+cache)
			output, runErr := command.CombinedOutput()
			// Register cleanup before assertions so a failing supervisor cannot leak
			// the deliberately persistent fixture descendant into subsequent tests.
			pidBytes, readErr := os.ReadFile(filepath.Join(root, "descendant.pid"))
			if readErr == nil {
				if pid, err := strconv.Atoi(string(pidBytes)); err == nil {
					t.Cleanup(func() {
						p, err := os.FindProcess(pid)
						if err == nil {
							_ = p.Kill()
						}
					})
				}
			}
			if ctx.Err() != nil {
				t.Fatalf("supervisor hung draining descendant pipes: %s", output)
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			state, _ := exec.CommandContext(t.Context(), "ps", "-o", "stat=", "-p", strings.TrimSpace(string(pidBytes))).Output()
			if value := strings.TrimSpace(string(state)); value != "" && !strings.HasPrefix(value, "Z") {
				t.Fatalf("descendant still executing: %s", state)
			}
			if outcome == "valid module result" {
				if runErr != nil {
					t.Fatalf("valid module output rejected: %v %s", runErr, output)
				}
				if _, err := os.Stat(filepath.Join(root, "joined-before-build")); err != nil {
					t.Fatal("compiler did not observe terminated descendant before cleanup")
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(runErr, &exit) || exit.ExitCode() != e.ExitCode(e.New(e.CodeBuildFailed, "")) || !bytes.Contains(output, []byte(e.CodeBuildFailed)) {
					t.Fatalf("invalid capture classification: %v %s", runErr, output)
				}
				if _, err := os.Stat(filepath.Join(root, "joined-before-build")); !os.IsNotExist(err) {
					t.Fatal("invalid module result reached compilation")
				}
			}
			if _, err := os.Stat(filepath.Join(root, "cleanup-before-stop")); !os.IsNotExist(err) {
				t.Fatal("cache deletion raced a live descendant")
			}
			entries, err := os.ReadDir(cache)
			if err != nil || len(entries) != 0 {
				t.Fatalf("owned cache leaked: %v %v", entries, err)
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
