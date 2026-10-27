// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
)

func TestGuestCommandPreservesLocalSelection(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, accelerator := range []string{"hvf", "tcg"} {
		t.Run(accelerator, func(t *testing.T) {
			calls := 0
			root := newCommand(io.Discard, io.Discard, demoActions{guest: func(_ context.Context, cfg nvkedemo.NativeGuestConfig) error {
				calls++
				if cfg.Image != "/local/node.qcow2" || cfg.Tools != "/local/tools" || cfg.Out != "/local/attempt" || cfg.Accelerator != accelerator || cfg.Timeout != 5*time.Minute || cfg.DemoExecutable != executable {
					t.Fatal("local guest selection changed")
				}
				return nil
			}})
			if err := executeRoot(t.Context(), root, []string{"guest", "--plain", "--image", "/local/node.qcow2", "--tools", "/local/tools", "--out", "/local/attempt", "--accelerator", accelerator, "--timeout", "5m"}); err != nil || calls != 1 {
				t.Fatalf("guest command = %v; calls=%d", err, calls)
			}
		})
	}
}

func TestGuestCommandHelpAndMissingInputsDoNotLaunch(t *testing.T) {
	for _, args := range [][]string{{"guest", "--help"}, {"guest"}, {"guest", "--image", "/local/node.qcow2"}} {
		calls := 0
		root := newCommand(io.Discard, io.Discard, demoActions{guest: func(context.Context, nvkedemo.NativeGuestConfig) error { calls++; return nil }})
		_ = executeRoot(t.Context(), root, args)
		if calls != 0 {
			t.Fatal("incomplete inputs started runtime work")
		}
	}
}
