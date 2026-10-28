// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBuildQualificationPolicyFlagPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		want    string
		invalid bool
	}{
		{"configured", nil, "/trust/configured.json", false},
		{"explicit", []string{"--qualification-policy", "/trust/override.json"}, "/trust/override.json", false},
		{"empty", []string{"--qualification-policy="}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			actions := demoActions{
				load: func(context.Context, string) (nvkedemo.DemoFile, error) {
					return nvkedemo.DemoFile{Version: 1, Build: nvkedemo.BuildFile{Repo: "/repo", Attempt: "fresh", Jobs: 1, QualificationPolicy: "/trust/configured.json"}}, nil
				},
				build: func(_ context.Context, cfg nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
					called = true
					if cfg.QualificationPolicy != tc.want {
						t.Fatalf("selected policy %q, want %q", cfg.QualificationPolicy, tc.want)
					}
					return nvkedemo.BuiltInputs{}, nil
				},
			}
			args := append([]string{"build", "--plain", "--config", "/demo.yaml"}, tc.args...)
			err := executeRoot(t.Context(), newCommand(io.Discard, io.Discard, actions), args)
			if tc.invalid {
				assertCode(t, err, nvkrerr.CodeUsage)
			} else if err != nil {
				t.Fatal(err)
			}
			if called == tc.invalid {
				t.Fatalf("build called=%t with invalid=%t", called, tc.invalid)
			}
		})
	}
}

func TestPreparedBuildFlagPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"configured", nil, true},
		{"explicit true", []string{"--prepared"}, true},
		{"explicit false", []string{"--prepared=false"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			actions := demoActions{
				load: func(context.Context, string) (nvkedemo.DemoFile, error) {
					return nvkedemo.DemoFile{Version: 1, Build: nvkedemo.BuildFile{Repo: "/repo", Attempt: "fresh", Jobs: 1, Prepared: true, QualificationPolicy: "/trust/policy.json"}}, nil
				},
				build: func(_ context.Context, cfg nvkedemo.BuildConfig) (nvkedemo.BuiltInputs, error) {
					called = true
					if cfg.Prepared != tc.want {
						t.Fatalf("prepared=%t want=%t", cfg.Prepared, tc.want)
					}
					return nvkedemo.BuiltInputs{}, nil
				},
			}
			args := append([]string{"build", "--plain", "--config", "/demo.yaml"}, tc.args...)
			if err := executeRoot(t.Context(), newCommand(io.Discard, io.Discard, actions), args); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("build was not called")
			}
		})
	}
}
