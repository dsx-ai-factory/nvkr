// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"reflect"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGenerationEngineAdmission(t *testing.T) {
	for _, test := range []struct {
		engine string
		code   e.Code
	}{
		{EngineGitHub, ""},
		{" GitHub ", ""},
		{EngineGitLab, ""},
		{EngineTekton, ""},
		{EngineArgo, e.CodeEngineCapability},
		{"local", e.CodeUsage},
		{"jenkins", e.CodeUsage},
	} {
		t.Run(test.engine, func(t *testing.T) {
			err := ValidateGenerationEngine(test.engine)
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			assertFactoryErrorCode(t, err, test.code)
			bundle, err := Render(t.Context(), nil, test.engine)
			assertFactoryErrorCode(t, err, test.code)
			if bundle != nil {
				t.Fatal("unsupported engine returned partial output")
			}
		})
	}
}

// The admitted native GitLab engine occupies the same projection seam as
// GitHub and Tekton: the in-process reference projection transports the same
// finite bundle the in-binary core renders, and the exact validator rejects
// substituted bytes before any output is written.
func TestGitLabProjectionMatchesInBinaryCore(t *testing.T) {
	_, plan := gitlabFixture(t, 1)
	oracle, err := RenderGitLab(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Render(WithProjectionAdapter(t.Context(), ProjectionAdapter{Transport: ProjectionInProcess, Release: projectionTestRelease}), plan, EngineGitLab)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, oracle) {
		t.Fatal("GitLab projection differs from the in-binary native core")
	}
	if err := ValidateGitLabBundle(t.Context(), plan, got); err != nil {
		t.Fatal(err)
	}
}

func TestGitLabProjectionRejectsSubstitutedBytes(t *testing.T) {
	_, plan := gitlabFixture(t, 1)
	emits := 0
	_, err := renderProjected(t.Context(), plan, EngineGitLab, faultyProjection{engine: EngineGitLab, fault: "flip-byte", emits: &emits}, projectionTestRelease)
	assertFactoryErrorCode(t, err, e.CodePipelineDrift)
	if emits != 1 {
		t.Fatalf("plugin received %d Plans, want 1", emits)
	}
}
