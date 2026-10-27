// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestRunCLICleanupSelection(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "default retain", true: "explicit cleanup"}[cleanup], func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			calls := 0
			actions := demoActions{
				load: func(context.Context, string) (nvkedemo.DemoFile, error) {
					return nvkedemo.DemoFile{Version: 1, Run: nvkedemo.RunFile{NVKECheckout: "/fixture/installer", NVKR: "/fixture/nvkr", Out: "/fixture/output", TimeoutSeconds: 300, Runs: nvkedemo.RunFileInputs{OS: "/os", Components: "/components", Providers: "/providers", Site: "/site", Tools: "/tools", Profiles: "/profiles", Simulator: "/simulator"}}}, nil
				},
				run: func(_ context.Context, cfg nvkedemo.Config) error {
					calls++
					if cfg.Cleanup != cleanup {
						t.Fatal("cleanup routing intent lost", cfg.Cleanup)
					}
					_, err := io.WriteString(cfg.Progress, "PASS: fixture public result\n")
					return err
				},
			}
			args := []string{"run", "--config", "/fixture.yaml", "--plain"}
			if cleanup {
				args = append(args, "--cleanup")
			}
			if err := executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), args); err != nil || calls != 1 {
				t.Fatal("native run routing failed", err, calls)
			}
			if !strings.Contains(out.String(), "fixture public result") {
				t.Fatal("native result was lost by presentation", out.String())
			}
		})
	}
}

func TestRetainedCLIHelpAndInvalidSelectorsDoNotMutate(t *testing.T) {
	for _, args := range [][]string{{"status", "--help"}, {"cleanup", "--help"}, {"status"}, {"cleanup"}, {"retained-owner"}, {"cleanup", "--session", "/fixture", "--config", "/must-not-read"}, {"retained-owner", "--session", "/fixture", "--plain"}} {
		var out, diagnostics bytes.Buffer
		called := false
		actions := demoActions{
			status:  func(context.Context, string, io.Writer) error { called = true; return nil },
			cleanup: func(context.Context, string, io.Writer) error { called = true; return nil },
			owner:   func(context.Context, string) error { called = true; return nil },
		}
		err := executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), args)
		if called {
			t.Fatal("help/invalid selector reached service work", args)
		}
		if args[len(args)-1] != "--help" {
			var coded *nvkrerr.Error
			if !errors.As(err, &coded) || coded.Code != nvkrerr.CodeUsage {
				t.Fatal("invalid retained command not coded usage", args, err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}
