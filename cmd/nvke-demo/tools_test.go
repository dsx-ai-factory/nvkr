// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
)

func TestToolsCommandDelegatesAndHelpDoesNoWork(t *testing.T) {
	for _, help := range []bool{true, false} {
		t.Run(map[bool]string{true: "help", false: "prepare"}[help], func(t *testing.T) {
			var output bytes.Buffer
			called := false
			actions := demoActions{tools: func(_ context.Context, cfg nvkedemo.ToolConfig) (string, error) {
				called = true
				if cfg.Repository != "/repo" || cfg.NVKR != "/nvkr" || cfg.Attempt != "test" || cfg.Out != "/out" || cfg.Progress == nil {
					t.Fatalf("lost tool selection: %+v", cfg)
				}
				return "/out/bin", nil
			}}
			args := []string{"tools", "--plain", "--repo", "/repo", "--nvkr", "/nvkr", "--attempt", "test", "--out", "/out"}
			if help {
				args = []string{"tools", "--help"}
			}
			if err := executeRoot(t.Context(), newCommand(&output, &output, actions), args); err != nil {
				t.Fatal(err)
			}
			if called == help {
				t.Fatal("help/delegation boundary changed")
			}
			if !help && !strings.Contains(output.String(), "/out/bin") {
				t.Fatal("prepared tool path omitted")
			}
		})
	}
}
