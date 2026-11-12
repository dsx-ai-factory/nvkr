// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestImageUsesMixedHelperRefusesBeforeOutput(t *testing.T) {
	t.Chdir(t.TempDir())
	plan := recompileGitHubStabilityPlan(t, mixedLocalUtilityPlan(t), func(in *githubStabilityInputs) {
		in.environment.ToolchainAccess = nil
	})
	_, err := plan.ImageUses("local")
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
	err = ExecuteNode(t.Context(), plan, "worker/sbom", io.Discard, io.Discard)
	_, artifacts := planIndex(plan)
	if _, statErr := os.Stat(artifacts["sbom/worker"].Path); !os.IsNotExist(statErr) {
		t.Errorf("unadmitted helper wrote SBOM output: %v", statErr)
	}
	assertFactoryErrorCode(t, err, e.CodeEngineCapability)
}

func TestImageUsesLocalParentRefusesBeforeRuntime(t *testing.T) {
	for _, boundary := range []string{"static admission", "host admission", "run"} {
		t.Run(boundary, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			plan := recompileGitHubStabilityPlan(t, buildTransportPlan(t, "source"), func(in *githubStabilityInputs) {
				in.environment.ToolchainAccess = nil
			})
			_, err := plan.ImageUses("local")
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			marker := filepath.Join(root, "runtime-called")
			t.Setenv("NVKR_TEST_RUNTIME_MARKER", marker)
			runtime := filepath.Join(root, "runtime")
			if err := os.WriteFile(runtime, []byte("#!/bin/sh\nprintf called > \"$NVKR_TEST_RUNTIME_MARKER\"\nexit 73\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			options := LocalRunOptions{Workspace: ".nvkr/local-image-admission", Runtime: runtime}
			switch boundary {
			case "static admission":
				err = AdmitLocalPlan(t.Context(), plan)
			case "host admission":
				err = AdmitLocal(t.Context(), plan, options)
			case "run":
				var result *LocalRunResult
				result, err = RunLocal(t.Context(), plan, options)
				if result != nil {
					_ = result.Close()
					t.Error("unadmitted parent returned a Run")
				}
			}
			for _, path := range []string{marker, options.Workspace} {
				if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
					t.Errorf("parent touched %s before image admission: %v", path, statErr)
				}
			}
			assertFactoryErrorCode(t, err, e.CodeEngineCapability)
			if !strings.Contains(err.Error(), "image access") {
				t.Fatalf("parent failed after the image admission boundary: %v", err)
			}
		})
	}
}
