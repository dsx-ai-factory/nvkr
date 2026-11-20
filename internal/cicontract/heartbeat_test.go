// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux || darwin

package cicontract

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
)

func TestHeartbeatCommandHelper(t *testing.T) {
	if os.Getenv("NVKR_HEARTBEAT_HELPER") != "1" {
		return
	}
	signal.Reset(os.Interrupt, syscall.SIGTERM)
	if err := os.WriteFile(os.Getenv("CHILD_PID"), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		os.Exit(80)
	}
	for {
		if _, err := os.Stat(os.Getenv("RELEASE")); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	status, err := strconv.Atoi(os.Getenv("CHILD_STATUS"))
	if err != nil {
		os.Exit(81)
	}
	os.Exit(status)
}

func TestHeartbeatLifecycle(t *testing.T) {
	wrapper, err := filepath.Abs("../../scripts/with-heartbeat.sh")
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		status    int
		tick      bool
		interrupt string
	}{{"tick and success", 0, true, ""}, {"command failure", 42, false, ""}, {"interrupted command", 143, false, "child"}, {"terminal interruption", -1, false, "group"}} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			timers, child, release := filepath.Join(work, "timers"), filepath.Join(work, "child"), filepath.Join(work, "release")
			output := filepath.Join(work, "output")
			fakeSleep := "#!/bin/sh\nprintf '%s %s %s\\n' \"$$\" \"$PPID\" \"$1\" >> \"$TIMER_PIDS\"\nexec /bin/sleep 0.2\n"
			if err := os.WriteFile(filepath.Join(work, "sleep"), []byte(fakeSleep), 0o700); err != nil {
				t.Fatal(err)
			}
			log, err := os.Create(output)
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", wrapper, "build", executable, "-test.run=^TestHeartbeatCommandHelper$")
			command.Env = append(os.Environ(), "PATH="+work+string(os.PathListSeparator)+os.Getenv("PATH"), "TIMER_PIDS="+timers, "CHILD_PID="+child, "RELEASE="+release, "CHILD_STATUS="+strconv.Itoa(tc.status), "NVKR_HEARTBEAT_HELPER=1")
			command.Stdout, command.Stderr = log, log
			if err := ownedcmd.ConfigureCancellation(command, time.Second); err != nil {
				t.Fatal(err)
			}
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			wait := make(chan error, 1)
			go func() { wait <- command.Wait() }()
			joined := false
			defer func() {
				_ = ownedcmd.KillDescendants(command)
				if !joined {
					<-wait
				}
			}()
			contents := func(path string) string { raw, _ := os.ReadFile(path); return string(raw) }
			until := func(description string, predicate func() bool) {
				t.Helper()
				deadline := time.NewTimer(15 * time.Second)
				defer deadline.Stop()
				tick := time.NewTicker(20 * time.Millisecond)
				defer tick.Stop()
				for !predicate() {
					select {
					case <-deadline.C:
						t.Fatalf("timed out waiting for %s; timers %q; log %s", description, contents(timers), contents(output))
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-tick.C:
					}
				}
			}
			until("command and timer startup", func() bool { return contents(child) != "" && contents(timers) != "" })
			if tc.tick {
				until("heartbeat and next timer", func() bool {
					return strings.Contains(contents(output), "[working] build") && strings.Count(contents(timers), "\n") >= 2
				})
			}
			switch tc.interrupt {
			case "group":
				if err := syscall.Kill(-command.Process.Pid, syscall.SIGINT); err != nil {
					t.Fatal(err)
				}
			case "child":
				pid, err := strconv.Atoi(contents(child))
				if err != nil {
					t.Fatal(err)
				}
				if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(release, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case err := <-wait:
				joined = true
				status := 0
				if err != nil {
					var exit *exec.ExitError
					if !errors.As(err, &exit) {
						t.Fatal(err)
					}
					status = exit.ExitCode()
					if tc.interrupt == "group" && exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGINT {
						t.Fatalf("expected SIGINT: %v", err)
					}
				}
				if status != tc.status {
					t.Fatalf("exit %d, want %d: %s", status, tc.status, contents(output))
				}
			case <-time.After(5 * time.Second):
				t.Fatal("heartbeat wrapper did not stop")
			}
			if !strings.Contains(contents(output), "[start] build") {
				t.Fatal(contents(output))
			}
			pids := map[int]bool{}
			for _, record := range strings.Split(strings.TrimSpace(contents(timers)), "\n") {
				fields := strings.Fields(record)
				if len(fields) != 3 || fields[2] != "10" {
					t.Fatalf("timer record %q", record)
				}
				for _, field := range fields[:2] {
					pid, err := strconv.Atoi(field)
					if err != nil {
						t.Fatal(err)
					}
					pids[pid] = true
				}
			}
			until(fmt.Sprint("timer processes to be reaped: ", pids), func() bool {
				for pid := range pids {
					if !errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
						return false
					}
				}
				return true
			})
		})
	}
}
