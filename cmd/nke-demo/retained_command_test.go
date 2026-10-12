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

	"github.com/NVIDIA/nke-recipes/internal/nkedemo"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestRetainedCLIFlagAndPostPresenterHandoff(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "default retain", true: "explicit cleanup"}[cleanup], func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			calls, admitted := 0, 0
			actions := demoActions{
				load: func(context.Context, string) (nkedemo.DemoFile, error) {
					return nkedemo.DemoFile{Version: 1, Run: nkedemo.RunFile{NKECheckout: "/fixture/installer", NKDCTL: "/fixture/nkdctl", Out: "/fixture/output", TimeoutSeconds: 300, Runs: nkedemo.RunFileInputs{OS: "/os", Components: "/components", Providers: "/providers", Site: "/site", Tools: "/tools", Profiles: "/profiles", Simulator: "/simulator"}}}, nil
				},
				retention: func(context.Context) error { admitted++; return nil },
				run: func(_ context.Context, cfg nkedemo.Config) error {
					calls++
					if cfg.Cleanup != cleanup {
						t.Fatal("cleanup routing intent lost", cfg.Cleanup)
					}
					_, err := io.WriteString(cfg.Progress, "PASS: fixture public result\n")
					return err
				},
				after: func(_ context.Context, sink io.Writer) error {
					if sink != &out || !strings.Contains(out.String(), "fixture public result") {
						t.Fatal("handoff reached log ring or preceded presenter return")
					}
					_, err := io.WriteString(sink, "FULL COPYABLE HANDOFF\n")
					return err
				},
			}
			args := []string{"run", "--config", "/fixture.yaml", "--plain"}
			if cleanup {
				args = append(args, "--cleanup")
			}
			if err := executeRoot(t.Context(), newCommand(&out, &diagnostics, actions), args); err != nil || calls != 1 || admitted != map[bool]int{false: 1, true: 0}[cleanup] {
				t.Fatal("run/handoff admission failed", err, calls, admitted)
			}
			if !strings.HasSuffix(out.String(), "FULL COPYABLE HANDOFF\n") {
				t.Fatal("handoff was truncated or consumed by presentation", out.String())
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
			var coded *nkderr.Error
			if !errors.As(err, &coded) || coded.Code != nkderr.CodeUsage {
				t.Fatal("invalid retained command not coded usage", args, err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
}
