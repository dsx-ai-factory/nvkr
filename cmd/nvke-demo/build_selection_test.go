// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	"github.com/spf13/cobra"
)

func TestBuildCommandSelectsExplicitNVKRInputs(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(map[bool]string{false: "flags", true: "configured overrides"}[configured], func(t *testing.T) {
			var output bytes.Buffer
			calls := 0
			actions := demoActions{
				load: func(context.Context, string) (nvkedemo.DemoFile, error) {
					return nvkedemo.DemoFile{Version: 1, Build: nvkedemo.BuildFile{Repo: "/repo", Attempt: "fresh", Jobs: 1, Definition: "old-definition.json", Environment: "old-environment.json", NVKR: "/old-nvkr"}}, nil
				},
				build: func(_ context.Context, cfg nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
					calls++
					if cfg.Definition != "/definition.json" || cfg.Environment != "/environment.json" || cfg.NVKR != "/native/nvkr" {
						t.Fatalf("selected inputs=%+v", cfg)
					}
					return nvkedemo.BuiltInputs{NVKR: cfg.NVKR}, nil
				},
			}
			args := []string{"build", "--repo", "/repo", "--attempt", "fresh", "--definition", "/definition.json", "--environment", "/environment.json", "--nvkr", "/native/nvkr", "--plain"}
			if configured {
				args = append(args, "--config", "/configuration.yaml")
			}
			if err := executeRoot(t.Context(), newCommand(&output, &output, actions), args); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("build calls=%d", calls)
			}
		})
	}
}

func TestBuildFlagPreservesConfiguredRunInput(t *testing.T) {
	var flags nvkedemo.DemoFile
	cmd := &cobra.Command{Use: "e2e"}
	addBuildFlags(cmd, &flags.Build)
	addRunFlags(cmd, &flags.Run, false)
	if err := cmd.Flags().Parse([]string{"--nvkr", "/native/build-nvkr"}); err != nil {
		t.Fatal(err)
	}
	actions := demoActions{load: func(context.Context, string) (nvkedemo.DemoFile, error) {
		return nvkedemo.DemoFile{Version: 1, Run: nvkedemo.RunFile{NVKR: "/retained/run-nvkr"}}, nil
	}}
	selected, err := configuredFile(cmd, "/fixture/config.yaml", flags, actions)
	if err != nil {
		t.Fatal(err)
	}
	if selected.Build.NVKR != "/native/build-nvkr" {
		t.Fatalf("build selection lost: %q", selected.Build.NVKR)
	}
	if selected.Run.NVKR != "/retained/run-nvkr" {
		t.Fatalf("build-only flag silently cleared the existing Run NVKR; E2E can no longer reject the retained input: %q", selected.Run.NVKR)
	}
}
