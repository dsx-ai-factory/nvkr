// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const workloadFixtureImage = "docker.io/library/alpine@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestTenantWorkloadRequiresExecutionDNSAndExactImage(t *testing.T) {
	for _, tc := range []struct {
		name, marker, image, selected, fail string
		copies                              int
		wantError                           bool
	}{
		{name: "accepted", marker: "NVKE_LOCAL_WORKLOAD_PASS\n", image: workloadFixtureImage, copies: 1},
		{name: "new acquired image", marker: "NVKE_LOCAL_WORKLOAD_PASS\n", selected: "example.invalid/workload@sha256:" + strings.Repeat("b", 64), image: "sha256:" + strings.Repeat("b", 64), copies: 1},
		{name: "missing execution marker", image: workloadFixtureImage, copies: 1, wantError: true},
		{name: "partial marker", marker: "prefixNVKE_LOCAL_WORKLOAD_PASS\n", image: workloadFixtureImage, copies: 1, wantError: true},
		{name: "wrong image", marker: "NVKE_LOCAL_WORKLOAD_PASS\n", image: "sha256:wrong", copies: 1, wantError: true},
		{name: "no observed container", marker: "NVKE_LOCAL_WORKLOAD_PASS\n", wantError: true},
		{name: "extra observed container", marker: "NVKE_LOCAL_WORKLOAD_PASS\n", image: workloadFixtureImage, copies: 2, wantError: true},
		{name: "wait failed", fail: "workload-complete", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected := tc.selected
			if selected == "" {
				selected = workloadFixtureImage
			}
			failed := errors.New("owned command failed")
			var phases []string
			capture := func(_ context.Context, args []string, phase string, budget time.Duration) ([]byte, error) {
				phases = append(phases, phase)
				if len(args) < 4 || args[0] != "kubectl" || args[1] != "--kubeconfig" || args[2] != "tenant.config" || budget < 0 || budget == 0 && phase != "workload-complete" {
					t.Fatalf("invalid command admission: %v %v", args, budget)
				}
				if phase == tc.fail {
					return nil, failed
				}
				if phase == "workload-log" {
					return []byte(tc.marker), nil
				}
				if phase == "workload-pod" {
					containers := make([]map[string]string, tc.copies)
					for i := range containers {
						containers[i] = map[string]string{"imageID": tc.image}
					}
					return json.Marshal(map[string]any{"items": []any{map[string]any{"status": map[string]any{"containerStatuses": containers}}}})
				}
				return nil, nil
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
			defer cancel()
			_, err := runTenantWorkload(ctx, t.TempDir(), "tenant.config", "kubectl", selected, capture)
			if (err != nil) != tc.wantError {
				t.Fatalf("acceptance error=%v; wantError=%v", err, tc.wantError)
			}
			if tc.fail != "" && (!errors.Is(err, failed) || strings.Join(phases, ",") != "workload-apply,workload-complete") {
				t.Fatalf("failed wait was bypassed: %v, phases=%v", err, phases)
			}
		})
	}
}
