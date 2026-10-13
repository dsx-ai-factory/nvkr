// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/urfave/cli/v2"
)

func TestQualifySuitesCLIDelegatesOnlyIndependentPolicy(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  []string
		calls int
	}{
		{"selected", []string{"--policy", "native.yaml", "--suite-config", "suite.json"}, 1},
		{"missing native", []string{"--suite-config", "suite.json"}, 0},
		{"missing suites", []string{"--policy", "native.yaml"}, 0},
		{"empty workspace", []string{"--policy", "native.yaml", "--suite-config", "suite.json", "--workspace", ""}, 0},
		{"positional source", []string{"--policy", "native.yaml", "--suite-config", "suite.json", "12"}, 0},
		{"caller source flag", []string{"--policy", "native.yaml", "--suite-config", "suite.json", "--source-run", "12"}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			var output bytes.Buffer
			publish := func(_ context.Context, policy, configuration string, options lifecycle.QualificationRuntimeOptions) (*qualification.PublicationV3, error) {
				calls++
				if policy != "native.yaml" || configuration != "suite.json" || options.WorkspaceRoot != ".nvkr/qualification" {
					t.Fatal("independent inputs changed")
				}
				return nil, e.New(e.CodeEngineCapability, "fixture refusal")
			}
			app := &cli.App{Writer: &output, ErrWriter: &output, Commands: []*cli.Command{cmdRunQualifyTestsWithPublisher(publish)}}
			err := app.RunContext(t.Context(), append([]string{"nvkr", "qualify-tests"}, tc.args...))
			if err == nil || calls != tc.calls {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			if tc.calls == 1 && cliErrorCode(err) != e.CodeEngineCapability {
				t.Fatal(err)
			}
		})
	}
}
