// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"io"
	"os"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestWorkspaceAcquiredImageObservationPreservesFrozenSubject(t *testing.T) {
	for _, platform := range []string{"linux/amd64", "linux/arm64"} {
		t.Run(platform, func(t *testing.T) {
			t.Chdir(t.TempDir())
			plan, artifacts, result := acquiredImagePlanFixture(t, platform)
			if err := executePackage(t.Context(), plan, planNode(plan, "package/worker"), artifacts, io.Discard); err != nil {
				t.Fatal(err)
			}
			identity, err := plan.IdentityDigest()
			if err != nil {
				t.Fatal(err)
			}
			workspace := ".nvkr/observed-attempt"
			if err := os.Rename(".nvkr/factory", workspace); err != nil {
				t.Fatal(err)
			}
			relocated, err := relocateFactoryPlan(plan, workspace)
			if err != nil {
				t.Fatal(err)
			}
			_, visible := planIndex(relocated)
			// The old mismatch remains rejected; no exact-artifact comparison is relaxed.
			_, err = ObserveLifecyclePayload(t.Context(), plan, plan.LifecycleOutputs[0], visible)
			assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
			observed, err := ObserveWorkspaceLifecyclePayload(t.Context(), plan, plan.LifecycleOutputs[0], workspace, visible)
			if err != nil || observed.Digest != result.SHA256 {
				t.Fatalf("observation=%+v err=%v", observed, err)
			}
			after, err := plan.IdentityDigest()
			if err != nil || identity != after {
				t.Fatalf("original Plan changed: %s %s %v", identity, after, err)
			}
			for _, id := range []string{"artifact/worker/worker", "result/worker/worker"} {
				original := visible[id]
				changed := original
				changed.Path += ".substituted"
				info, err := os.Stat(original.Path)
				if err != nil {
					t.Fatal(err)
				}
				if info.IsDir() {
					if err := os.CopyFS(changed.Path, os.DirFS(original.Path)); err != nil {
						t.Fatal(err)
					}
				} else {
					raw, err := os.ReadFile(original.Path)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(changed.Path, raw, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				visible[id] = changed
				_, err = ObserveWorkspaceLifecyclePayload(t.Context(), plan, plan.LifecycleOutputs[0], workspace, visible)
				assertFactoryErrorCode(t, err, e.CodeProvenanceMissing)
				visible[id] = original
			}
			_, err = ObserveWorkspaceLifecyclePayload(t.Context(), plan, plan.LifecycleOutputs[0], "", visible)
			assertFactoryErrorCode(t, err, e.CodeUsage)
		})
	}
}
