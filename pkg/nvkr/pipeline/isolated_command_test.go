// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func isolatedFixture(t *testing.T) IsolatedCommand {
	t.Helper()
	root := filepath.Join(t.TempDir(), "space:comma,quote\"")
	return IsolatedCommand{
		Image: "go@sha256:" + strings.Repeat("a", 64), Platform: "linux/arm64",
		Source: filepath.Join(root, "source"), Cache: filepath.Join(root, "cache"), WorkingDirectory: "cli",
		Inputs:      []IsolatedMount{{Source: filepath.Join(root, "inputs", "executable", "nvkectl"), Target: "/inputs/executable/nvkectl"}},
		Command:     []string{"make", "test", "FLAGS=-count=1 -json"},
		Environment: []string{"GOCACHE=/cache/go-build", "NVKECTL=/inputs/executable/nvkectl"},
		Offline:     true, Timeout: time.Minute,
	}
}

func mountsOf(t *testing.T, args []string) map[string][]string {
	t.Helper()
	mounts := map[string][]string{}
	for i, arg := range args {
		if arg == "--mount" {
			fields, err := csv.NewReader(strings.NewReader(args[i+1])).Read()
			if err != nil {
				t.Fatal(err)
			}
			mounts[fields[2]] = fields
		}
	}
	return mounts
}

func flagValue(args []string, flag string) (string, bool) {
	index := slices.Index(args, flag)
	if index < 0 || index+1 >= len(args) {
		return "", false
	}
	return args[index+1], true
}

func TestIsolatedCommandArguments(t *testing.T) {
	for _, offline := range []bool{true, false} {
		t.Run(map[bool]string{true: "offline", false: "networked"}[offline], func(t *testing.T) {
			command := isolatedFixture(t)
			command.Offline = offline
			if !offline {
				command.Inputs = nil
			}
			var args []string
			run := func(opts RunOptions, arguments ...string) error {
				if opts.Runtime != "docker" {
					t.Fatal(opts.Runtime)
				}
				args = append([]string{}, arguments...)
				return nil
			}
			if err := runIsolatedCommand(t.Context(), "nvkr-fixture", command, io.Discard, io.Discard, run, unexpectedArtifactCleanup(t)); err != nil {
				t.Fatal(err)
			}
			if args[0] != "run" || !slices.Contains(args, "--rm") || !slices.Contains(args, "--init") || !slices.Contains(args, "--read-only") {
				t.Fatal("isolation baseline lost", args)
			}
			for flag, value := range map[string]string{"--name": "nvkr-fixture", "--platform": "linux/arm64", "--cap-drop": "ALL",
				"--security-opt": "no-new-privileges", "--workdir": "/src/cli", "--tmpfs": "/tmp:rw,exec,mode=1777"} {
				if got, ok := flagValue(args, flag); !ok || got != value {
					t.Fatalf("%s = %q, want %q", flag, got, value)
				}
			}
			if user, ok := flagValue(args, "--user"); !ok || user != fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()) {
				t.Fatal("container must run as the invoking user, not the image default", user)
			}
			network, hasNetwork := flagValue(args, "--network")
			if offline != (hasNetwork && network == "none") {
				t.Fatalf("offline=%t but --network=%q", offline, network)
			}
			for _, value := range command.Environment {
				index := slices.Index(args, value)
				if index <= 0 || args[index-1] != "--env" {
					t.Fatalf("missing exact --env %s pair", value)
				}
			}
			mounts := mountsOf(t, args)
			want := map[string][]string{
				"dst=/src":   {"type=bind", "src=" + command.Source, "dst=/src", "readonly"},
				"dst=/cache": {"type=bind", "src=" + command.Cache, "dst=/cache"},
			}
			for _, input := range command.Inputs {
				want["dst="+input.Target] = []string{"type=bind", "src=" + input.Source, "dst=" + input.Target, "readonly"}
			}
			if !reflect.DeepEqual(mounts, want) {
				t.Fatalf("mounts = %v, want %v", mounts, want)
			}
			tail := append([]string{command.Image}, command.Command...)
			if !reflect.DeepEqual(args[len(args)-len(tail):], tail) {
				t.Fatal("image and argument vector must end the invocation unchanged", args)
			}
		})
	}
}

