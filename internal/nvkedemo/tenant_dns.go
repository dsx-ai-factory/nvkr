// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"io"
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Ready nodes alone do not prove the source-owned DNS controllers have rolled
// out. Both deployment paths use this gate before their single smoke submission.
func waitTenantDNS(ctx context.Context, kubeconfig, kubectl string, progress io.Writer, capture func(context.Context, []string, string, time.Duration) ([]byte, error)) (result error) {
	boundary := beginProgress(progress, "tenant-dns-readiness", PhaseStarted, PhaseFinished, PhaseFailed)
	stage := "waiting for tenant DNS readiness"
	defer func() {
		if result != nil {
			code := nvkrerr.CodePreflightFailed
			var coded *nvkrerr.Error
			switch {
			case errors.Is(result, context.Canceled), errors.Is(result, context.DeadlineExceeded):
				code = nvkrerr.CodeTimeout
			case errors.As(result, &coded):
				code = coded.Code
			}
			result = nvkrerr.Wrap(code, result, "%s", stage)
		}
		boundary.finish(result)
	}()
	if err := requireContext(ctx); err != nil {
		return err
	}
	for _, resource := range []struct{ name, phase string }{
		{"deployment/coredns", "dns-coredns-rollout"},
		{"daemonset/node-local-dns", "dns-node-local-rollout"},
	} {
		stage = "waiting for tenant DNS " + resource.name + " rollout"
		timeout, err := remainingWork(ctx)
		if err != nil {
			return err
		}
		args := []string{kubectl, "--kubeconfig", kubeconfig, "-n", "kube-system", "rollout", "status", resource.name, "--timeout=" + timeout.String()}
		if _, err := capture(ctx, args, resource.phase, 0); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}
