// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"os"
	"reflect"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestTektonProjectionEquivalence(t *testing.T) {
	plan := tektonTestPlan(t)
	oracle, err := renderTekton(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, driver := range []struct {
		name    string
		adapter *ProjectionAdapter
		calls   int
	}{
		{"built-in", nil, 2},
		{"in-process", &ProjectionAdapter{Transport: ProjectionInProcess, Release: projectionTestRelease}, 2},
	} {
		t.Run(driver.name, func(t *testing.T) {
			ctx := t.Context()
			if driver.adapter != nil {
				ctx = WithProjectionAdapter(ctx, *driver.adapter)
			}
			record := recordProjectionCalls(t)
			got, err := Render(ctx, plan, EngineTekton)
			if err != nil || !reflect.DeepEqual(got, oracle) {
				t.Fatalf("projection differs from core: %v", err)
			}
			if record.calls != driver.calls || record.plans != 1 {
				t.Fatalf("contract calls = %+v, want %d calls and one Plan", record, driver.calls)
			}
			root := t.TempDir()
			for _, check := range []bool{false, true} {
				digest, err := Generate(ctx, plan, EngineTekton, root, check)
				if err != nil || digest != oracle.PlanDigest {
					t.Fatalf("Generate(check=%t): digest=%s error=%v", check, digest, err)
				}
			}
			if err := CheckGenerated(leakedProjectionSelection(ctx), plan, EngineTekton, root); err != nil {
				t.Fatal(err)
			}
			if err := CheckGeneratedBundle(ctx, root, oracle); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func assertProjectionDirectoryEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("refused generation left partial output: %v, %v", entries, err)
	}
}

func TestTektonProjectionRejectsNilContext(t *testing.T) {
	plan := tektonTestPlan(t)
	var ctx context.Context
	t.Run("render", func(t *testing.T) {
		bundle, err := Render(ctx, plan, EngineTekton)
		assertFactoryErrorCode(t, err, e.CodeUsage)
		if bundle != nil {
			t.Fatal("nil context returned a bundle")
		}
	})
	for _, check := range []bool{false, true} {
		name := "write"
		if check {
			name = "check"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			digest, err := Generate(ctx, plan, EngineTekton, root, check)
			assertFactoryErrorCode(t, err, e.CodeUsage)
			if digest != "" {
				t.Fatal("nil context returned a Plan digest")
			}
			assertProjectionDirectoryEmpty(t, root)
		})
	}
}

func TestProjectionUnknownEngineAdmittedBeforeIO(t *testing.T) {
	for _, engine := range []string{"unknown", EngineGitLab, EngineArgo, "local"} {
		t.Run(engine, func(t *testing.T) {
			want := e.CodeEngineCapability
			if engine == "unknown" || engine == "local" {
				want = e.CodeUsage
			}
			forbidGenerationSeam(t)
			root := t.TempDir()
			_, err := Generate(leakedProjectionSelection(t.Context()), nil, engine, root, false)
			assertFactoryErrorCode(t, err, want)
			assertProjectionDirectoryEmpty(t, root)
		})
	}
}
