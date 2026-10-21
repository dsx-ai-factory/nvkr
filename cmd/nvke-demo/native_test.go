// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
)

func TestNativeCommandSelectsLocalArtifactsWithoutBuildOrUpload(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	root := newCommand(io.Discard, io.Discard, demoActions{native: func(_ context.Context, cfg nvkedemo.NativeConfig) error {
		calls++
		if cfg.Image != "/local/node.qcow2" || cfg.Accelerator != "tcg" || cfg.Out != "/local/run" || cfg.Assets.ToolsRun != "/local/tools" || cfg.Assets.RuntimeRun != "/local/runtime-images" || cfg.Assets.NVKR != "/local/nvkr" || cfg.Cleanup || cfg.DemoExecutable != executable {
			t.Fatal("local native configuration changed")
		}
		return nil
	}})
	err = executeRoot(t.Context(), root, []string{"native", "--plain", "--image", "/local/node.qcow2", "--accelerator", "tcg", "--out", "/local/run", "--nvkr", "/local/nvkr", "--tools-run", "/local/tools", "--runtime-run", "/local/runtime-images", "--bootstrap-run", "/local/bootstrap", "--deployment-run", "/local/deployment"})
	if err != nil || calls != 1 {
		t.Fatalf("native command=%v calls=%d", err, calls)
	}
}
