// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

func TestArtifactTestInputCommand(t *testing.T) {
	lifecycleCommandRepo(t)
	for _, output := range []string{"nvkectl", "nvkectl-linux-arm64", "missing", ""} {
		t.Run(output, func(t *testing.T) {
			args := []string{"run", "artifact-test-inputs", "--plan", ".github/nvkr-run/plan.json"}
			if output != "" {
				args = append(args, "--output", output)
			}
			raw, err := invokeLifecycleCLI(t, args...)
			if output == "missing" || output == "" {
				if err == nil || raw != "" {
					t.Fatalf("invalid selection produced output: %q, %v", raw, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var inputs factory.CLIArtifactTestInputs
			if err := json.Unmarshal([]byte(raw), &inputs); err != nil {
				t.Fatal(err)
			}
			if inputs.Output != output || inputs.ToolchainImage == "" || inputs.Commit == "" || inputs.ExpectedVersion == "" {
				t.Fatalf("incomplete CLI inputs: %+v", inputs)
			}
		})
	}
}
