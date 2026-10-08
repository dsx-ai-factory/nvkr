// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestNKEChartInputsAdapter(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "nke_prepare_chart_inputs_test")
	cmd.WaitDelay = time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("chart handoff contracts: %v\n%s", err, out)
	}
}
