// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"encoding/csv"
	"errors"
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
	// DependencyTimeout overrides only acquisition; zero reuses Timeout.
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
	// Both phases share one owned container name and the isolated-command baseline.
	name := containerName(ir.Node{ID: "artifact-test"})
	environment := []string{"GOTOOLCHAIN=local", "GOWORK=off", "GOFLAGS=-mod=readonly -modcacherw",
		"CGO_ENABLED=0", "GOCACHE=/cache/go-build", "GOPATH=/cache/go", "HOME=/tmp"}
	timeout := input.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	acquisitionTimeout := timeout
	if input.DependencyTimeout > 0 {
		acquisitionTimeout = input.DependencyTimeout
	}
	acquire := IsolatedCommand{Image: input.Image, Platform: input.Platform, Source: filepath.Join(input.Workspace, "nvke"), Cache: cache,
		WorkingDirectory: "cli", Command: []string{"go", "mod", "download"}, Environment: environment}
	if err := runBoundedContainer(ctx, name, acquisitionTimeout, acquire.arguments(name), stdout, stderr, run, cleanup); err != nil {
		return err
	}
	log, err := os.Create(filepath.Join(input.Workspace, "tests.jsonl"))
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating diagnostic log")
	}
	target := "test-artifact"
	if input.Suite == "cli" {
		target = "test"
	}
	offline := acquire
	offline.Offline = true
	offline.Environment = append(append([]string{}, environment...), "GOPROXY=off")
	offline.Inputs = []IsolatedMount{{Source: input.Artifact, Target: "/artifact/nvkectl"}}
	offline.Command = []string{"make", "--no-print-directory", target, "GO=go", "GO_TEST_FLAGS=-count=1 -timeout=15m -json",
		"COVERAGE_DIR=/cache/coverage", "NVKECTL=/artifact/nvkectl", "NVKECTL_EXPECTED_VERSION=" + input.Version}
	runErr := runBoundedContainer(ctx, name, timeout, offline.arguments(name), log, stderr, run, cleanup)
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
