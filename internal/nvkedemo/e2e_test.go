// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestEndToEnd is deliberately opt-in: ordinary go test does not download,
// build, create tenants or grant host controls. It invokes the customer CLI,
// which builds fresh Runs and deploys their exact outputs through the native path.
// This noninteractive test requires the documented unattended host controls;
// it never receives a password or opens a terminal for authentication.
func TestEndToEnd(t *testing.T) {
	if os.Getenv("NVKR_NVKE_E2E") != "1" {
		t.Skip("set NVKR_NVKE_E2E=1 on a dedicated native ARM64 host to build and deploy real NVKE")
	}
	if runtime.GOARCH != "arm64" || runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Fatal("E2E requires dedicated native Ubuntu or macOS ARM64")
	}
	required := func(key string) string {
		value := os.Getenv(key)
		if value == "" {
			t.Fatalf("explicit %s required", key)
		}
		return value
	}
	repo := required("NVKR_NVKE_E2E_REPO")
	out := required("NVKR_NVKE_E2E_OUT")
	attempt := required("NVKR_NVKE_E2E_ATTEMPT")
	binary := os.Getenv("NVKR_NVKE_E2E_BINARY")
	if binary == "" {
		binary = filepath.Join(repo, "bin", "nvke-demo")
	}
	// The CLI owns its 8/12-hour workflow deadline. Leave two more hours for
	// independent guest cleanup (65m), restoration (8m), bridge stop (1m)
	// and bounded diagnostics instead of killing it before those owners join.
	budget := 10 * time.Hour
	if runtime.GOOS == "linux" {
		budget += 4 * time.Hour
	}
	args := []string{binary, "e2e", "--repo", repo, "--attempt", attempt,
		"--out", out, "--jobs", "1", "--network", "ovn", "--cleanup", "--plain"}
	if clone := os.Getenv("NVKE_LOCAL_CLONE"); clone != "" {
		args = append(args, "--nvke-clone", clone)
	}
	if runtime.GOOS == "linux" {
		args = append(args, "--conformance")
	}
	if err := (Runner{Env: os.Environ(), Progress: os.Stdout}).Run(t.Context(), args, "full-native-e2e", budget); err != nil {
		t.Fatal(err)
	}
}
