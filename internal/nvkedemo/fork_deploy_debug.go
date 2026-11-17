// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// TEMPORARY FORK-ONLY DIAGNOSTICS. Revert this entire change before review.
// The existing private, bounded combined stdout/stderr sink is retained outside
// the disposable attempt. No diagnostic bytes enter the public progress writer.
func temporaryForkDeployDebug(r Runner) (Runner, bool, error) {
	if os.Getenv("NVKR_NKTP_DEBUG") != "1" || os.Getenv("GITHUB_REPOSITORY") != "njhensley/nvkr" ||
		os.Getenv("GITHUB_REF") != "refs/heads/fix/nvkr-nktp-chart-source" || os.Getenv("GITHUB_EVENT_NAME") != "workflow_dispatch" {
		return r, false, nil
	}
	root := os.Getenv("RUNNER_TEMP")
	if !filepath.IsAbs(root) {
		return r, false, os.ErrInvalid
	}
	dir := filepath.Join(root, "nktp-deploy-debug-plain")
	if err := os.Mkdir(dir, 0o700); err != nil {
		return r, false, err
	}
	if r.directory == "" {
		r.directory = r.Work
	}
	r.Work = dir
	r.output = nil
	return r, true, nil
}

func temporaryForkDeployFailure(ctx context.Context, r Runner, tools, kubeconfig string) {
	// Capture before the caller destroys the VM. The original work deadline may
	// already have expired; this bounded diagnostic owns no persistent service.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	r.Progress, r.output = nil, nil
	base := []string{filepath.Join(tools, "kubectl"), "--kubeconfig", kubeconfig, "--request-timeout=8s"}
	_ = r.Run(ctx, append(append([]string{}, base...), "get", "pods,deployments,statefulsets,daemonsets,jobs,services,persistentvolumeclaims", "-A", "-o", "wide"), "kubectl-get-wide", 12*time.Second)
	_ = r.Run(ctx, append(append([]string{}, base...), "get", "events", "-A", "-o", "wide", "--sort-by=.metadata.creationTimestamp"), "kubectl-events", 12*time.Second)
	for _, namespace := range []string{"nktp", "postgres", "temporal", "spire-server", "spire-system", "nvke-controller", "capnico-system"} {
		_ = r.Run(ctx, append(append([]string{}, base...), "describe", "pods,deployments,statefulsets,daemonsets,jobs,persistentvolumeclaims", "-n", namespace), "kubectl-describe-"+namespace, 12*time.Second)
	}
	// Only this exact failure enables the later encryption step. The environment
	// file receives a fixed boolean, never paths, diagnostic bytes or credentials.
	file, err := os.OpenFile(os.Getenv("GITHUB_ENV"), os.O_APPEND|os.O_WRONLY, 0)
	if err == nil {
		_, _ = file.WriteString("NVKR_HOST_DEPLOY_FAILED=1\n")
		_ = file.Close()
	}
}
