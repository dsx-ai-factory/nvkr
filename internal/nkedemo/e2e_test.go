// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestEndToEnd is deliberately opt-in: ordinary go test does not download,
// build, create tenants, grant host controls or touch a Colima installation.
// It builds one combined Run before deploying its exact newly completed outputs.
func TestEndToEnd(t *testing.T) {
	if os.Getenv("NKD_NKE_E2E") != "1" {
		t.Skip("set NKD_NKE_E2E=1 on dedicated Ubuntu ARM64 to build and deploy real NKE")
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		t.Fatal("E2E requires dedicated native Ubuntu ARM64, not macOS")
	}
	required := func(key string) string {
		value := os.Getenv(key)
		if value == "" {
			t.Fatalf("explicit %s required", key)
		}
		return value
	}
	repo := required("NKD_NKE_E2E_REPO")
	installer := os.Getenv("NKD_NKE_E2E_INSTALLER")
	out := required("NKD_NKE_E2E_OUT")
	attempt := required("NKD_NKE_E2E_ATTEMPT")
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Hour)
	defer cancel()
	build := BuildConfig{Repository: repo, NKEClone: os.Getenv("NKE_LOCAL_CLONE"), AttemptPrefix: attempt, Jobs: 1, Progress: os.Stdout}
	run, err := PrepareInstaller(ctx, build, Config{NKECheckout: installer, Out: out, Timeout: time.Hour, Progress: os.Stdout})
	if err != nil {
		t.Fatal(err)
	}
	built, err := BuildArtifacts(ctx, build)
	if err != nil {
		t.Fatal(err)
	}
	run.NKDCTL, run.Runs = built.NKDCTL, built.Runs
	run.DemoExecutable = filepath.Join(filepath.Dir(built.NKDCTL), "nke-demo")
	if err = Run(ctx, run); err != nil {
		t.Fatal(err)
	}
}
