// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestTektonCoreChecksNeverSelectProjection(t *testing.T) {
	plan := tektonTestPlan(t)
	bundle, err := renderTekton(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, outcome := range []string{"admitted", "tampered", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			root := t.TempDir()
			if err := WriteGeneratedBundle(t.Context(), root, bundle); err != nil {
				t.Fatal(err)
			}
			forbidGenerationSeam(t)
			ctx, cancel := context.WithCancel(leakedProjectionSelection(t.Context()))
			defer cancel()
			var want e.Code
			switch outcome {
			case "tampered":
				if err := os.WriteFile(filepath.Join(root, tektonResourcePath), []byte("{}"), 0o644); err != nil {
					t.Fatal(err)
				}
				want = e.CodePipelineDrift
			case "canceled":
				cancel()
				want = e.CodeTimeout
			}
			err := CheckGenerated(ctx, plan, EngineTekton, root)
			if want == "" {
				if err != nil {
					t.Fatal(err)
				}
				// Unselected generate --check uses this same core-only path.
				if _, err := Generate(t.Context(), plan, EngineTekton, root, true); err != nil {
					t.Fatal(err)
				}
			} else {
				assertFactoryErrorCode(t, err, want)
			}
		})
	}
}

// Task admission renders its own complete execution binding. A selected
// adapter in the context must never influence that authentication. The native
// Linux admission gate is deliberate; the Linux CI suite exercises this test.
func TestTektonTaskAdmissionNeverSelectsProjection(t *testing.T) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("runtime fixture admits native linux/amd64 only")
	}
	plan := tektonTestPlan(t)
	bundle, err := renderTekton(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	var resources struct {
		Items []struct {
			Kind string
			Spec json.RawMessage
		}
	}
	if err := json.Unmarshal(bundle.Files[tektonResourcePath], &resources); err != nil {
		t.Fatal(err)
	}
	var execution []byte
	for _, item := range resources.Items {
		if item.Kind == "PipelineRun" {
			execution = item.Spec
		}
	}
	if len(execution) == 0 {
		t.Fatal("missing execution binding")
	}
	for _, outcome := range []string{"admitted", "tampered", "canceled"} {
		t.Run(outcome, func(t *testing.T) {
			options := TektonTaskOptions{PlanParts: t.TempDir(), Execution: t.TempDir()}
			for index, raw := 0, bundle.Files[tektonPlanPath]; len(raw) > 0; index++ {
				length := min(tektonChunkBytes, len(raw))
				if err := os.WriteFile(filepath.Join(options.PlanParts, fmt.Sprintf("%03d", index)), raw[:length], 0o600); err != nil {
					t.Fatal(err)
				}
				raw = raw[length:]
			}
			binding := execution
			if outcome == "tampered" {
				binding = []byte("{}")
			}
			if err := os.WriteFile(filepath.Join(options.Execution, "run.json"), binding, 0o600); err != nil {
				t.Fatal(err)
			}
			forbidGenerationSeam(t)
			ctx, cancel := context.WithCancel(leakedProjectionSelection(t.Context()))
			defer cancel()
			if outcome == "canceled" {
				cancel()
			}
			_, err := openTektonRuntime(ctx, options)
			switch outcome {
			case "admitted":
				// Exact binding passed. There is deliberately no cluster token in
				// the unit fixture, so provider admission stops before network I/O.
				assertFactoryErrorCode(t, err, e.CodeTrustPolicyMissing)
				if !strings.Contains(err.Error(), "reading scoped Tekton API token") {
					t.Fatal(err)
				}
			case "tampered":
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
				if !strings.Contains(err.Error(), "complete Plan projection") {
					t.Fatal(err)
				}
			case "canceled":
				// Control reads preserve the public drift wrapper at this boundary.
				assertFactoryErrorCode(t, err, e.CodePipelineDrift)
			}
		})
	}
}
