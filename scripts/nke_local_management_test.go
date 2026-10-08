// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package scripts_test

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

func TestNKELocalManagementConsumer(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "nke_local_management_test")
	cmd.WaitDelay = time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("management consumer contracts: %v\n%s", err, out)
	}
}

func TestNKELocalSiteConsumer(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "-B", "-m", "unittest", "nke_local_site_test")
	cmd.WaitDelay = time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("site consumer contracts: %v\n%s", err, out)
	}
}
