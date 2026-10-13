// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkee2estaging"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func requiredFlags() []string {
	return []string{"--run", "run.json", "--plan", "plan.json", "--definition", "definition.yaml",
		"--environment", "environment.yaml", "--inputs", "inputs.json", "--out", "fresh",
		"--repository", "example/nvkr", "--run-id", "123", "--run-attempt", "2",
		"--reference", "registry.example.test/release@sha256:" + strings.Repeat("a", 64),
		"--source-node", "fetch-exact", "--stream-name", "forge-gb200-amd64-ubuntu-26.04-kube-1.36",
		"--gpu", "gb200", "--k8s", "1.36", "--arch", "amd64", "--ubuntu", "26.04", "--versions", "controller=exact"}
}

func TestHelpAndInvalidModesDoNotReachPreparation(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		code e.Code
	}{
		{"help", []string{"--help"}, ""},
		{"missing inputs", []string{"--plan-only"}, e.CodeUsage},
		{"missing mode", requiredFlags(), e.CodeUsage},
		{"both modes", append(requiredFlags(), "--stage", "--plan-only"), e.CodeUsage},
		{"unstaged compile", append(requiredFlags(), "--plan-only", "--compile-only"), e.CodeUsage},
		{"positional argument", append(requiredFlags(), "--plan-only", "extra"), e.CodeUsage},
		{"malformed verification timeout", append(requiredFlags(), "--stage", "--verify-timeout=unknown"), e.CodeUsage},
		{"zero verification timeout", append(requiredFlags(), "--stage", "--verify-timeout=0s"), e.CodeUsage},
		{"negative verification timeout", append(requiredFlags(), "--plan-only", "--verify-timeout=-1s"), e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			var out, diagnostics bytes.Buffer
			err := execute(t.Context(), test.args, &out, &diagnostics, func(context.Context, nvkee2estaging.Options) (nvkee2estaging.Report, error) {
				t.Fatal("help or invalid flags reached preparation")
				return nvkee2estaging.Report{}, nil
			})
			if test.code == "" {
				if err != nil || !strings.Contains(out.String(), "--stage") {
					t.Fatalf("help = %q, %v", out.String(), err)
				}
				return
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("error = %v, want %s", err, test.code)
			}
			if out.Len() != 0 {
				t.Fatalf("invalid flags produced output: %q", out.String())
			}
		})
	}
}

func TestPreparationFlagsAndOwningErrorArePreserved(t *testing.T) {
	for _, stage := range []bool{false, true} {
		t.Run(map[bool]string{false: "plan", true: "stage"}[stage], func(t *testing.T) {
			mode := "--plan-only"
			if stage {
				mode = "--stage"
			}
			args := append(requiredFlags(), mode, "--github-server-url", "https://github.example.test")
			want := e.New(e.CodeVerifyFailed, "owning verification rejected candidate")
			var out, diagnostics bytes.Buffer
			err := execute(t.Context(), args, &out, &diagnostics, func(ctx context.Context, options nvkee2estaging.Options) (nvkee2estaging.Report, error) {
				if ctx == nil || options.PlanOnly == stage || options.RunID != "123" || options.RunAttempt != "2" ||
					options.GitHubServerURL != "https://github.example.test" || options.SourceNode != "fetch-exact" || options.VerifyTimeout != 30*time.Minute {
					t.Fatalf("flag mapping = %+v", options)
				}
				return nvkee2estaging.Report{}, want
			})
			if !errors.Is(err, want) || out.Len() != 0 {
				t.Fatalf("owner error changed or success printed: %v %q", err, out.String())
			}
		})
	}
}

func TestVerificationTimeoutOverrideReachesPreparation(t *testing.T) {
	for _, duration := range []time.Duration{time.Second, 60 * time.Minute} {
		t.Run(duration.String(), func(t *testing.T) {
			args := append(requiredFlags(), "--stage", "--verify-timeout="+duration.String())
			want := e.New(e.CodeTimeout, "owning verification reached its deadline")
			var out, diagnostics bytes.Buffer
			err := execute(t.Context(), args, &out, &diagnostics, func(_ context.Context, options nvkee2estaging.Options) (nvkee2estaging.Report, error) {
				if options.VerifyTimeout != duration {
					t.Fatalf("verification timeout=%s, want %s", options.VerifyTimeout, duration)
				}
				return nvkee2estaging.Report{}, want
			})
			if !errors.Is(err, want) || out.Len() != 0 {
				t.Fatalf("owner timeout changed or success printed: %v %q", err, out.String())
			}
		})
	}
}
