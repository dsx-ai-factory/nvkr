// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
)

func TestDemoDefaultAttemptsAreAcceptedByRunPreparation(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	for _, test := range []struct {
		prefix, definition, environment string
	}{
		{"m1", "docs/examples/nvke-tutorial/definition.yaml", "docs/examples/nvke-tutorial/environment-local.yaml"},
	} {
		t.Run(test.prefix, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", `source scripts/_demo-common.sh; demo_default_attempt "$1"`, "demo-test", test.prefix)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("generate default attempt: %v\n%s", err, output)
			}
			attempt := strings.TrimSpace(string(output))
			prepared, err := lifecycle.PrepareFiles(context.Background(), test.definition, test.environment, "test (test)", attempt)
			if err != nil {
				t.Fatalf("demo default %q rejected by actual Run preparation: %v", attempt, err)
			}
			if prepared.Description().Attempt != attempt {
				t.Fatal("Run preparation did not preserve the generated attempt")
			}
		})
	}
}
