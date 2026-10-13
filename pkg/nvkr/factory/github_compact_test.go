// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestGitHubWorkflowSizeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   []byte
		valid bool
	}{
		{"under", bytes.Repeat([]byte("x"), maxGitHubWorkflowBytes-1), true},
		{"exact", bytes.Repeat([]byte("x"), maxGitHubWorkflowBytes), true},
		{"over", bytes.Repeat([]byte("x"), maxGitHubWorkflowBytes+1), false},
		{"UTF8 bytes", []byte(strings.Repeat("é", maxGitHubWorkflowBytes/2) + "x"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateGitHubWorkflowSize(tc.raw)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			}
		})
	}
}

func TestOversizedWorkflowCannotWriteGeneratedBundle(t *testing.T) {
	plan := githubTestPlan(t)
	bundle, err := RenderGitHub(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	raw := bytes.Repeat([]byte("x"), maxGitHubWorkflowBytes+1)
	bundle.Files[GitHubWorkflowPath] = raw
	bundle.WorkflowDigest = pipeline.FileDigest(raw)
	out := filepath.Join(t.TempDir(), "must-not-exist")
	assertFactoryErrorCode(t, WriteGeneratedBundle(t.Context(), out, bundle), e.CodeEngineCapability)
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatalf("oversized workflow wrote output: %v", err)
	}
}

func TestMaintainedWorkflowFitsGitHubFileLimit(t *testing.T) {
	raw, err := os.ReadFile("../../../" + GitHubWorkflowPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateGitHubWorkflowSize(raw); err != nil {
		t.Fatal(err)
	}
}
