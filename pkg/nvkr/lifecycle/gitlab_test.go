// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestFinalizeGitLabRejectsMissingContextAndPathsBeforeInputs(t *testing.T) {
	job := factory.GitLabJobContext{ProjectID: 7, PipelineID: 23, JobID: 31, Commit: strings.Repeat("a", 40)}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name      string
		ctx       context.Context
		workspace string
		code      e.Code
	}{
		{"nil context", nil, ".nvkr/factory", e.CodeUsage},
		{"canceled", canceled, ".nvkr/factory", e.CodeTimeout},
		{"empty workspace", t.Context(), "", e.CodeUsage},
		{"foreign workspace", t.Context(), "other-workspace", e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "must-not-exist.json")
			run, err := FinalizeGitLab(test.ctx, "missing-definition.yaml", "missing-environment.yaml", "missing-plan.json", "test", test.workspace, out, job)
			assertCode(t, err, test.code)
			if run != nil {
				t.Fatal("refused finalization returned a Run")
			}
			if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
				t.Fatalf("refused finalization created output: %v", statErr)
			}
		})
	}
}

func TestFinalizeGitLabAdmitsAuthoredDefinitionBeforeGeneratedPlan(t *testing.T) {
	job := factory.GitLabJobContext{ProjectID: 7, PipelineID: 23, JobID: 31, Commit: strings.Repeat("a", 40)}
	out := filepath.Join(t.TempDir(), "must-not-exist.json")
	run, err := FinalizeGitLab(t.Context(), "missing-definition.yaml", "missing-environment.yaml", "missing-plan.json", "test", ".nvkr/factory", out, job)
	assertCode(t, err, e.CodeInvalidCatalog)
	if run != nil {
		t.Fatal("invalid authored inputs returned a Run")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Fatalf("invalid authored inputs created output: %v", statErr)
	}
}
