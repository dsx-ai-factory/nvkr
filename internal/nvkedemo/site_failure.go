// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// A failed readiness observation is not acceptance or authority to change a pod.
// Keep its exact subject private while surfacing the container and exit status.
type runtimeReadinessError struct {
	dependency string
	pod        podMetadata
	container  string
	state      podContainerState
}

func (e *runtimeReadinessError) Error() string {
	if e.state.State.Terminated != nil && e.state.State.Terminated.ExitCode != nil {
		return fmt.Sprintf("deployed dependency not ready or completed: %s; container %q terminated with exit code %d", e.dependency, e.container, *e.state.State.Terminated.ExitCode)
	}
	return fmt.Sprintf("deployed dependency not ready or completed: %s; container %q ready=%t running=%t", e.dependency, e.container, e.state.Ready, e.state.State.Running != nil)
}

// Retain current/previous container logs before management cleanup removes the
// node. These bounded private diagnostics never reach the public output writer,
// do not retry deployment, and cannot turn the original failure into success.
func retainRuntimeFailure(ctx context.Context, session ManagementSession, cause error) error {
	var failed *runtimeReadinessError
	if !errors.As(cause, &failed) {
		return cause
	}
	if failed.pod.Name == "" || failed.pod.Namespace == "" || failed.pod.UID == "" || failed.container == "" {
		return fmt.Errorf("%w; runtime diagnostic subject is incomplete", cause)
	}
	budget, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	runner := session.Runner
	runner.Progress, runner.output = nil, nil
	kubectl := filepath.Join(session.Tools, "kubectl")
	kube := environmentValue(runner.Env, "SITE_KUBECONFIG")
	pod, err := captureJSON[sitePod](budget, runner, []string{kubectl, "--kubeconfig", kube, "--request-timeout=5s", "-n", failed.pod.Namespace, "get", "pod/" + failed.pod.Name, "-o", "json"}, "runtime-failure-identity")
	if err != nil {
		return runtimeDiagnosticFailure(cause, err)
	}
	if pod.Metadata.UID != failed.pod.UID || pod.Metadata.Name != failed.pod.Name || pod.Metadata.Namespace != failed.pod.Namespace {
		return fmt.Errorf("%w; runtime diagnostic pod identity changed; logs not collected", cause)
	}
	var logErrors []error
	for _, previous := range []bool{false, true} {
		phase := "runtime-failure-current"
		args := []string{kubectl, "--kubeconfig", kube, "--request-timeout=5s", "-n", failed.pod.Namespace, "logs", "pod/" + failed.pod.Name, "-c", failed.container, "--tail=100", "--limit-bytes=262144", "--timestamps=true"}
		if previous {
			phase = "runtime-failure-previous"
			args = append(args, "--previous=true")
		}
		if err := runner.Run(budget, args, phase, 5*time.Second); err != nil {
			logErrors = append(logErrors, err)
			if errors.Is(err, ErrUnjoined) {
				// An unjoined child must still block resource removal.
				return errors.Join(cause, err)
			}
		}
	}
	if len(logErrors) != 0 {
		return runtimeDiagnosticFailure(cause, errors.Join(logErrors...))
	}
	return fmt.Errorf("%w; private runtime-failure-current.log and runtime-failure-previous.log retained", cause)
}

func runtimeDiagnosticFailure(cause, diagnostic error) error {
	if errors.Is(diagnostic, ErrUnjoined) {
		return errors.Join(cause, diagnostic)
	}
	// A diagnostic deadline must not replace the primary readiness error's code.
	// Raw provider messages and private subject/path details stay in command logs.
	return fmt.Errorf("%w; private runtime failure diagnostics incomplete", cause)
}
