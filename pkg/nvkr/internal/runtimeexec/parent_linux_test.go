// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package runtimeexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"golang.org/x/sys/unix"
)

func TestParentDeathRearmRejectsExitedParent(t *testing.T) {
	for _, exited := range []bool{false, true} {
		t.Run(strconv.FormatBool(exited), func(t *testing.T) {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			defer func() { _ = unix.Prctl(unix.PR_SET_PDEATHSIG, 0, 0, 0, 0) }()
			command := exec.CommandContext(t.Context(), "/bin/sleep", "60")
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
			fd, err := unix.PidfdOpen(command.Process.Pid, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			if exited {
				_ = command.Process.Kill()
				_ = command.Wait()
			}
			err = rearmParentDeath(fd)
			var coded *e.Error
			if exited {
				if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
					t.Fatalf("exited parent: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

// This opt-in test kills the actual launcher process, not its context or group.
// Both the consumer PID namespace and the outer-network helper must disappear.
func TestNativeRuntimeLauncherDeath(t *testing.T) {
	if os.Getenv("NVKR_TEST_NATIVE_RUNTIME") != "1" {
		t.Skip("requires privileged native Linux namespace runner")
	}
	for _, mode := range []string{"offline", "outbound"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			parent := nativeFixtureParent(t)
			root, output := filepath.Join(parent, "root"), filepath.Join(parent, "output")
			for _, directory := range []string{filepath.Join(root, "bin"), filepath.Join(root, "usr/bin"), output} {
				if err := os.MkdirAll(directory, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Chown(output, 65534, 65534); err != nil {
				t.Fatal(err)
			}
			self, err := os.ReadFile("/proc/self/exe")
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"bin/payload", "usr/bin/slirp4netns"} {
				if err := os.WriteFile(filepath.Join(root, name), self, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			netns, err := os.Readlink("/proc/self/ns/net")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "parent-netns"), []byte(netns), 0o644); err != nil {
				t.Fatal(err)
			}
			log, err := os.CreateTemp(parent, "launcher-log-")
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			command := exec.CommandContext(ctx, "/proc/self/exe", "runtime-test-launcher", root, output, mode)
			command.Stdout, command.Stderr = log, log
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = command.Process.Kill(); _ = command.Wait() }()
			heartbeat := filepath.Join(output, "heartbeat")
			for {
				if raw, err := os.ReadFile(heartbeat); err == nil && len(raw) > 0 {
					break
				}
				select {
				case <-ctx.Done():
					raw, _ := os.ReadFile(log.Name())
					t.Fatalf("launcher readiness: %v\n%s", ctx.Err(), raw)
				case <-time.After(10 * time.Millisecond):
				}
			}
			files, err := filepath.Glob(fmt.Sprintf("/proc/%d/task/*/children", command.Process.Pid))
			if err != nil {
				t.Fatal(err)
			}
			var descriptors []unix.PollFd
			for _, file := range files {
				raw, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				for _, value := range strings.Fields(string(raw)) {
					pid, err := strconv.Atoi(value)
					if err != nil {
						t.Fatal(err)
					}
					fd, err := unix.PidfdOpen(pid, 0)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); _ = unix.Close(fd) }()
					descriptors = append(descriptors, unix.PollFd{Fd: int32(fd), Events: unix.POLLIN})
				}
			}
			want := 1
			if mode == "outbound" {
				want = 2
			}
			if len(descriptors) != want {
				t.Fatalf("launcher has %d namespace children, want %d", len(descriptors), want)
			}
			if err := command.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_ = command.Wait()
			for _, descriptor := range descriptors {
				fds := []unix.PollFd{descriptor}
				if n, err := unix.Poll(fds, 3000); err != nil || n != 1 || fds[0].Revents&(unix.POLLIN|unix.POLLHUP) == 0 {
					t.Fatalf("namespace survived launcher kill: %v %v", fds, err)
				}
			}
			before, err := os.ReadFile(heartbeat)
			if err != nil {
				t.Fatal(err)
			}
			time.Sleep(250 * time.Millisecond)
			after, err := os.ReadFile(heartbeat)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("detached descendant survived launcher kill: %q -> %q, %v", before, after, err)
			}
		})
	}
}

func deathTestLauncher(root, output string, outbound bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	admit := Admit
	if outbound {
		admit = AdmitOutbound
	}
	launcher, err := admit(ctx)
	if err != nil {
		return err
	}
	defer launcher.Close()
	rootFD, err := os.Open(root)
	if err != nil {
		return err
	}
	defer rootFD.Close()
	outFD, err := os.Open(output)
	if err != nil {
		return err
	}
	defer outFD.Close()
	return launcher.Execute(ctx, Request{Root: rootFD, Outbound: outbound, Projections: []Projection{{Directory: outFD, Target: "/out", Writable: true}}, Command: []string{"/bin/payload", "runtime-test-block"}, Workdir: "/out", Stdout: os.Stdout, Stderr: os.Stderr})
}
