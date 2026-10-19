// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Workload acceptance is identical for every execution host. The caller owns
// command execution and private diagnostics; this function owns the checks.
func runTenantWorkload(ctx context.Context, work, kubeconfig, kubectl string, capture func(context.Context, []string, string, time.Duration) ([]byte, error)) (json.RawMessage, error) {
	job := filepath.Join(work, "workload.json")
	if err := privateJSON(job, workloadJob()); err != nil {
		return nil, err
	}
	command := func(args ...string) []string {
		return append([]string{kubectl, "--kubeconfig", kubeconfig}, args...)
	}
	if _, err := capture(ctx, command("apply", "-f", job), "workload-apply", 30*time.Second); err != nil {
		return nil, err
	}
	if _, err := capture(ctx, command("wait", "job/nvkr-smoke", "--for=condition=Complete", "--timeout=210s"), "workload-complete", 240*time.Second); err != nil {
		return nil, err
	}
	raw, err := capture(ctx, command("logs", "job/nvkr-smoke"), "workload-log", 30*time.Second)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(strings.Split(strings.TrimSpace(string(raw)), "\n"), "NVKE_LOCAL_WORKLOAD_PASS") {
		return nil, errors.New("workload did not report ARM64 execution and cluster DNS success")
	}
	raw, err = capture(ctx, command("get", "pods", "-l", "job-name=nvkr-smoke", "-o", "json"), "workload-pod", 30*time.Second)
	if err != nil {
		return nil, err
	}
	var pods struct {
		Items []struct {
			Status struct {
				Containers []struct{ ImageID string } `json:"containerStatuses"`
			}
		}
	}
	if err := json.Unmarshal(raw, &pods); err != nil {
		return nil, err
	}
	var ids []string
	for _, pod := range pods.Items {
		for _, container := range pod.Status.Containers {
			ids = append(ids, container.ImageID)
		}
	}
	if len(ids) != 1 || strings.Split(ids[0], "@")[len(strings.Split(ids[0], "@"))-1] != strings.Split(WorkloadImage, "@")[1] {
		return nil, errors.New("workload image ID differs from pinned diagnostic acquisition")
	}
	return raw, nil
}
