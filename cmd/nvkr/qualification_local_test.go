// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

func TestQualifyLocalCommand(t *testing.T) {
	for _, mode := range []string{"exact", "missing", "positional", "failure"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			var output bytes.Buffer
			command := cmdRunQualifyLocalWith(func(_ context.Context, options lifecycle.LocalQualificationOptions) (*lifecycle.LocalQualificationResult, error) {
				calls++
				if options.Workspace != "attempt" || options.PolicyPath != "policy" || options.BuilderKeyPath != "builder" || options.SmokeKeyPath != "smoke" || options.QualifierKeyPath != "qualifier" || options.CosignExecutable != "cosign" || options.Repository != "localhost:5000/demo" || options.Scratch != "scratch" || options.EvidenceOut != "evidence" || !options.Registry.PlainHTTP || !options.Registry.Anonymous {
					t.Fatal("command lost explicit qualification inputs")
				}
				if mode == "failure" {
					return &lifecycle.LocalQualificationResult{Status: "failed"}, e.New(e.CodeGateUnmet, "fixture refusal")
				}
				return &lifecycle.LocalQualificationResult{Status: "passed", Lane: "unqualified-local-execution"}, nil
			})
			args := []string{"nvkr", "qualify-local", "--workspace", "attempt", "--policy", "policy", "--builder-key", "builder", "--smoke-key", "smoke", "--qualifier-key", "qualifier", "--cosign", "cosign", "--repository", "localhost:5000/demo", "--scratch", "scratch", "--evidence-out", "evidence", "--plain-http", "--anonymous"}
			if mode == "missing" {
				args = []string{"nvkr", "qualify-local"}
			}
			if mode == "positional" {
				args = append(args, "extra")
			}
			app := &cli.App{Commands: []*cli.Command{command}, Writer: &output, ErrWriter: &output}
			err := app.RunContext(t.Context(), args)
			if mode == "missing" || mode == "positional" {
				if e.ExitCode(err) != e.ExitCode(e.New(e.CodeUsage, "")) || calls != 0 {
					t.Fatal("invalid command reached lifecycle", err)
				}
				return
			}
			if calls != 1 {
				t.Fatal("lifecycle invocation count", calls)
			}
			if mode == "failure" {
				if err == nil || !strings.Contains(output.String(), `"status":"failed"`) {
					t.Fatal("failure was not preserved", err)
				}
				return
			}
			if err != nil || !strings.Contains(output.String(), `"lane":"unqualified-local-execution"`) {
				t.Fatal("local result missing", err)
			}
		})
	}
}
