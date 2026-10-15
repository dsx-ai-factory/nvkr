// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/urfave/cli/v2"
)

func TestReleaseExportAdmissionPrecedesVerification(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, flags := range [][]string{
		{"--export-layout", "export"},
		{"--expected-invocation", "https://github.com/example/nvkr/actions/runs/1/attempts/1"},
		{"--export-layout", "export", "--expected-invocation", "invocation"},
	} {
		t.Run(flags[0], func(t *testing.T) {
			verify := func(context.Context, string, lifecycle.VerificationPolicy,
				lifecycle.ConsumerOptions, *lifecycle.ExactInputExpectation, releaseExportOptions,
			) (releaseVerificationResult, error) {
				t.Fatal("incomplete export reached registry/signature verification")
				return releaseVerificationResult{}, nil
			}
			app := &cli.App{Commands: []*cli.Command{cmdReleaseWithVerify(verify)}}
			args := []string{"nvkr", "release", "verify", "--reference", "example.test/release@sha256:" + strings.Repeat("a", 64),
				"--lane", "pipeline-attested", "--scope", "artifact-publication"}
			if got := cliErrorCode(app.RunContext(t.Context(), append(args, flags...))); got != e.CodeUsage {
				t.Fatalf("export admission returned %s", got)
			}
		})
	}
}

func TestReleaseExportForwardsExactInvocationAndDestination(t *testing.T) {
	lifecycleCommandRepo(t)
	const invocation = "https://github.com/example/nvkr/actions/runs/1/attempts/2"
	called := false
	verify := func(_ context.Context, _ string, _ lifecycle.VerificationPolicy,
		_ lifecycle.ConsumerOptions, expectation *lifecycle.ExactInputExpectation, export releaseExportOptions,
	) (releaseVerificationResult, error) {
		called = true
		if expectation == nil || export.Invocation != invocation || export.Directory != "new-layout" {
			t.Fatalf("export was detached from exact inputs/origin: %+v", export)
		}
		return releaseVerificationResult{}, e.New(e.CodeVerifyFailed, "wrong originating invocation")
	}
	app := &cli.App{Commands: []*cli.Command{cmdReleaseWithVerify(verify)}}
	err := app.RunContext(t.Context(), []string{"nvkr", "release", "verify",
		"--reference", "example.test/release@sha256:" + strings.Repeat("a", 64),
		"--lane", "pipeline-attested", "--scope", "artifact-publication", "--identity", "^builder$", "--issuer", "issuer",
		"--definition", "definitions/nvke-m2/definition.yaml", "--environment", "definitions/nvke-m2/environment-github.yaml",
		"--plan", ".github/nvkr-run/plan.json", "--export-layout", "new-layout", "--expected-invocation", invocation})
	if !called || cliErrorCode(err) != e.CodeVerifyFailed {
		t.Fatalf("export origin error did not survive command boundary: called=%v err=%v", called, err)
	}
}
