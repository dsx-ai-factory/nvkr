// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestLocalCommandCapturedPreparation(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, tc := range []struct {
		name, root string
		code       e.Code
	}{
		{"repository", ".", ""},
		{"empty root", "", e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			execute := func(_ context.Context, prepared *lifecycle.PreparedRun, _ lifecycle.LocalOptions) (*lifecycle.Run, error) {
				called = true
				want, err := lifecycle.PrepareCapturedFiles(t.Context(), tc.root,
					"docs/examples/nvke-tutorial/definition.yaml", "docs/examples/nvke-tutorial/environment-local.yaml",
					factoryGeneratorVersion(), "presenter-test")
				if err != nil || prepared.Description().PlanDigest != want.Description().PlanDigest {
					t.Fatalf("CLI did not delegate captured preparation: %v", err)
				}
				return completedPresentationRun(prepared), nil
			}
			app := localPresentationTestApp(execute, io.Discard, defaultRunPresentationServices())
			err := app.RunContext(t.Context(), localPresentationArgs("--ui=plain", "--capture-inputs-root="+tc.root))
			if tc.code != "" {
				assertLifecycleCommandCode(t, err, tc.code)
				if called {
					t.Fatal("invalid capture root reached execution")
				}
			} else if err != nil || !called {
				t.Fatalf("captured preparation failed: called=%t err=%v", called, err)
			}
		})
	}
}