func TestIsolatedCommandRefusesBeforeRuntime(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		mutate func(*IsolatedCommand)
		code   e.Code
	}{
		{"nil-context", nil, func(*IsolatedCommand) {}, e.CodeUsage},
		{"canceled", canceled, func(*IsolatedCommand) {}, e.CodeTimeout},
		{"platform", t.Context(), func(c *IsolatedCommand) { c.Platform = "darwin/arm64" }, e.CodeUsage},
		{"empty-image", t.Context(), func(c *IsolatedCommand) { c.Image = "" }, e.CodeUsage},
		{"option-image", t.Context(), func(c *IsolatedCommand) { c.Image = "--privileged" }, e.CodeUsage},
		{"relative-source", t.Context(), func(c *IsolatedCommand) { c.Source = "source" }, e.CodeUsage},
		{"relative-cache", t.Context(), func(c *IsolatedCommand) { c.Cache = "cache" }, e.CodeUsage},
		{"escaping-directory", t.Context(), func(c *IsolatedCommand) { c.WorkingDirectory = "../cli" }, e.CodeUsage},
		{"absolute-directory", t.Context(), func(c *IsolatedCommand) { c.WorkingDirectory = "/cli" }, e.CodeUsage},
		{"unclean-directory", t.Context(), func(c *IsolatedCommand) { c.WorkingDirectory = "cli/" }, e.CodeUsage},
		{"empty-command", t.Context(), func(c *IsolatedCommand) { c.Command = nil }, e.CodeUsage},
		{"empty-executable", t.Context(), func(c *IsolatedCommand) { c.Command = []string{""} }, e.CodeUsage},
		{"unnamed-environment", t.Context(), func(c *IsolatedCommand) { c.Environment = []string{"=value"} }, e.CodeUsage},
		{"option-environment", t.Context(), func(c *IsolatedCommand) { c.Environment = []string{"--privileged"} }, e.CodeUsage},
		{"relative-input", t.Context(), func(c *IsolatedCommand) { c.Inputs[0].Source = "nvkectl" }, e.CodeUsage},
		{"relative-target", t.Context(), func(c *IsolatedCommand) { c.Inputs[0].Target = "inputs/nvkectl" }, e.CodeUsage},
		{"source-target", t.Context(), func(c *IsolatedCommand) { c.Inputs[0].Target = "/src/cli/nvkectl" }, e.CodeUsage},
		{"cache-target", t.Context(), func(c *IsolatedCommand) { c.Inputs[0].Target = "/cache" }, e.CodeUsage},
		{"tmp-target", t.Context(), func(c *IsolatedCommand) { c.Inputs[0].Target = "/tmp/nvkectl" }, e.CodeUsage},
		{"root-target", t.Context(), func(c *IsolatedCommand) { c.Inputs[0].Target = "/" }, e.CodeUsage},
		{"duplicate-target", t.Context(), func(c *IsolatedCommand) { c.Inputs = append(c.Inputs, c.Inputs[0]) }, e.CodeUsage},
		{"no-timeout", t.Context(), func(c *IsolatedCommand) { c.Timeout = 0 }, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			command := isolatedFixture(t)
			tc.mutate(&command)
			err := runIsolatedCommand(tc.ctx, "nvkr-fixture", command, io.Discard, io.Discard, func(RunOptions, ...string) error {
				t.Fatal("refused command reached the runtime")
				return nil
			}, unexpectedArtifactCleanup(t))
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != tc.code {
				t.Fatalf("got %v, want %s", err, tc.code)
			}
		})
	}
}

func TestIsolatedCommandRefusesRoot(t *testing.T) {
	for _, tc := range []struct {
		uid     int
		refused bool
	}{{0, true}, {1, false}, {501, false}, {65534, false}} {
		err := admitIsolatedUser(tc.uid)
		var coded *e.Error
		if tc.refused != (err != nil) || (err != nil && (!errors.As(err, &coded) || coded.Code != e.CodeEngineCapability)) {
			t.Fatalf("uid %d: %v", tc.uid, err)
		}
	}
	if os.Getuid() == 0 {
		// Under root the public entry point must refuse before any runtime work.
		err := RunIsolatedCommand(t.Context(), isolatedFixture(t), io.Discard, io.Discard)
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
			t.Fatalf("root was admitted: %v", err)
		}
	}
}

func TestIsolatedCommandPreservesExitStatus(t *testing.T) {
	exit := exec.Command("sh", "-c", "exit 3").Run()
	cleaned := 0
	err := runIsolatedFixture(t, isolatedFixture(t), func(RunOptions, ...string) error { return exit },
		func(_ context.Context, _ time.Duration, _ string, names []string) error {
			cleaned++
			if len(names) != 1 || names[0] != "nvkr-fixture" {
				t.Errorf("cleanup lost the owned name: %v", names)
			}
			return nil
		})
	var status *exec.ExitError
	if !errors.As(err, &status) || status.ExitCode() != 3 {
		t.Fatalf("exit status not preserved: %v", err)
	}
	if cleaned != 1 {
		t.Fatal("failed container was not reaped")
	}
}

func TestIsolatedCommandTimeoutStopsAndReaps(t *testing.T) {
	command := isolatedFixture(t)
	command.Timeout = time.Millisecond
	cleaned := false
	err := runIsolatedFixture(t, command, func(opts RunOptions, _ ...string) error {
		<-opts.ctx().Done()
		return opts.ctx().Err()
	}, func(ctx context.Context, _ time.Duration, _ string, _ []string) error {
		cleaned = ctx.Err() == nil
		return nil
	})
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != e.CodeTimeout || !cleaned {
		t.Fatalf("%v cleaned=%t", err, cleaned)
	}
}

// runIsolatedFixture runs the fixture under a fixed owned name.
func runIsolatedFixture(t *testing.T, command IsolatedCommand, run func(RunOptions, ...string) error, cleanup reaperCleanupCommand) error {
	t.Helper()
	return runIsolatedCommand(t.Context(), "nvkr-fixture", command, io.Discard, io.Discard, run, cleanup)
}
