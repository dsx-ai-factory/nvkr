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
	runPythonContract(t, "nke_local_management_test")
}

func TestNKELocalSiteConsumer(t *testing.T) {
	runPythonContract(t, "nke_local_site_test")
}

func TestNKELocalNetworkConsumer(t *testing.T) {
	runPythonContract(t, "nke_local_network_test")
}

func TestNKELocalFullDemoConsumer(t *testing.T) {
	runPythonContract(t, "nke_local_demo_test")
}

// Go-only development may omit Python. The mandatory make check tooling gate
// requires Python and independently discovers every adapter suite; this skip
// is not a successful full-gate or runtime result.
func runPythonContract(t *testing.T, suite string) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python adapter contracts require python3; run make test-ci-tools with Python 3.12+")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-B", "-m", "unittest", suite)
	cmd.WaitDelay = time.Second
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Python adapter %s: %v\n%s", suite, err, out)
	}
}

func TestPythonContractMissingInterpreterIsExplicitSkip(t *testing.T) {
	skipped := false
	t.Run("missing python3", func(t *testing.T) {
		defer func() { skipped = t.Skipped() }()
		t.Setenv("PATH", t.TempDir())
		runPythonContract(t, "nke_local_demo_test")
	})
	if !skipped {
		t.Fatal("missing Python must be reported as a skip, not a passed contract")
	}
}
