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

	"github.com/google/go-containerregistry/pkg/name"
)

// Workload acceptance is identical for every execution host. The caller owns
// command execution and private diagnostics; this function owns the checks.
func runTenantWorkload(ctx context.Context, work, kubeconfig, kubectl, image string, capture func(context.Context, []string, string, time.Duration) ([]byte, error)) (json.RawMessage, error) {
	digest, err := workloadImageDigest(image)
	if err != nil {
		return nil, err
	}
	job := filepath.Join(work, "workload.json")
	if err := privateJSON(job, workloadJob(image)); err != nil {
		return nil, err
	}
	command := func(args ...string) []string {
		return append([]string{kubectl, "--kubeconfig", kubeconfig}, args...)
	}
	if _, err := capture(ctx, command("apply", "-f", job), "workload-apply", 30*time.Second); err != nil {
		return nil, err
	}
	timeout, err := remainingWork(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := capture(ctx, command("wait", "job/nvkr-smoke", "--for=condition=Complete", "--timeout="+timeout.String()), "workload-complete", 0); err != nil {
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
	if len(ids) != 1 || strings.Split(ids[0], "@")[len(strings.Split(ids[0], "@"))-1] != digest {
		return nil, errors.New("workload image ID differs from pinned diagnostic acquisition")
	}
	return raw, nil
}

// The caller selects the reference from its admitted assets. This shared guard
// requires an exact OCI subject without introducing another image pin.
func workloadImageDigest(reference string) (string, error) {
	ref, err := name.NewDigest(reference, name.StrictValidation)
	if err != nil || !nativeImageReferenceIsCanonical(ref, reference) {
		return "", errors.New("workload requires an exact canonical image digest reference")
	}
	return ref.DigestStr(), nil
}
