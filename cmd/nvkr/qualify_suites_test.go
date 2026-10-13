// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/urfave/cli/v2"
)

func TestQualifySuitesCLIRejectsConfigurationBeforeExternalWork(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"malformed", "{"},
		{"unknown schema", `{"policy":{"outsideMvpSuites":[],"requiredSuites":[]},"schemaVersion":"future","suites":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private-policy.json")
			if err := os.WriteFile(path, []byte(tc.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			app := &cli.App{Writer: &output, ErrWriter: &output, Commands: []*cli.Command{cmdRunQualifyTests()}}
			args := []string{"nvkr", "qualify-tests", "--definition", "/not-opened/definition.yaml", "--environment", "/not-opened/environment.yaml", "--plan", "/not-opened/plan.json", "--candidate", "/not-opened/candidate.json", "--source-repository", "example/nvkr", "--source-run", "12", "--source-attempt", "1", "--source-commit", strings.Repeat("a", 40), "--source-workflow", ".github/workflows/build.yml", "--default-branch", "main", "--cosign", "/not-executed/cosign", "--cosign-digest", "sha256:" + strings.Repeat("b", 64), "--qualification-workflow", ".github/workflows/qualify.yml", "--suite-config", path}
			err := app.RunContext(t.Context(), args)
			if cliErrorCode(err) != e.CodeVerifyFailed {
				t.Fatalf("configuration rejection lost its code: %v", err)
			}
			if output.Len() != 0 {
				t.Fatalf("rejected configuration emitted a partial result: %q", output.String())
			}
		})
	}
}
