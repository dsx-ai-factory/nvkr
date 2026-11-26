// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// Fixed container paths of an isolated command.
const (
	IsolatedSourcePath = "/src"
	IsolatedCachePath  = "/cache"
	isolatedTempPath   = "/tmp"
)

// IsolatedMount mounts one host file or directory read-only at an absolute
// container path outside /src, /cache and /tmp.
type IsolatedMount struct {
	Source, Target string
}

// IsolatedCommand runs one argument vector in a pinned image under fixed
// isolation: the invoking non-root user's UID and GID, a read-only root filesystem, all
// capabilities dropped, no new privileges and a private /tmp. Source is mounted
// read-only at /src and Cache writable at /cache; Inputs are read-only. Offline
// removes all network access. Callers choose the command, environment, inputs and
// network for each phase; they cannot add mounts, devices or privileges.
type IsolatedCommand struct {
	Image, Platform string
	Source, Cache   string
	// WorkingDirectory is relative to /src; empty runs from /src.
	WorkingDirectory string
	Inputs           []IsolatedMount
	Command          []string
	// Environment holds NAME=value bindings.
	Environment []string
	Offline     bool
	Timeout     time.Duration
}

var isolatedEnvironmentName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// AdmitIsolatedCommandUser refuses to run isolated commands as root. They run
// as the invoking UID, and without user-namespace remapping UID 0 in the
// container is root on the host if the container is ever escaped.
func AdmitIsolatedCommandUser() error { return admitIsolatedUser(os.Getuid()) }

func admitIsolatedUser(uid int) error {
	if uid == 0 {
		return e.New(e.CodeEngineCapability, "isolated commands must not run as root; run NVKR as an unprivileged user")
	}
	return nil
}

// RunIsolatedCommand runs the command to completion or its timeout. It refuses
// root before any runtime work. A started container is registered for bounded
// force-removal before it starts. A process exit status is preserved for
// errors.As; timeout and cancellation are CodeTimeout.
func RunIsolatedCommand(ctx context.Context, command IsolatedCommand, stdout, stderr io.Writer) error {
	if err := AdmitIsolatedCommandUser(); err != nil {
		return err
	}
	err := runIsolatedCommand(ctx, containerName(ir.Node{ID: "validation"}), command, stdout, stderr, runContainerRuntime, runReaperCleanupCommand)
	var coded *e.Error
	if err == nil || errors.As(err, &coded) {
		return err
	}
	return e.Wrap(e.CodeBuildFailed, err, "running isolated command")
}

func runIsolatedCommand(ctx context.Context, name string, command IsolatedCommand, stdout, stderr io.Writer,
	run func(RunOptions, ...string) error, cleanup reaperCleanupCommand) error {
	if ctx == nil || !command.valid() {
		return e.New(e.CodeUsage, "invalid isolated command")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "isolated command canceled")
	}
	return runBoundedContainer(ctx, name, command.Timeout, command.arguments(name), stdout, stderr, run, cleanup)
}

func (c IsolatedCommand) valid() bool {
	if (c.Platform != "linux/amd64" && c.Platform != "linux/arm64") || c.Image == "" || strings.HasPrefix(c.Image, "-") ||
		!filepath.IsAbs(c.Source) || !filepath.IsAbs(c.Cache) || len(c.Command) == 0 || c.Command[0] == "" || c.Timeout <= 0 {
		return false
	}
	if c.WorkingDirectory != "" && (path.IsAbs(c.WorkingDirectory) || path.Clean(c.WorkingDirectory) != c.WorkingDirectory ||
		c.WorkingDirectory == ".." || strings.HasPrefix(c.WorkingDirectory, "../")) {
		return false
	}
	for _, binding := range c.Environment {
		if !isolatedEnvironmentName.MatchString(binding) {
			return false
		}
	}
	targets := map[string]bool{}
	for _, input := range c.Inputs {
		target := input.Target
		if !filepath.IsAbs(input.Source) || !path.IsAbs(target) || path.Clean(target) != target || target == "/" || targets[target] {
			return false
		}
		for _, reserved := range []string{IsolatedSourcePath, IsolatedCachePath, isolatedTempPath} {
			if target == reserved || strings.HasPrefix(target, reserved+"/") {
				return false
			}
		}
		targets[target] = true
	}
	return true
}

func (c IsolatedCommand) arguments(name string) []string {
	args := []string{"run", "--name", name, "--rm", "--init", "--platform", c.Platform,
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--workdir", path.Join(IsolatedSourcePath, c.WorkingDirectory),
		"--tmpfs", isolatedTempPath + ":rw,exec,mode=1777", "--mount", diagnosticMount(c.Source, IsolatedSourcePath, true),
		"--mount", diagnosticMount(c.Cache, IsolatedCachePath, false)}
	if c.Offline {
		args = append(args, "--network", "none")
	}
	for _, input := range c.Inputs {
		args = append(args, "--mount", diagnosticMount(input.Source, input.Target, true))
	}
	for _, binding := range c.Environment {
		args = append(args, "--env", binding)
	}
	return append(append(args, c.Image), c.Command...)
}

// runBoundedContainer uses the existing runtime boundary and cleanup registry.
// Registration precedes process start; cleanup has an independent bounded context
// and is joined before returning, so callers may then remove mounted paths.
func runBoundedContainer(ctx context.Context, name string, timeout time.Duration, args []string, stdout, stderr io.Writer,
	run func(RunOptions, ...string) error, cleanup reaperCleanupCommand) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	reap := newReaperWithCleanup(bounded, "docker", cleanup)
	defer reap.close()
	opts := RunOptions{Ctx: bounded, Runtime: "docker", Stdout: stdout, Stderr: stderr, reap: reap}
	if err := registerContainer(opts, name); err != nil {
		return err
	}
	err := run(opts, args...)
	// Classify execution before independently bounded cleanup. Its deadline
	// must not turn an already-completed failure into a timeout.
	executionErr := bounded.Err()
	if err == nil {
		reap.forget(name)
	}
	cancel()
	reap.close()
	if executionErr != nil {
		return e.Wrap(e.CodeTimeout, executionErr, "container execution canceled")
	}
	return err
}
