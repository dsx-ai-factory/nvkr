// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"os"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestLocalRejectsGitHubDevelopmentBeforeWork(t *testing.T) {
	for _, test := range []struct {
		name   string
		dryRun bool
	}{
		{name: "execution"},
		{name: "dry run", dryRun: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := githubTestPlan(t)
			plan.Environment.Publication = nil
			plan.Environment.GitHubDevelopment = true
			stack, err := catalog.ApplyStack(plan.CatalogSnapshot)
			if err != nil {
				t.Fatal(err)
			}
			plan, err = Compile(t.Context(), plan.Definition, plan.Environment, stack, plan.GeneratorVersion)
			if err != nil {
				t.Fatal(err)
			}
			t.Chdir(t.TempDir())
			var output bytes.Buffer
			options := LocalRunOptions{Workspace: ".nvkr/development", DryRun: test.dryRun, Stdout: &output, Stderr: &output}
			assertFactoryErrorCode(t, AdmitLocal(t.Context(), plan, options), e.CodeEngineCapability)
			result, err := RunLocal(t.Context(), plan, options)
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if result != nil || output.Len() != 0 {
				t.Fatalf("local admission returned partial output: %v %q", result, output.String())
			}
			if _, err := os.Stat(".nvkr"); !os.IsNotExist(err) {
				t.Fatalf("local admission touched workspace: %v", err)
			}
		})
	}
}
