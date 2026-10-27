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
	"path/filepath"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// ArtifactDiagnostic describes a trusted-input test invocation, not an admitted
// producer or an authenticated Run. Each call uses a private empty cache.
type ArtifactDiagnostic struct {
	// DependencyTimeout overrides only acquisition; zero retains the legacy bound.
	DependencyTimeout                                    time.Duration
	Workspace, Artifact, Image, Platform, Version, Suite string
	Timeout                                              time.Duration
}

// RunArtifactDiagnostic acquires dependencies without the artifact and runs the
// selected upstream suite offline against read-only source and staged bytes.
func RunArtifactDiagnostic(ctx context.Context, input ArtifactDiagnostic, stdout, stderr io.Writer) error {
	err := runArtifactDiagnostic(ctx, input, stdout, stderr, runContainerRuntime, runReaperCleanupCommand)
	var coded *e.Error
	if err == nil || errors.As(err, &coded) {
		return err
	}
	return e.Wrap(e.CodeBuildFailed, err, "running artifact diagnostic")
}

func runArtifactDiagnostic(ctx context.Context, input ArtifactDiagnostic, stdout, stderr io.Writer,
	run func(RunOptions, ...string) error, cleanup reaperCleanupCommand) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if ctx == nil || (input.Suite != "contract" && input.Suite != "cli") ||
		(input.Platform != "linux/amd64" && input.Platform != "linux/arm64") || input.Image == "" || input.Version == "" {
		return e.New(e.CodeUsage, "invalid artifact diagnostic execution inputs")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "artifact diagnostic canceled")
	}
	cache := filepath.Join(input.Workspace, "cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating empty diagnostic cache")
	}
	name := containerName(ir.Node{ID: "artifact-test"})
	args := []string{"run", "--name", name, "--rm", "--init", "--platform", input.Platform,
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--workdir", "/src/cli",
		"--tmpfs", "/tmp:rw,exec,mode=1777", "--mount", diagnosticMount(filepath.Join(input.Workspace, "nvke"), "/src", true),
		"--mount", diagnosticMount(cache, "/cache", false)}
	for _, value := range []string{"GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=-mod=readonly -modcacherw",
		"CGO_ENABLED=0", "GOCACHE=/cache/go-build", "GOPATH=/cache/go", "HOME=/tmp"} {
		args = append(args, "--env", value)
	}
	timeout := input.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	container := func(arguments []string, output io.Writer) error {
		bounded, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		// Use the existing runtime boundary and cleanup registry. Registration
		// precedes process start; cleanup has an independent bounded context and
		// is joined before the diagnostic can remove its workspace.
		reap := newReaperWithCleanup(bounded, "docker", cleanup)
		defer reap.close()
		opts := RunOptions{Ctx: bounded, Runtime: "docker", Stdout: output, Stderr: stderr, reap: reap}
		if err := registerContainer(opts, name); err != nil {
			return err
		}
		err := run(opts, arguments...)
		// Classify execution before independently bounded cleanup. Its deadline
		// must not turn an already-completed failure into a timeout.
		executionErr := bounded.Err()
		if err == nil {
			reap.forget(name)
		}
		cancel()
		reap.close()
		if executionErr != nil {
			return e.Wrap(e.CodeTimeout, executionErr, "artifact diagnostic execution canceled")
		}
		return err
	}
	testTimeout := timeout
	if input.DependencyTimeout > 0 {
		timeout = input.DependencyTimeout
	}
	if err := container(append(append([]string{}, args...), input.Image, "go", "mod", "download"), stdout); err != nil {
		return err
	}
	timeout = testTimeout
	log, err := os.Create(filepath.Join(input.Workspace, "tests.jsonl"))
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating diagnostic log")
	}
	target := "test-artifact"
	if input.Suite == "cli" {
		target = "test"
	}
	offline := append(append([]string{}, args...), "--network", "none", "--env", "GOPROXY=off",
		"--mount", diagnosticMount(input.Artifact, "/artifact/nvkectl", true), input.Image,
		"make", "--no-print-directory", target, "GO=go", "GO_TEST_FLAGS=-count=1 -timeout=15m -json",
		"COVERAGE_DIR=/cache/coverage", "NVKECTL=/artifact/nvkectl", "NVKECTL_EXPECTED_VERSION="+input.Version)
	runErr := container(offline, log)
	closeErr := log.Close()
	// Replay only after child completion: a closed output consumer cannot replace
	// the process's primary failure or truncate its retained raw log.
	var replayErr error
	if raw, openErr := os.Open(log.Name()); openErr == nil {
		_, replayErr = io.Copy(stdout, raw)
		_ = raw.Close()
	} else {
		replayErr = openErr
	}
	if runErr != nil {
		return runErr
	}
	if closeErr != nil {
		return e.Wrap(e.CodeBuildFailed, closeErr, "closing diagnostic log")
	}
	if replayErr != nil {
		return e.Wrap(e.CodeBuildFailed, replayErr, "replaying diagnostic log")
	}
	return nil
}

func diagnosticMount(source, target string, readonly bool) string {
	var output strings.Builder
	writer := csv.NewWriter(&output)
	fields := []string{"type=bind", "src=" + source, "dst=" + target}
	if readonly {
		fields = append(fields, "readonly")
	}
	_ = writer.Write(fields)
	writer.Flush()
	return strings.TrimSuffix(output.String(), "\n")
}
