// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// AdmitArtifactDiagnostic checks bootstrap's statically knowable host requirements
// before factory creates a workspace. The existing offline suites are unchanged.
// Host preparation remains owned by the upstream workflow or local environment.
func AdmitArtifactDiagnostic(ctx context.Context, input ArtifactDiagnostic) error {
	if input.Suite != "bootstrap" {
		return nil
	}
	if ctx == nil {
		return e.New(e.CodeUsage, "bootstrap requires a context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "bootstrap diagnostic canceled")
	}
	if input.Platform != "linux/amd64" || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" || os.Geteuid() != 0 {
		return e.New(e.CodeEngineCapability, "bootstrap diagnostic requires the upstream rootful native Linux AMD64 environment")
	}
	if input.ControllerImage == "" {
		return e.New(e.CodeUsage, "bootstrap requires its supporting controller image in upstream containerd")
	}
	for _, tool := range []string{"bash", "make", "go", "timeout", "setsid", "sha256sum", "nerdctl", "kind", "kubectl", "helm", "clusterctl", "iptables", "ip"} {
		if _, err := exec.LookPath(tool); err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "upstream bootstrap tool %s is unavailable", tool)
		}
	}
	return nil
}

// runBootstrapArtifactDiagnostic invokes the original upstream fixture on its
// prepared host. It does not install host services or copy upstream assertions.
// Cancellation uses the existing process-group owner; the independent termination
// grace lets the suite collect diagnostics and run its owned fixture teardown.
func runBootstrapArtifactDiagnostic(ctx context.Context, input ArtifactDiagnostic, stdout, stderr io.Writer) error {
	if err := AdmitArtifactDiagnostic(ctx, input); err != nil {
		return err
	}
	return executeBootstrapArtifactDiagnostic(ctx, input, stdout, stderr)
}

func executeBootstrapArtifactDiagnostic(ctx context.Context, input ArtifactDiagnostic, stdout, stderr io.Writer) error {
	if ctx == nil || input.Suite != "bootstrap" || input.Artifact == "" || input.Version == "" || input.ControllerImage == "" {
		return e.New(e.CodeUsage, "invalid bootstrap artifact diagnostic inputs")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "bootstrap execution canceled before start")
	}
	budget := input.Timeout
	if budget <= 0 {
		budget = 45 * time.Minute
	}
	bounded, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	log, err := os.OpenFile(filepath.Join(input.Workspace, "bootstrap.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return e.Wrap(e.CodeBuildFailed, err, "creating bootstrap diagnostic log")
	}
	cmd := execCmd(RunOptions{Ctx: bounded}, "bash", "./e2e/bootstrap/run.sh")
	cmd.Dir = filepath.Join(input.Workspace, "nvke", "cli")
	// Do not allow ambient Make controls to override the selected entrypoint or
	// executable. Preserve upstream runtime, networking and fixture configuration.
	overrides := map[string]string{
		"NVKECTL": input.Artifact, "NVKECTL_EXPECTED_VERSION": input.Version,
		"NVKECTL_CONTROLLER_IMAGE": input.ControllerImage,
		"NVKECTL_E2E_REPORT":       filepath.Join(input.Workspace, "bootstrap.json"),
	}
	for _, value := range os.Environ() {
		key, _, _ := strings.Cut(value, "=")
		if _, replace := overrides[key]; replace {
			continue
		}
		switch key {
		case "MAKEFLAGS", "GNUMAKEFLAGS", "MAKEFILES", "MAKEOVERRIDES", "MFLAGS":
			continue
		}
		cmd.Env = append(cmd.Env, value)
	}
	for _, key := range []string{"NVKECTL", "NVKECTL_EXPECTED_VERSION", "NVKECTL_CONTROLLER_IMAGE", "NVKECTL_E2E_REPORT"} {
		cmd.Env = append(cmd.Env, key+"="+overrides[key])
	}
	configureCommandCancellation(cmd, 2*time.Minute)
	cmd.Stdout = log
	cmd.Stderr = log
	runErr := runOwnedCommand(bounded, cmd)
	executionErr := bounded.Err()
	closeErr := log.Close()
	// Replay retained raw diagnostics after execution, preserving its primary error.
	var replayErr error
	if raw, err := os.Open(log.Name()); err == nil {
		if stdout != nil {
			_, replayErr = io.Copy(stdout, raw)
		}
		_ = raw.Close()
	} else {
		replayErr = err
	}
	// The upstream script records its primary failure before teardown and exits
	// with it afterward. A later deadline while that teardown finishes must not
	// replace a completed positive failure with cancellation.
	var exit *exec.ExitError
	if errors.As(runErr, &exit) && exit.ExitCode() > 0 {
		switch exit.ExitCode() {
		case 124, 130, 137, 143:
			return e.Wrap(e.CodeTimeout, runErr, "upstream bootstrap timed out or was canceled")
		default:
			return e.Wrap(e.CodeBuildFailed, runErr, "upstream bootstrap execution failed")
		}
	}
	if executionErr != nil {
		return e.Wrap(e.CodeTimeout, executionErr, "bootstrap execution canceled; cleanup may be incomplete")
	}
	if runErr != nil {
		return e.Wrap(e.CodeBuildFailed, runErr, "upstream bootstrap execution failed")
	}
	if closeErr != nil {
		return e.Wrap(e.CodeBuildFailed, closeErr, "closing bootstrap diagnostic log")
	}
	if replayErr != nil {
		return e.Wrap(e.CodeBuildFailed, replayErr, "replaying bootstrap diagnostic log")
	}
	return nil
}
