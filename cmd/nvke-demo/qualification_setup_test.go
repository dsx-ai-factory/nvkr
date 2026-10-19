// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestQualificationSetupCommandAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		help bool
	}{
		{"setup help", []string{"qualification-setup", "--help"}, true},
		{"stop help", []string{"qualification-registry-stop", "--help"}, true},
		{"missing config", []string{"qualification-setup", "--out", "/unused"}, false},
		{"missing setup directory", []string{"qualification-setup", "--config", "/unused"}, false},
		{"setup positional", []string{"qualification-setup", "extra"}, false},
		{"missing stop directory", []string{"qualification-registry-stop"}, false},
		{"stop positional", []string{"qualification-registry-stop", "extra"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actions := demoActions{load: func(context.Context, string) (nvkedemo.DemoFile, error) {
				t.Fatal("help or invalid input reached configuration I/O")
				return nvkedemo.DemoFile{}, nil
			}}
			err := executeRoot(t.Context(), newCommand(io.Discard, io.Discard, actions), tc.args)
			if tc.help {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertCode(t, err, e.CodeUsage)
		})
	}
}

func TestAutomaticQualificationFlagOverridesConfiguration(t *testing.T) {
	for _, command := range []string{"build", "e2e"} {
		for _, value := range []bool{false, true} {
			t.Run(command+map[bool]string{false: "/disabled", true: "/enabled"}[value], func(t *testing.T) {
				called := false
				stop := e.New(e.CodePreflightFailed, "stop before runtime")
				actions := demoActions{
					load: func(context.Context, string) (nvkedemo.DemoFile, error) {
						return nvkedemo.DemoFile{Version: 1, Build: nvkedemo.BuildFile{Repo: "/repo", Attempt: "fresh", Jobs: 1, QualifyLocal: !value}, Run: nvkedemo.RunFile{Out: t.TempDir() + "/out", TimeoutSeconds: nvkedemo.DefaultTenantTimeoutSeconds}}, nil
					},
					build: func(_ context.Context, cfg nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
						called = true
						if cfg.QualifyLocal != value {
							t.Fatal("flag override lost")
						}
						return nvkedemo.BuiltInputs{}, stop
					},
					prepare: func(_ context.Context, cfg nvkedemo.BuildConfig, _ nvkedemo.Config) (nvkedemo.Config, error) {
						called = true
						if cfg.QualifyLocal != value {
							t.Fatal("flag override lost")
						}
						return nvkedemo.Config{}, stop
					},
				}
				flag := "--qualify-local=false"
				if value {
					flag = "--qualify-local"
				}
				err := executeRoot(t.Context(), newCommand(io.Discard, io.Discard, actions), []string{command, "--config", "/config", flag, "--plain"})
				assertCode(t, err, e.CodePreflightFailed)
				if !called {
					t.Fatal("action not reached")
				}
			})
		}
	}
}
