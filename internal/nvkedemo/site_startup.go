// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Only readiness may converge under the deployment controllers. Every snapshot
// still validates all selected image identities and roles before another poll.
// This never retries deployment or the zero-retry tenant workload.
func waitRuntimePods(ctx context.Context, refs map[string]string, timeout, interval time.Duration,
	observe func(context.Context) (podList, error),
) (map[string]observedRuntime, error) {
	if err := requireContext(ctx); err != nil {
		return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "runtime startup observation canceled")
	}
	if timeout <= 0 || interval <= 0 || observe == nil {
		return nil, nvkrerr.New(nvkrerr.CodeUsage, "positive runtime startup budgets and observer required")
	}
	budget, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var last error
	for {
		if err := budget.Err(); err != nil {
			return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, errors.Join(last, err), "runtime startup did not converge")
		}
		rows, err := observe(budget)
		if err != nil {
			return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, errors.Join(last, err), "runtime startup observation failed")
		}
		if err = budget.Err(); err != nil {
			return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, errors.Join(last, err), "runtime startup observation canceled")
		}
		result, err := validateRuntimePods(rows, refs)
		if err == nil {
			return result, nil
		}
		var pending *runtimeReadinessError
		if !errors.As(err, &pending) {
			return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "runtime startup identity validation failed")
		}
		last = err
		if err = pause(budget, interval); err != nil {
			return nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, errors.Join(last, err), "runtime startup did not converge")
		}
	}
}

// A failed worker may be a consequence of frontend startup or service routing.
// Collect both sides before cleanup, without exposing raw diagnostics publicly.
func retainTemporalFailure(ctx context.Context, session ManagementSession, cause error, failed *runtimeReadinessError) error {
	budget, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	runner := session.Runner
	runner.Progress, runner.output = nil, nil
	kubectl := filepath.Join(session.Tools, "kubectl")
	kube := environmentValue(runner.Env, "SITE_KUBECONFIG")
	base := []string{kubectl, "--kubeconfig", kube, "--request-timeout=5s", "-n", "temporal"}
	var diagnosticErrors []error
	for _, item := range []struct {
		phase string
		args  []string
	}{
		{"runtime-failure-frontend-service", []string{"get", "service/temporal-frontend", "-o", "json"}},
		{"runtime-failure-frontend-endpointslices", []string{"get", "endpointslices", "-l", "kubernetes.io/service-name=temporal-frontend", "-o", "json"}},
	} {
		if err := runner.Run(budget, append(append([]string{}, base...), item.args...), item.phase, 5*time.Second); err != nil {
			if errors.Is(err, ErrUnjoined) {
				return errors.Join(cause, err)
			}
			diagnosticErrors = append(diagnosticErrors, err)
		}
	}
	if failed.container != "temporal-frontend" {
		selector := "app.kubernetes.io/name=temporal,app.kubernetes.io/instance=temporal,app.kubernetes.io/component=frontend"
		pods, err := captureJSON[podList](budget, runner, append(append([]string{}, base...), "get", "pods", "-l", selector, "-o", "json"), "runtime-failure-frontend-identity")
		if err != nil {
			return runtimeDiagnosticFailure(cause, err)
		}
		if len(pods.Items) != 1 || pods.Items[0].Metadata.Name == "" || pods.Items[0].Metadata.UID == "" || pods.Items[0].Metadata.Namespace != "temporal" {
			return runtimeDiagnosticFailure(cause, errors.New("frontend diagnostic subject is ambiguous"))
		}
		for _, previous := range []bool{false, true} {
			phase := "runtime-failure-frontend-current"
			args := append(append([]string{}, base...), "logs", "pod/"+pods.Items[0].Metadata.Name, "-c", "temporal-frontend", "--tail=100", "--limit-bytes=262144", "--timestamps=true")
			if previous {
				phase = "runtime-failure-frontend-previous"
				args = append(args, "--previous=true")
			}
			if err = runner.Run(budget, args, phase, 5*time.Second); err != nil {
				if errors.Is(err, ErrUnjoined) {
					return errors.Join(cause, err)
				}
				diagnosticErrors = append(diagnosticErrors, err)
			}
		}
	}
	if len(diagnosticErrors) != 0 {
		return runtimeDiagnosticFailure(cause, errors.Join(diagnosticErrors...))
	}
	return cause
}
